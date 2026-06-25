@ECHO OFF
SETLOCAL ENABLEDELAYEDEXPANSION
REM ═══════════════════════════════════════════════════════════════════════════
REM  setup_yolo_win.bat  —  Universal GPU/CPU venv setup for yolo_counter
REM
REM  Auto-detects the installed NVIDIA driver / CUDA version and installs
REM  the matching PyTorch wheel.  No manual configuration needed.
REM
REM  Usage (run from the repository root):
REM    scripts\setup_yolo_win.bat <deploy_dir>
REM    Example:  scripts\setup_yolo_win.bat D:\GO_YO\
REM
REM  deploy_dir:  folder where go2rtc.exe lives  (default: current directory)
REM
REM  Requires: Python 3.11+ in PATH
REM
REM  ┌─────────────────────────────────────────────────────────────────────┐
REM  │  CUDA version shown in nvidia-smi → PyTorch build selected          │
REM  │                                                                     │
REM  │  CUDA 12.6+  (driver ≥ 561)  →  torch cu126  (best performance)   │
REM  │  CUDA 12.1+  (driver ≥ 530)  →  torch cu121                       │
REM  │  CUDA 11.8+  (driver ≥ 522)  →  torch cu118                       │
REM  │  CUDA 11.3+  (driver ≥ 456)  →  torch cu113 / torch 1.12.x        │
REM  │  CUDA < 11.3 (driver < 456)  →  GPU NOT SUPPORTED — see note       │
REM  │  No GPU / nvidia-smi absent  →  torch CPU-only                     │
REM  │                                                                     │
REM  │  Note on CUDA 10.x (e.g. driver 411.95):                           │
REM  │    Minimum for any GPU-capable torch is CUDA 11.3 (driver ≥ 456).  │
REM  │    Update your NVIDIA driver to ≥ 456.38 for GPU with torch cu113. │
REM  │    Update to ≥ 522.06 for best GPU support with torch cu118.       │
REM  └─────────────────────────────────────────────────────────────────────┘
REM ═══════════════════════════════════════════════════════════════════════════

ECHO.
ECHO === YOLO Counter — GPU/CPU venv setup ===
ECHO.

REM ── Resolve paths ────────────────────────────────────────────────────────
SET "REPO=%~dp0.."
SET "DEPLOY=%~1"
IF "%DEPLOY%"=="" (
    SET "DEPLOY=%CD%"
    ECHO [info] No deploy dir given, using current dir: %CD%
)

REM Strip trailing backslash so paths stay consistent
IF "%DEPLOY:~-1%"=="\" SET "DEPLOY=%DEPLOY:~0,-1%"

ECHO [info] Repository: %REPO%
ECHO [info] Deploy dir: %DEPLOY%
ECHO.

REM ── Sanity checks ────────────────────────────────────────────────────────
IF NOT EXIST "%REPO%\yolo_counter\counter.py" (
    ECHO ERROR: Cannot find yolo_counter\counter.py under %REPO%
    ECHO        Run this script from inside the repository root.
    EXIT /B 1
)

python --version >nul 2>nul
IF ERRORLEVEL 1 (
    ECHO ERROR: Python not found in PATH.
    ECHO        Install Python 3.11+ from https://www.python.org/downloads/
    ECHO        Make sure to tick "Add Python to PATH" during install.
    EXIT /B 1
)
FOR /F "tokens=*" %%V IN ('python --version 2^>^&1') DO ECHO [info] Using %%V

IF NOT EXIST "%DEPLOY%\go2rtc.exe" (
    ECHO [warn] go2rtc.exe not found in %DEPLOY%
    ECHO        Make sure you are pointing at the correct deployment folder.
    ECHO        Continuing anyway...
    ECHO.
)

REM ── Detect GPU / CUDA version using Python (robust, cross-locale) ────────
ECHO --- Detecting GPU and CUDA version ---

