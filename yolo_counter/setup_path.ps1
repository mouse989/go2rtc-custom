#Requires -Version 5.1
# Add Python Scripts directory to current user PATH (no Admin needed).
# Usage:  powershell -ExecutionPolicy Bypass -File setup_path.ps1

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function OK  ($m) { Write-Host "[OK]  $m" -ForegroundColor Green }
function INF ($m) { Write-Host "[--]  $m" -ForegroundColor Cyan }
function WRN ($m) { Write-Host "[!!]  $m" -ForegroundColor Yellow }
function ERR ($m) { Write-Host "[XX]  $m" -ForegroundColor Red }

INF "Detecting Python..."

# 1. Find python.exe
$pyExe = $null
foreach ($cmd in @("python", "python3")) {
    try { $pyExe = (Get-Command $cmd -ErrorAction Stop).Source; break } catch {}
}
if (-not $pyExe) {
    ERR "python / python3 not found in PATH."
    ERR "Install Python from https://www.python.org/downloads/ then re-run."
    Read-Host "Press Enter to exit"
    exit 1
}
INF "Python: $pyExe"

# 2. Get Scripts directory for this Python
$pyScripts = (& $pyExe -c "import sysconfig; print(sysconfig.get_path('scripts'))").Trim()
if ($LASTEXITCODE -ne 0) {
    ERR "Cannot determine Scripts path."
    Read-Host "Press Enter to exit"
    exit 1
}
INF "Scripts dir: $pyScripts"

if (-not (Test-Path $pyScripts)) {
    WRN "Directory does not exist yet (will be created when packages are installed)."
}

# 3. Read current user PATH
$scope   = [System.EnvironmentVariableTarget]::User
$current = [Environment]::GetEnvironmentVariable("PATH", $scope)
if (-not $current) { $current = "" }
$paths = $current -split ";" | Where-Object { $_.Trim() -ne "" }

# 4. Add Scripts dir
if ($paths -contains $pyScripts) {
    OK "Scripts dir already in PATH -- nothing to do."
} else {
    $newPath = ($paths + $pyScripts) -join ";"
    [Environment]::SetEnvironmentVariable("PATH", $newPath, $scope)
    OK "Added to user PATH: $pyScripts"
}

# 5. Add Python executable dir
$pyDir = Split-Path $pyExe -Parent
$paths2 = ([Environment]::GetEnvironmentVariable("PATH", $scope)) -split ";" | Where-Object { $_.Trim() -ne "" }
if ($paths2 -notcontains $pyDir) {
    [Environment]::SetEnvironmentVariable("PATH", ($paths2 + $pyDir -join ";"), $scope)
    OK "Added Python dir to PATH: $pyDir"
} else {
    OK "Python dir already in PATH."
}

# 6. Refresh current session
$machinePath = [Environment]::GetEnvironmentVariable("PATH", [System.EnvironmentVariableTarget]::Machine)
$userPath    = [Environment]::GetEnvironmentVariable("PATH", [System.EnvironmentVariableTarget]::User)
$env:PATH    = "$userPath;$machinePath"

Write-Host ""
OK "Done! Open a new Command Prompt or PowerShell for PATH to take effect."
INF "Test: uvicorn --version"
INF "Test: yolo --version"
Write-Host ""
Read-Host "Press Enter to exit"
