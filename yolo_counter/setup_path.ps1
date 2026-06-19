#Requires -Version 5.1
<#
.SYNOPSIS
    Thêm thư mục Scripts của Python vào PATH của người dùng hiện tại.
    Chạy một lần sau khi cài pip packages. Không cần quyền Administrator.

.USAGE
    Mở PowerShell, cd vào thư mục này rồi chạy:
        powershell -ExecutionPolicy Bypass -File setup_path.ps1
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Ok  ($msg) { Write-Host "[OK]  $msg" -ForegroundColor Green }
function Write-Inf ($msg) { Write-Host "[--]  $msg" -ForegroundColor Cyan }
function Write-Wrn ($msg) { Write-Host "[!!]  $msg" -ForegroundColor Yellow }
function Write-Err ($msg) { Write-Host "[XX]  $msg" -ForegroundColor Red }

Write-Inf "Tìm Python đang dùng..."

# 1. Tìm python.exe (ưu tiên `python`, thử cả `python3`)
$pyExe = $null
foreach ($cmd in @("python", "python3")) {
    try {
        $p = Get-Command $cmd -ErrorAction Stop
        $pyExe = $p.Source
        break
    } catch { }
}

if (-not $pyExe) {
    Write-Err "Không tìm thấy python / python3 trong PATH."
    Write-Err "Hãy cài Python từ https://www.python.org/downloads/ rồi chạy lại."
    exit 1
}

Write-Inf "Python: $pyExe"

# 2. Lấy thư mục Scripts của Python này
$pyScripts = & $pyExe -c "import sysconfig; print(sysconfig.get_path('scripts'))" 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Err "Không lấy được đường dẫn Scripts từ Python: $pyScripts"
    exit 1
}
$pyScripts = $pyScripts.Trim()
Write-Inf "Scripts dir: $pyScripts"

# 3. Kiểm tra thư mục tồn tại
if (-not (Test-Path $pyScripts)) {
    Write-Wrn "Thư mục chưa tồn tại (sẽ tạo sau khi cài package): $pyScripts"
}

# 4. Đọc PATH hiện tại của user (không sửa PATH hệ thống, không cần Admin)
$scope   = [System.EnvironmentVariableTarget]::User
$current = [Environment]::GetEnvironmentVariable("PATH", $scope) ?? ""

$paths = $current -split ";" | Where-Object { $_.Trim() -ne "" }

if ($paths -contains $pyScripts) {
    Write-Ok  "Đường dẫn đã có trong PATH, không cần thêm."
} else {
    $newPath = ($paths + $pyScripts) -join ";"
    [Environment]::SetEnvironmentVariable("PATH", $newPath, $scope)
    Write-Ok  "Đã thêm vào PATH (User): $pyScripts"
}

# 5. Thêm thư mục chứa python.exe nếu chưa có
$pyDir = Split-Path $pyExe -Parent
if ($paths -notcontains $pyDir) {
    $current2 = [Environment]::GetEnvironmentVariable("PATH", $scope)
    [Environment]::SetEnvironmentVariable("PATH", "$current2;$pyDir", $scope)
    Write-Ok  "Đã thêm thư mục Python vào PATH: $pyDir"
} else {
    Write-Ok  "Thư mục Python đã có trong PATH."
}

# 6. Cập nhật PATH trong phiên PowerShell hiện tại (có hiệu lực ngay)
$env:PATH = [Environment]::GetEnvironmentVariable("PATH", $scope) + ";" +
            [Environment]::GetEnvironmentVariable("PATH", [System.EnvironmentVariableTarget]::Machine)

Write-Host ""
Write-Ok  "Xong! Mở Command Prompt / PowerShell mới để PATH có hiệu lực."
Write-Inf "Kiểm tra: uvicorn --version"
Write-Inf "Kiểm tra: yolo --version"