REM Write detection output to a temp file to avoid FOR /F pipe quoting issues.
REM Output format:  <torch_flavor>|<cuda_ver>|<driver_ver>|<python_ver>
REM torch_flavor:   cu126 / cu121 / cu118 / cu113 / cu113_nopy / old / cpu
REM cu113_nopy = CUDA 11.3-11.7 but Python>=3.11 (no cu113 wheel for that Python)
python -c "import subprocess,re,sys;r=subprocess.run(['nvidia-smi'],capture_output=True,text=True,timeout=10);o=r.stdout;cm=re.search(r'CUDA Version: (\d+)\.(\d+)',o);dm=re.search(r'Driver Version: (\S+)',o);drv=dm.group(1) if dm else 'unknown';pv=f'{sys.version_info.major}.{sys.version_info.minor}';py311=(sys.version_info.major,sys.version_info.minor)>=(3,11);(print(f'cpu|none|{drv}|{pv}') if not cm else (lambda mj,mn,cv:(print(f'cu126|{cv}|{drv}|{pv}') if mj>12 or(mj==12 and mn>=6) else print(f'cu121|{cv}|{drv}|{pv}') if mj==12 else print(f'cu118|{cv}|{drv}|{pv}') if mj==11 and mn>=8 else print(f'cu113_nopy|{cv}|{drv}|{pv}') if mj==11 and mn>=3 and py311 else print(f'cu113|{cv}|{drv}|{pv}') if mj==11 and mn>=3 else print(f'old|{cv}|{drv}|{pv}')))(int(cm.group(1)),int(cm.group(2)),cm.group(1)+'.'+cm.group(2)))" > "%TEMP%\yolo_cuda_detect.tmp" 2>nul

IF NOT EXIST "%TEMP%\yolo_cuda_detect.tmp" (
    ECHO [warn] nvidia-smi detection failed. Defaulting to CPU-only mode.
    SET "TORCH_FLAVOR=cpu"
    SET "CUDA_VER=none"
    SET "DRIVER_VER=unknown"
    SET "PYTHON_VER=unknown"
    GOTO :pick_url
)

FOR /F "tokens=1,2,3,4 delims=|" %%A IN (%TEMP%\yolo_cuda_detect.tmp) DO (
    SET "TORCH_FLAVOR=%%A"
    SET "CUDA_VER=%%B"
    SET "DRIVER_VER=%%C"
    SET "PYTHON_VER=%%D"
)
DEL "%TEMP%\yolo_cuda_detect.tmp" 2>nul

IF NOT DEFINED TORCH_FLAVOR (
    ECHO [warn] Could not parse detection output. Defaulting to CPU-only mode.
    SET "TORCH_FLAVOR=cpu"
    SET "CUDA_VER=none"
    SET "DRIVER_VER=unknown"
)

ECHO [info] Driver version : %DRIVER_VER%
ECHO [info] CUDA version   : %CUDA_VER%
ECHO [info] Torch flavor   : %TORCH_FLAVOR%
ECHO.

REM ── Handle CUDA 11.3-11.7 — use torch cu113 (torch 1.12.x, older but GPU-capable) ──
IF "%TORCH_FLAVOR%"=="cu113" (
    ECHO ================================================================
    ECHO  CUDA %CUDA_VER% detected ^(driver %DRIVER_VER%^)
    ECHO  Using PyTorch cu113 ^(torch 1.12.x -- CUDA 11.3 build^)
    ECHO ================================================================
    ECHO.
    ECHO  torch cu113 bundles CUDA 11.3 runtime which is compatible with
    ECHO  your driver's CUDA %CUDA_VER% support.  GPU will be enabled.
    ECHO.
    ECHO  Note: torch 1.12 is older than the torch 2.x used with cu118/cu126.
    ECHO  Inference and counting work fine.  Training may be slightly slower.
    ECHO.
    ECHO  For best GPU support, update driver to ^>= 522.06 ^(CUDA 11.8^)
    ECHO  then re-run this script -- it will auto-select torch cu118.
    ECHO.
)

