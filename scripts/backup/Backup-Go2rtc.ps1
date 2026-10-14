<#
.SYNOPSIS
    Backs up the go2rtc-custom data directory using Restic, to a local
    repository (on a different physical disk than the data) and a Synology
    NAS repository reached over SMB/CIFS.

.DESCRIPTION
    See scripts/backup/README.md for one-time setup (installing restic.exe
    and sqlite3.exe, securing the Restic password file, storing the NAS
    credential via cmdkey, registering this script in Task Scheduler)
    before running this for the first time.

    Design notes (see docs/data-backup-plan.md for the full rationale):
    - The whole app's persisted state lives in one directory ($DataDir,
      the folder containing go2rtc.yaml) — backing it up is backing up
      the whole system.
    - trafficflow.db is a live SQLite WAL database; instead of letting
      restic read it directly while go2rtc.exe may be mid-write, this
      script first takes an online, transactionally-consistent copy via
      `sqlite3 .backup` into a subfolder that sits inside $DataDir, so it
      is picked up by the same single `restic backup $DataDir` call with
      no extra paths or excludes needed.
    - Backing up to the local repo and the NAS repo are independent: a
      NAS hiccup (network, share offline) does not prevent the local
      backup from completing, and vice versa.
#>

# ---------------------------------------------------------------------------
# Config — edit these for your server
# ---------------------------------------------------------------------------

# Directory containing go2rtc.yaml and all data subfolders (see
# docs/data-backup-plan.md §1.2 for the full list of what lives here).
$DataDir = "D:\Working\Counting"

# Local Restic repository — ideally on a different physical disk than
# $DataDir, so a single disk failure can't take out both.
$LocalRepo = "D:\go2rtc-backups\restic-repo-local"

# Synology NAS repository, reached over SMB via a UNC path. Requires the NAS
# credential to already be stored via `cmdkey` (see README.md step 3) so
# this script can reach it non-interactively from Task Scheduler.
$NasRepo = "\\NAS-IP-OR-HOSTNAME\go2rtc-backup\restic-repo-nas"

# File holding the Restic repository password — NOT inside $DataDir (so it
# is never itself swept into the backup it protects), ACL-restricted (see
# README.md step 2). Losing this file AND every copy of the passphrase
# makes every snapshot permanently unreadable — keep a copy somewhere else.
$ResticPasswordFile = "C:\go2rtc-backup-secrets\restic-password.txt"

# Paths to the restic.exe / sqlite3.exe binaries (see README.md step 1).
$ResticExe = "C:\Tools\restic\restic.exe"
$Sqlite3Exe = "C:\Tools\sqlite3\sqlite3.exe"

# Retention policy applied to BOTH repos after every successful backup.
$KeepDaily = 7
$KeepWeekly = 4
$KeepMonthly = 6

# Log file.
$LogFile = "D:\go2rtc-backups\backup.log"

# ---------------------------------------------------------------------------
# Script
# ---------------------------------------------------------------------------

$ErrorActionPreference = "Stop"

function Write-Log {
    param([string]$Message)
    $line = "[{0}] {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $Message
    Write-Host $line
    New-Item -ItemType Directory -Path (Split-Path $LogFile) -Force | Out-Null
    Add-Content -Path $LogFile -Value $line
}

function Invoke-Restic {
    param([string]$Repo, [string[]]$Args)
    & $ResticExe -r $Repo --password-file $ResticPasswordFile @Args
    if ($LASTEXITCODE -ne 0) {
        throw "restic exited with code $LASTEXITCODE (repo: $Repo, args: $($Args -join ' '))"
    }
}

function Ensure-ResticRepo {
    param([string]$Repo)
    & $ResticExe -r $Repo --password-file $ResticPasswordFile snapshots *> $null 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Log "Repo chưa tồn tại, khởi tạo: $Repo"
        Invoke-Restic -Repo $Repo -Args @("init")
    }
}

function Snapshot-TrafficFlowDb {
    $dbPath = Join-Path $DataDir "trafficflow_data\trafficflow.db"
    if (-not (Test-Path $dbPath)) {
        return
    }

    $snapshotDir = Join-Path $DataDir "trafficflow_data\_sqlite_snapshot"
    New-Item -ItemType Directory -Path $snapshotDir -Force | Out-Null
    $snapshotPath = Join-Path $snapshotDir "trafficflow.db"
    if (Test-Path $snapshotPath) {
        Remove-Item $snapshotPath -Force
    }

    # .backup takes an online, transactionally-consistent copy even while
    # go2rtc.exe is writing to the live DB (WAL mode) — safer than letting
    # restic read trafficflow.db directly mid-write.
    & $Sqlite3Exe $dbPath ".backup '$snapshotPath'"
    if ($LASTEXITCODE -ne 0) {
        throw "sqlite3 .backup thất bại (exit $LASTEXITCODE)"
    }
    Write-Log "Đã tạo snapshot nhất quán cho trafficflow.db: $snapshotPath"
}

function Backup-ToRepo {
    param([string]$Repo, [string]$Label)

    Write-Log "Bắt đầu backup -> $Label ($Repo)"
    Ensure-ResticRepo -Repo $Repo
    Invoke-Restic -Repo $Repo -Args @("backup", $DataDir)
    Write-Log "Backup xong -> $Label"

    Write-Log "Dọn bản cũ (forget --prune) -> $Label"
    Invoke-Restic -Repo $Repo -Args @(
        "forget", "--prune",
        "--keep-daily", $KeepDaily,
        "--keep-weekly", $KeepWeekly,
        "--keep-monthly", $KeepMonthly
    )
    Write-Log "Dọn xong -> $Label"
}

$failures = @()

Write-Log "===== Bắt đầu backup go2rtc ====="

try {
    Snapshot-TrafficFlowDb
}
catch {
    Write-Log "LỖI khi tạo snapshot SQLite: $($_.Exception.Message)"
    $failures += "sqlite-snapshot"
}

try {
    Backup-ToRepo -Repo $LocalRepo -Label "LOCAL (HDD)"
}
catch {
    Write-Log "LỖI khi backup -> LOCAL (HDD): $($_.Exception.Message)"
    $failures += "local"
}

try {
    Backup-ToRepo -Repo $NasRepo -Label "NAS (SMB)"
}
catch {
    Write-Log "LỖI khi backup -> NAS (SMB): $($_.Exception.Message)"
    $failures += "nas"
}

if ($failures.Count -gt 0) {
    Write-Log "===== Backup KẾT THÚC VỚI LỖI: $($failures -join ', ') ====="
    exit 1
}
else {
    Write-Log "===== Backup hoàn tất, không lỗi ====="
    exit 0
}
