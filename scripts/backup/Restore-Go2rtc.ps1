<#
.SYNOPSIS
    Restores a go2rtc-custom data directory from a Restic snapshot (local or
    NAS repo).

.DESCRIPTION
    Interactive helper: lists the snapshots available in the chosen repo,
    then restores one into a target directory you choose. This script NEVER
    restores directly on top of a live data directory — stop go2rtc.exe
    first, then copy the restored files over yourself once you've checked
    them (see the notes printed at the end, and
    docs/data-backup-plan.md §3 mục 3 / §5 for the full procedure).

.EXAMPLE
    .\Restore-Go2rtc.ps1 -Repo "D:\go2rtc-backups\restic-repo-local" -RestoreTo "D:\go2rtc-restore-test"

.EXAMPLE
    .\Restore-Go2rtc.ps1 -Repo "\\NAS-IP-OR-HOSTNAME\go2rtc-backup\restic-repo-nas" -RestoreTo "D:\go2rtc-restore-test" -SnapshotId "a1b2c3d4"
#>

param(
    [Parameter(Mandatory = $true)]
    [string]$Repo,

    [Parameter(Mandatory = $true)]
    [string]$RestoreTo,

    [string]$SnapshotId = "latest",

    [string]$ResticExe = "C:\Tools\restic\restic.exe",
    [string]$ResticPasswordFile = "C:\go2rtc-backup-secrets\restic-password.txt"
)

$ErrorActionPreference = "Stop"

Write-Host "Các snapshot có trong repo ($Repo):"
& $ResticExe -r $Repo --password-file $ResticPasswordFile snapshots
if ($LASTEXITCODE -ne 0) {
    throw "restic snapshots thất bại (exit $LASTEXITCODE) — kiểm tra lại đường dẫn repo / mật khẩu / kết nối NAS"
}

Write-Host "`nĐang khôi phục snapshot '$SnapshotId' vào: $RestoreTo"
& $ResticExe -r $Repo --password-file $ResticPasswordFile restore $SnapshotId --target $RestoreTo
if ($LASTEXITCODE -ne 0) {
    throw "restic restore thất bại (exit $LASTEXITCODE)"
}

Write-Host ""
Write-Host "Khôi phục xong vào: $RestoreTo"
Write-Host "Các bước còn lại (làm TAY, script này không tự đè lên dữ liệu đang chạy):"
Write-Host "  1. Dừng go2rtc.exe trước khi copy bất kỳ gì vào thư mục dữ liệu thật."
Write-Host "  2. Nếu bản restore có file trafficflow_data\_sqlite_snapshot\trafficflow.db,"
Write-Host "     dùng chính file đó để thay cho trafficflow_data\trafficflow.db (bản chụp"
Write-Host "     nhất quán qua sqlite3 .backup) — xoá trafficflow.db-wal/-shm cũ nếu có."
Write-Host "  3. .jwt_secret và integrations.json BẮT BUỘC phải cùng lấy từ snapshot này,"
Write-Host "     không trộn với snapshot khác (xem docs/data-backup-plan.md §3 mục 3)."
Write-Host "  4. Khởi động lại go2rtc.exe và kiểm tra lại trước khi coi là xong."