REM ── Handle CUDA 11.3-11.7 + Python 3.11+ — no compatible GPU wheel ──────────
REM torch cu113 (torch 1.12.x) only ships wheels for Python <=3.10.
REM torch 2.x requires CUDA >=11.7 (driver >=515).  Neither works here.
IF NOT "%TORCH_FLAVOR%"=="cu113_nopy" GOTO :after_cu113nopy_warn
ECHO ================================================================
ECHO  GPU NOT SUPPORTED — CUDA %CUDA_VER% + Python %PYTHON_VER%
ECHO ================================================================
ECHO.
ECHO  Your GPU supports CUDA %CUDA_VER% ^(driver %DRIVER_VER%^), which maps
ECHO  to PyTorch cu113 ^(torch 1.12.x^).  However, torch cu113 only ships
ECHO  wheels for Python ^<= 3.10 — you are running Python %PYTHON_VER%.
ECHO.
ECHO  torch 2.x ^(which supports Python 3.11+^) requires CUDA ^>= 11.7
ECHO  ^(driver ^>= 515^).  Your current driver does not reach that.
ECHO.
ECHO  To enable GPU acceleration, choose one of:
ECHO.
ECHO    Option A ^(recommended^) — Update NVIDIA driver:
ECHO      Minimum: driver ^>= 522.06  ^(enables CUDA 11.8 / torch cu118^)
ECHO      Better : driver ^>= 561.09  ^(enables CUDA 12.6 / torch cu126^)
ECHO      Download: https://www.nvidia.com/Download/index.aspx
ECHO      Then re-run this script — it will auto-select the right build.
ECHO.
ECHO    Option B — Install Python 3.10 alongside Python 3.11:
ECHO      Download Python 3.10 from https://www.python.org/downloads/
ECHO      Then re-run using:  py -3.10 scripts\setup_yolo_win.bat %DEPLOY%\
ECHO      torch cu113 ^(torch 1.12.x^) will be installed with GPU support.
ECHO.
ECHO ================================================================
ECHO.
CHOICE /C YN /M "Continue with CPU-only mode (slower, no GPU)?"
IF ERRORLEVEL 2 (
    ECHO.
    ECHO Aborted. Update driver ^>= 522.06 ^(CUDA 11.8^) or install Python 3.10
    ECHO and re-run this script to get GPU acceleration.
    EXIT /B 0
)
ECHO.
SET "TORCH_FLAVOR=cpu"
SET "CUDA_VER=none"
:after_cu113nopy_warn

REM ── Handle truly unsupported CUDA (10.x or 11.0-11.2) ────────────────────
IF NOT "%TORCH_FLAVOR%"=="old" GOTO :after_old_warn
ECHO ================================================================
ECHO  GPU NOT SUPPORTED -- CUDA %CUDA_VER% / Driver %DRIVER_VER%
ECHO ================================================================
ECHO.
ECHO  The minimum CUDA for any GPU-capable PyTorch build is 11.3.
ECHO  Your driver only supports up to CUDA %CUDA_VER%.
ECHO.
ECHO  To enable GPU acceleration, update your NVIDIA driver:
ECHO    Minimum for CUDA 11.3:  driver ^>= 456.38  ^(torch cu113^)
ECHO    Minimum for CUDA 11.8:  driver ^>= 522.06  ^(torch cu118^)
ECHO    Minimum for CUDA 12.6:  driver ^>= 561.09  ^(torch cu126, best^)
ECHO    Download: https://www.nvidia.com/Download/index.aspx
ECHO.
ECHO  Note: if your GPU is Kepler ^(GTX 600/700 series^) the last
ECHO  supported driver is 391.35 ^(CUDA 9.1^), which cannot run
ECHO  YOLO11 on GPU at all.  Use CPU-only mode in that case.
ECHO.
ECHO ================================================================
ECHO.
CHOICE /C YN /M "Continue with CPU-only mode (slower, no GPU)?"
IF ERRORLEVEL 2 (
    ECHO.
    ECHO Aborted. Please update your NVIDIA driver and re-run this script.
    EXIT /B 0
)
ECHO.
SET "TORCH_FLAVOR=cpu"
SET "CUDA_VER=none"
:after_old_warn

