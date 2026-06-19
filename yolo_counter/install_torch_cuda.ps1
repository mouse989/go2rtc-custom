#Requires -Version 5.1
# Install PyTorch with CUDA support.
# Detects CUDA version from nvidia-smi then installs the matching torch build.
# Also detects and removes shadowing CPU-only torch installed in user site-packages.
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

# ── 2. Detect ALL torch installs (user-space may shadow system) ───────────────
INF "Checking installed torch locations..."
$locScript = @'
import importlib, sys, os
# Show every torch found in sys.path order
found = []
for p in sys.path:
    t = os.path.join(p, "torch")
    if os.path.isdir(t):
        found.append(t)
# Also show the one actually imported
try:
    import torch
    print(f"ACTIVE: {torch.__version__}  path={torch.__file__}")
    print(f"ACTIVE_CUDA: {torch.version.cuda or 'None'}")
except Exception as e:
    print(f"IMPORT_ERROR: {e}")
for f in found:
    print(f"FOUND: {f}")
'@

$locResult = & $pyExe -c $locScript 2>&1
$locResult | ForEach-Object { Write-Host "  $_" }
Write-Host ""

# Check if active torch is CPU-only
$activeLine = $locResult | Where-Object { $_ -match "^ACTIVE:" } | Select-Object -First 1
$activeCuda = $locResult | Where-Object { $_ -match "^ACTIVE_CUDA:" } | Select-Object -First 1
$isCpuOnly  = $activeCuda -match "ACTIVE_CUDA: None"

if ($isCpuOnly) {
    WRN "CPU-only torch is active and may be shadowing a system CUDA install."

    # Find user site-packages torch
    $userSiteScript = @'
import site, os
for p in site.getusersitepackages() if isinstance(site.getusersitepackages(), list) else [site.getusersitepackages()]:
    t = os.path.join(p, "torch")
    if os.path.isdir(t):
        print(f"USER_TORCH: {t}")
'@
    $userTorch = & $pyExe -c $userSiteScript 2>&1 | Where-Object { $_ -match "USER_TORCH:" }
    if ($userTorch) {
        WRN "Found CPU-only torch in user site-packages -- this shadows the system CUDA install."
        INF "Removing user-space torch/torchvision..."
        # Uninstall from user space only
        & $pyExe -m pip uninstall -y torch torchvision 2>&1 | ForEach-Object { Write-Host "  $_" }
        Write-Host ""

        # Re-check: maybe system CUDA torch is now visible
        $recheckScript = @'
try:
    import importlib, sys
    # Force reimport
    for m in list(sys.modules.keys()):
        if m.startswith("torch"):
            del sys.modules[m]
    import torch
    import torch.cuda
    print(f"VERSION: {torch.__version__}")
    print(f"CUDA_AVAIL: {torch.cuda.is_available()}")
except Exception as e:
    print(f"ERROR: {e}")
'@
        $recheck = & $pyExe -c $recheckScript 2>&1
        $recheck | ForEach-Object { Write-Host "  $_" }
        Write-Host ""

        if ($recheck -match "CUDA_AVAIL: True") {
            OK "System CUDA torch is now active -- GPU ready!"
            OK "Restart go2rtc to apply."
            Read-Host "Press Enter to exit"
            exit 0
        } else {
            INF "System torch is not CUDA or not present. Will install CUDA torch now."
        }
    }
}

# ── 3. Detect CUDA version from nvidia-smi ───────────────────────────────────
INF "Detecting GPU / CUDA version via nvidia-smi..."
$cudaIndex = $null
try {
    $nv = & nvidia-smi 2>&1 | Select-String "CUDA Version"
    if ($nv -and $nv.Line -match "CUDA Version:\s*(\d+)\.(\d+)") {
        $major = [int]$Matches[1]; $minor = [int]$Matches[2]
        INF "nvidia-smi: CUDA $major.$minor  (driver supports up to this version)"
        if     ($major -gt 12 -or ($major -eq 12 -and $minor -ge 8)) { $cudaIndex = "cu128" }
        elseif ($major -eq 12 -and $minor -ge 4)                      { $cudaIndex = "cu124" }
        elseif ($major -eq 12 -and $minor -ge 1)                      { $cudaIndex = "cu121" }
        elseif ($major -eq 11 -and $minor -ge 8)                      { $cudaIndex = "cu118" }
        else { WRN "CUDA $major.$minor < 11.8 -- limited support."; $cudaIndex = "cu118" }
    }
} catch { WRN "nvidia-smi failed: $_" }

if (-not $cudaIndex) {
    WRN "Could not auto-detect CUDA version."
    Write-Host ""
    Write-Host "  Select PyTorch CUDA build:"
    Write-Host "    1) cu128  (CUDA 12.8+, driver 570+)  <-- RTX A2000 / 4xxx series"
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
INF "Installing torch+$cudaIndex from $indexUrl ..."
Write-Host ""

# Uninstall any remaining torch before clean install
& $pyExe -m pip uninstall -y torch torchvision 2>&1 | ForEach-Object { Write-Host "  $_" }
Write-Host ""

& $pyExe -m pip install torch torchvision --index-url $indexUrl
if ($LASTEXITCODE -ne 0) {
    ERR "pip install failed (exit $LASTEXITCODE)."
    Read-Host "Press Enter"; exit 1
}
Write-Host ""

# ── 4. Final verification ────────────────────────────────────────────────────
INF "Verifying..."
$verScript = @'
import torch
print(f"PyTorch       : {torch.__version__}")
print(f"CUDA build    : {torch.version.cuda or 'None'}")
print(f"CUDA available: {torch.cuda.is_available()}")
print(f"GPU count     : {torch.cuda.device_count()}")
if torch.cuda.is_available():
    for i in range(torch.cuda.device_count()):
        p = torch.cuda.get_device_properties(i)
        print(f"  GPU {i}: {p.name}  {p.total_memory//1024**2} MiB")
'@

$result = & $pyExe -c $verScript 2>&1
$result | ForEach-Object { Write-Host "  $_" }
Write-Host ""

if ($result -match "CUDA available: True") {
    OK "GPU is ready! Restart go2rtc to apply."
} else {
    WRN "CUDA still not available. Active torch location:"
    & $pyExe -c "import torch; print(torch.__file__)" 2>&1 | ForEach-Object { WRN "  $_" }
    WRN "Make sure go2rtc runs yolo_counter with this same Python."
}

Read-Host "Press Enter to exit"
