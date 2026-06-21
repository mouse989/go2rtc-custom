@ECHO OFF
REM Setup yolo_counter GPU for Windows using a Python venv (no PyInstaller).
REM Use this when the PyInstaller GPU build fails with WinError 1114 on
REM modern NVIDIA drivers (596+, CUDA 13.x).
REM
REM Usage (run from the repo root):
REM   scripts\setup_yolo_win_gpu.bat <deploy_dir>
REM
REM   deploy_dir: folder where go2rtc.exe lives  (default: current directory)
REM
REM Requires Python 3.11+ in PATH.
REM
REM For CPU-only or older-driver GPU: use scripts\build_yolo_win.bat instead
REM (produces a single self-contained yolo_counter.exe).

ECHO === Setting up yolo_counter GPU venv ===
ECHO.

REM --- Resolve repo root and deploy dir ---
SET "REPO=%~dp0.."
SET "DEPLOY=%~1"
IF "%DEPLOY%"=="" (
    SET "DEPLOY=%CD%"
    ECHO [info] No deploy dir given, using current dir: %CD%
)

REM Verify go2rtc.exe is present in the deploy dir (sanity check)
IF NOT EXIST "%DEPLOY%\go2rtc.exe" (
    ECHO WARNING: go2rtc.exe not found in %DEPLOY%
    ECHO Make sure you are pointing at the right deployment folder.
)

IF NOT EXIST "%REPO%\yolo_counter\counter.py" (
    ECHO ERROR: Cannot find yolo_counter\counter.py under %REPO%
    ECHO Run this script from inside the repository.
    EXIT /B 1
)

ECHO --- Creating Python venv in %DEPLOY%\yolo_venv ---
python -m venv "%DEPLOY%\yolo_venv"
IF ERRORLEVEL 1 GOTO error

REM Remove conflicting NVIDIA pip packages (may linger from earlier build attempts).
REM The torch cu126 wheel bundles its own matching CUDA 12.6 DLLs; standalone
REM nvidia-*-cu12 packages install mismatched versions that shadow torch's libs.
ECHO --- Removing any conflicting standalone NVIDIA CUDA pip packages ---
"%DEPLOY%\yolo_venv\Scripts\pip" uninstall -y ^
    nvidia-cuda-runtime-cu12 nvidia-cublas-cu12 nvidia-cuda-nvrtc-cu12 ^
    nvidia-cufft-cu12 nvidia-curand-cu12 nvidia-cusolver-cu12 ^
    nvidia-cusparse-cu12 nvidia-cudnn-cu12 nvidia-cuda-cupti-cu12 ^
    nvidia-nvtx-cu12 nvidia-nvjitlink-cu12 2>NUL

ECHO --- Installing PyTorch (CUDA 12.6, compatible with driver 560+ / CUDA 13.x) ---
"%DEPLOY%\yolo_venv\Scripts\pip" install torch --index-url https://download.pytorch.org/whl/cu126
IF ERRORLEVEL 1 GOTO error

ECHO --- Installing ultralytics, opencv, web stack ---
"%DEPLOY%\yolo_venv\Scripts\pip" install ultralytics opencv-python-headless fastapi "uvicorn[standard]"
IF ERRORLEVEL 1 GOTO error

ECHO --- Verifying torch sees the GPU ---
"%DEPLOY%\yolo_venv\Scripts\python" -c ^
    "import torch; print('torch', torch.__version__, '| cuda', torch.version.cuda, '| GPU available:', torch.cuda.is_available())"
IF ERRORLEVEL 1 (
    ECHO.
    ECHO ERROR: torch import failed inside the venv.
    ECHO Run the line above manually to see the real error.
    GOTO error
)

ECHO --- Copying counter.py and yolo_counter.bat to %DEPLOY% ---
copy /Y "%REPO%\yolo_counter\counter.py" "%DEPLOY%\counter.py"
IF ERRORLEVEL 1 GOTO error
copy /Y "%REPO%\yolo_counter\yolo_counter.bat" "%DEPLOY%\yolo_counter.bat"
IF ERRORLEVEL 1 GOTO error

ECHO.
ECHO === Done! ===
ECHO.
ECHO Files deployed to %DEPLOY%\:
ECHO   yolo_counter.bat   ^<-- go2rtc will auto-discover this
ECHO   counter.py
ECHO   yolo_venv\         ^<-- Python venv with torch cu126 + ultralytics
ECHO.
ECHO IMPORTANT: if yolo_counter.exe already exists in %DEPLOY%\, rename or delete
ECHO it -- go2rtc prefers .exe over .bat and would launch the old bundle instead.
ECHO.
ECHO   ren "%DEPLOY%\yolo_counter.exe" yolo_counter.exe.bak
ECHO.
GOTO end

:error
ECHO.
ECHO === SETUP FAILED — check errors above ===
EXIT /B 1

:end