REM ── Map flavor to PyTorch index URL ──────────────────────────────────────
:pick_url
IF "%TORCH_FLAVOR%"=="cu126" (
    SET "TORCH_URL=https://download.pytorch.org/whl/cu126"
    SET "TORCH_DESC=CUDA 12.6  (driver %DRIVER_VER%)"
) ELSE IF "%TORCH_FLAVOR%"=="cu121" (
    SET "TORCH_URL=https://download.pytorch.org/whl/cu121"
    SET "TORCH_DESC=CUDA 12.1  (driver %DRIVER_VER%)"
) ELSE IF "%TORCH_FLAVOR%"=="cu118" (
    SET "TORCH_URL=https://download.pytorch.org/whl/cu118"
    SET "TORCH_DESC=CUDA 11.8  (driver %DRIVER_VER%)"
) ELSE IF "%TORCH_FLAVOR%"=="cu113" (
    SET "TORCH_URL=https://download.pytorch.org/whl/cu113"
    SET "TORCH_DESC=CUDA 11.3 via cu113 / torch 1.12.x  (driver %DRIVER_VER%)"
) ELSE (
    SET "TORCH_URL=https://download.pytorch.org/whl/cpu"
    SET "TORCH_DESC=CPU-only  (no GPU acceleration)"
)

ECHO --- PyTorch selection: %TORCH_DESC% ---
ECHO.

REM ── Create Python venv ────────────────────────────────────────────────────
REM Always delete any existing venv so pip installs the correct torch CUDA
REM version from scratch.  Reusing an old venv keeps stale torch wheels
REM (e.g. cu126 from a previous run) and pip will not downgrade them.
ECHO --- Removing old yolo_venv (if any) ---
IF EXIST "%DEPLOY%\yolo_venv" (
    RMDIR /S /Q "%DEPLOY%\yolo_venv"
    IF ERRORLEVEL 1 (
        ECHO [warn] Could not remove old venv -- it may be in use.
        ECHO        Close any terminals using it and retry.
        GOTO error
    )
)
ECHO --- Creating Python venv in %DEPLOY%\yolo_venv ---
python -m venv "%DEPLOY%\yolo_venv"
IF ERRORLEVEL 1 GOTO error

REM ── Remove conflicting standalone NVIDIA pip packages ─────────────────────
REM torch wheels bundle their own matching CUDA DLLs.  Standalone nvidia-*-cu12
REM packages install a different version that shadows torch's bundled libs and
REM causes WinError 1114 / 1455 on import.
ECHO --- Removing conflicting standalone NVIDIA pip packages (if any) ---
"%DEPLOY%\yolo_venv\Scripts\pip" uninstall -y ^
    nvidia-cuda-runtime-cu12 nvidia-cublas-cu12 nvidia-cuda-nvrtc-cu12 ^
    nvidia-cufft-cu12 nvidia-curand-cu12 nvidia-cusolver-cu12 ^
    nvidia-cusparse-cu12 nvidia-cudnn-cu12 nvidia-cuda-cupti-cu12 ^
    nvidia-nvtx-cu12 nvidia-nvjitlink-cu12 2>NUL

REM ── Install PyTorch + torchvision ─────────────────────────────────────────
ECHO --- Installing PyTorch + torchvision [%TORCH_DESC%] ---
"%DEPLOY%\yolo_venv\Scripts\pip" install torch torchvision --index-url %TORCH_URL%
IF ERRORLEVEL 1 GOTO error

REM ── Pin NumPy < 2 for torch 1.12.x (cu113) ───────────────────────────────
REM torch 1.12.x was compiled against NumPy 1.x.  NumPy 2.x breaks it with
REM "Numpy is not available" / _ARRAY_API not found at ultralytics import time.
REM ultralytics itself pulls the latest NumPy (2.x) so we must downgrade here,
REM BEFORE installing ultralytics so pip's dependency resolver keeps numpy<2.
IF "%TORCH_FLAVOR%"=="cu113" (
    ECHO --- Pinning numpy^<2 for torch 1.12.x ^(cu113^) compatibility ---
    "%DEPLOY%\yolo_venv\Scripts\pip" install "numpy<2"
    IF ERRORLEVEL 1 GOTO error
)

