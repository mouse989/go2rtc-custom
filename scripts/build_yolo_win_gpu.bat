@ECHO OFF
REM Build yolo_counter.exe for Windows with NVIDIA GPU (CUDA 12.1) support.
REM Run on a Windows machine that has an NVIDIA GPU.
REM Requires Python 3.11 and pip in PATH.
REM
REM Usage:
REM   Double-click or run from cmd:  scripts\build_yolo_win_gpu.bat
REM
REM For CPU-only use:                scripts\build_yolo_win.bat
REM The binary will be at:           dist\yolo_counter.exe

ECHO === Building yolo_counter.exe for Windows (NVIDIA GPU / CUDA 12.1) ===
ECHO.

REM Resolve repo root from script location (script lives in scripts\)
SET "REPO=%~dp0.."
SET "SCRIPT=%REPO%\yolo_counter\counter.py"

REM Verify counter.py exists before proceeding
IF NOT EXIST "%SCRIPT%" (
    ECHO ERROR: Cannot find yolo_counter\counter.py under %REPO%
    ECHO Make sure you are running this script from inside the repo.
    EXIT /B 1
)

ECHO --- Installing PyTorch (CUDA 12.1) ---
pip install torch --index-url https://download.pytorch.org/whl/cu121
IF ERRORLEVEL 1 GOTO error

ECHO --- Installing ultralytics, opencv, web stack, PyInstaller ---
pip install ultralytics opencv-python-headless fastapi "uvicorn[standard]" pyinstaller
IF ERRORLEVEL 1 GOTO error

ECHO --- Installing NVIDIA CUDA runtime libraries (needed to bundle CUDA DLLs into .exe) ---
pip install nvidia-cuda-runtime-cu12 nvidia-cublas-cu12 nvidia-cuda-nvrtc-cu12 nvidia-cufft-cu12 nvidia-curand-cu12 nvidia-cusolver-cu12 nvidia-cusparse-cu12
IF ERRORLEVEL 1 GOTO error

ECHO --- Building binary ---
python -m PyInstaller --onedir --collect-all torch --runtime-hook "%REPO%\yolo_counter\pyi_rth_torch_cuda.py" --name yolo_counter "%SCRIPT%"
IF ERRORLEVEL 1 GOTO error

ECHO --- Copying torch DLLs to root (Windows searches EXE directory first) ---
REM Python 3.8+ restricts DLL search to application dir + System32 + AddDllDirectory entries.
REM The EXE lives at dist\yolo_counter\yolo_counter.exe so its application dir is dist\yolo_counter\.
REM Placing torch DLLs there guarantees Windows finds them when c10.dll loads its dependencies.
xcopy /Y "dist\yolo_counter\_internal\torch\lib\*.dll" "dist\yolo_counter\"
IF ERRORLEVEL 1 GOTO error

ECHO.
ECHO === Done! Folder: dist\yolo_counter\ (NVIDIA GPU / CUDA 12.1) ===
ECHO.
ECHO DEPLOY: xcopy /E /Y dist\yolo_counter\* D:\GO_YO\
ECHO         The torch DLLs are at the root (next to yolo_counter.exe) — required for GPU.
GOTO end

:error
ECHO.
ECHO === BUILD FAILED - check errors above ===
EXIT /B 1

:end
