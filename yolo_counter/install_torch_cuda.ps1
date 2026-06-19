#Requires -Version 5.1
# Install PyTorch with CUDA support.
# Detects CUDA version from nvidia-smi then installs the matching torch build.
# Run: powershell -ExecutionPolicy Bypass -File install_torch_cuda.ps1

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function OK  ($m) { Write-Host "[OK]  $m" -ForegroundColor Green }
function INF ($m) { Write-Host "[--]  $m" -ForegroundColor Cyan }
function WRN ($m) { Write-Host "[!!]  $m" -ForegroundColor Yellow }
function ERR ($m) { Write-Host "[XX]  $m" -ForegroundColor Red }

# ── 1. Find Python ────────────────────────────────────────────────────────────
$pyExe = $null
foreach ($cmd in @("python", "python3")) {
    try { $pyExe = (Get-Command $cmd -ErrorAction Stop).Source; break } catch {}
}
if (-not $pyExe) { ERR "python not found in PATH."; Read-Host "Press Enter"; exit 1 }
INF "Python: $pyExe"

# ── 2. Detect CUDA version from nvidia-smi ───────────────────────────────────
INF "Detecting GPU / CUDA version..."
$cudaIndex = $null
try {
    $nv = & nvidia-smi 2>&1 | Select-String "CUDA Version"
    if ($nv) {
        # Line looks like:  | CUDA Version: 12.8     |
        if ($nv.Line -match "CUDA Version:\s*(\d+)\.(\d+)") {
            $major = [int]$Matches[1]
            $minor = [int]$Matches[2]
            $cudaVer = "$major.$minor"
            INF "nvidia-smi reports CUDA $cudaVer"

            # Map to closest PyTorch wheel index
            if     ($major -gt 12 -or ($major -eq 12 -and $minor -ge 8)) { $cudaIndex = "cu128" }
            elseif ($major -eq 12 -and $minor -ge 4)                      { $cudaIndex = "cu124" }
            elseif ($major -eq 12 -and $minor -ge 1)                      { $cudaIndex = "cu121" }
            elseif ($major -eq 11 -and $minor -ge 8)                      { $cudaIndex = "cu118" }
            else {
                WRN "CUDA $cudaVer is older than 11.8 -- GPU support may be limited."
                $cudaIndex = "cu118"
            }
        }
    }
} catch {
    WRN "nvidia-smi not found or failed: $_"
}

if (-not $cudaIndex) {
    WRN "Could not auto-detect CUDA version."
    Write-Host ""
    Write-Host "  Select PyTorch CUDA build:"
    Write-Host "    1) cu128  (CUDA 12.8+, driver 570+)"
    Write-Host "    2) cu124  (CUDA 12.4,  driver 550+)"
    Write-Host "    3) cu121  (CUDA 12.1,  driver 525+)"
    Write-Host "    4) cu118  (CUDA 11.8,  driver 450+)"
    $choice = Read-Host "Enter 1-4"
    switch ($choice) {
        "1" { $cudaIndex = "cu128" }
        "2" { $cudaIndex = "cu124" }
        "3" { $cudaIndex = "cu121" }
        "4" { $cudaIndex = "cu118" }
        default { ERR "Invalid choice."; Read-Host "Press Enter"; exit 1 }
    }
}

$indexUrl = "https://download.pytorch.org/whl/$cudaIndex"
INF "Target: torch+$cudaIndex  ($indexUrl)"
Write-Host ""

# ── 3. Uninstall old CPU-only torch (if present) ────────────────────────────
INF "Uninstalling existing torch / torchvision (if any)..."
& $pyExe -m pip uninstall -y torch torchvision 2>&1 | ForEach-Object { Write-Host "  $_" }
Write-Host ""

# ── 4. Install CUDA torch ───────────────────────────────────────────────────
INF "Installing torch+$cudaIndex  (this may take a few minutes)..."
& $pyExe -m pip install torch torchvision --index-url $indexUrl
if ($LASTEXITCODE -ne 0) {
    ERR "pip install failed (exit $LASTEXITCODE)."
    Read-Host "Press Enter"
    exit 1
}
Write-Host ""

# ── 5. Verify ───────────────────────────────────────────────────────────────
INF "Verifying..."
$verScript = @"
import torch
print(f'PyTorch : {torch.__version__}')
print(f'CUDA build : {torch.version.cuda}')
print(f'CUDA available : {torch.cuda.is_available()}')
print(f'GPU count  : {torch.cuda.device_count()}')
if torch.cuda.is_available():
    for i in range(torch.cuda.device_count()):
        p = torch.cuda.get_device_properties(i)
        print(f'  GPU {i}: {p.name}  {p.total_memory//1024**2} MiB')
"@

$result = & $pyExe -c $verScript 2>&1
$result | ForEach-Object { Write-Host "  $_" }
Write-Host ""

if ($result -match "CUDA available : True") {
    OK "GPU is ready! Restart go2rtc to apply."
} else {
    WRN "CUDA still not available after install."
    WRN "Check that this Python is the same one used by yolo_counter:"
    WRN "  $pyExe"
}

Read-Host "Press Enter to exit"