REM ── Install ultralytics and web stack ─────────────────────────────────────
REM Do NOT install opencv-python-headless alongside ultralytics on Windows:
REM ultralytics pulls opencv-python; having both packages conflicts (cv2 not found).
ECHO --- Installing ultralytics, fastapi, uvicorn ---
IF "%TORCH_FLAVOR%"=="cu113" (
    REM Keep numpy pinned: pass numpy<2 alongside to prevent ultralytics from
    REM pulling numpy 2.x as a transitive dependency.
    "%DEPLOY%\yolo_venv\Scripts\pip" install "numpy<2" ultralytics fastapi "uvicorn[standard]"
) ELSE (
    "%DEPLOY%\yolo_venv\Scripts\pip" install ultralytics fastapi "uvicorn[standard]"
)
IF ERRORLEVEL 1 GOTO error

REM ── Verify cv2 ───────────────────────────────────────────────────────────
ECHO --- Verifying cv2 ---
"%DEPLOY%\yolo_venv\Scripts\python" -c "import cv2; print('cv2', cv2.__version__)"
IF ERRORLEVEL 1 (
    ECHO [warn] cv2 not found via ultralytics dependency, installing opencv-python...
    "%DEPLOY%\yolo_venv\Scripts\pip" install opencv-python
    IF ERRORLEVEL 1 GOTO error
)

REM ── Verify torch installation ─────────────────────────────────────────────
ECHO --- Verifying torch installation ---
IF "%TORCH_FLAVOR%"=="cpu" (
    "%DEPLOY%\yolo_venv\Scripts\python" -c ^
        "import cv2,torch,torchvision; print('OK | cv2', cv2.__version__, '| torch', torch.__version__, '| torchvision', torchvision.__version__, '| CUDA available:', torch.cuda.is_available())"
    IF ERRORLEVEL 1 GOTO error
) ELSE (
    "%DEPLOY%\yolo_venv\Scripts\python" -c ^
        "import cv2,torch,torchvision; t=torch.tensor([1.0],device='cuda'); print('OK | cv2', cv2.__version__, '| torch', torch.__version__, '| torchvision', torchvision.__version__, '| CUDA:', torch.version.cuda, '| GPU:', torch.cuda.is_available())"
    IF ERRORLEVEL 1 (
        ECHO.
        ECHO [warn] GPU verification failed — torch cannot reach CUDA.
        ECHO        YOLO will fall back to CPU mode at runtime.
        ECHO        To fix: ensure your NVIDIA driver is version ^>= 522.06
        ECHO.
    )
)

REM ── Copy runtime files to deploy dir ─────────────────────────────────────
ECHO --- Copying counter.py and yolo_counter.bat to %DEPLOY% ---
copy /Y "%REPO%\yolo_counter\counter.py" "%DEPLOY%\counter.py"
IF ERRORLEVEL 1 GOTO error
copy /Y "%REPO%\yolo_counter\yolo_counter.bat" "%DEPLOY%\yolo_counter.bat"
IF ERRORLEVEL 1 GOTO error

REM ── Done ──────────────────────────────────────────────────────────────────
ECHO.
ECHO ================================================================
ECHO  Setup complete!
ECHO ================================================================
ECHO.
ECHO  Files deployed to: %DEPLOY%\
ECHO    yolo_counter.bat     ^<-- go2rtc will auto-discover and launch this
ECHO    counter.py
ECHO    yolo_venv\           ^<-- Python venv (%TORCH_DESC%)
ECHO.
ECHO  PyTorch: %TORCH_DESC%
ECHO.
IF NOT "%TORCH_FLAVOR%"=="cpu" (
    ECHO  GPU acceleration: ENABLED
) ELSE (
    ECHO  GPU acceleration: DISABLED (CPU-only)
    ECHO  To enable GPU later: update NVIDIA driver ^>= 522.06 and re-run this script.
)
ECHO.
ECHO  IMPORTANT: if yolo_counter.exe already exists in the deploy folder,
ECHO  rename or delete it — go2rtc prefers .exe over .bat:
ECHO.
ECHO    ren "%DEPLOY%\yolo_counter.exe" yolo_counter.exe.bak
ECHO.
GOTO end

:error
ECHO.
ECHO ================================================================
ECHO  SETUP FAILED — check errors above
ECHO ================================================================
EXIT /B 1

:end
ENDLOCAL
