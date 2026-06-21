@ECHO OFF
REM Build yolo_counter.exe for Windows with NVIDIA GPU (CUDA 12.6) support.
REM Run on a Windows machine that has an NVIDIA GPU.
REM Requires Python 3.11 and pip in PATH.
REM
REM Usage:
REM   Double-click or run from cmd:  scripts\build_yolo_win_gpu.bat
REM
REM For CPU-only use:                scripts\build_yolo_win.bat

ECHO === Building yolo_counter.exe for Windows (NVIDIA GPU / CUDA 12.6) ===
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

REM ---------------------------------------------------------------------------
REM IMPORTANT: the torch cu126 wheel is fully self-contained. Its torch\lib\
REM directory already ships cudart64_12.dll, cublas64_12.dll, cudnn*, etc.
REM Do NOT install the nvidia-*-cu12 pip packages here: they may install
REM mismatched CUDA libraries that shadow torch's own, causing WinError 1114.
REM ---------------------------------------------------------------------------

ECHO --- Removing any conflicting standalone NVIDIA CUDA pip packages ---
REM These may linger from earlier build attempts and shadow torch's own libs.
pip uninstall -y nvidia-cuda-runtime-cu12 nvidia-cublas-cu12 nvidia-cuda-nvrtc-cu12 nvidia-cufft-cu12 nvidia-curand-cu12 nvidia-cusolver-cu12 nvidia-cusparse-cu12 nvidia-cudnn-cu12 nvidia-cuda-cupti-cu12 nvidia-nvtx-cu12 nvidia-nvjitlink-cu12 1>NUL 2>NUL

ECHO --- Installing PyTorch + torchvision (CUDA 12.6, self-contained) ---
REM torchvision MUST come from the same index as torch to get the CUDA NMS kernel.
pip install torch torchvision --index-url https://download.pytorch.org/whl/cu126
IF ERRORLEVEL 1 GOTO error

ECHO --- Installing ultralytics (pulls opencv-python), web stack, PyInstaller ---
REM ultralytics depends on opencv-python; do not also install opencv-python-headless
REM as having both packages causes cv2 import failures on Windows.
pip install ultralytics fastapi "uvicorn[standard]" pyinstaller
IF ERRORLEVEL 1 GOTO error

ECHO --- Verifying torch imports and sees the GPU (before bundling) ---
python -c "import torch; print('torch', torch.__version__, 'cuda build', torch.version.cuda, 'available', torch.cuda.is_available())"
IF ERRORLEVEL 1 (
    ECHO.
    ECHO ERROR: 'import torch' failed in plain Python. The problem is NOT PyInstaller.
    ECHO Fix the torch install first - run the line above manually to see the real error.
    GOTO error
)

ECHO --- Cleaning stale build artifacts (old DLLs cause conflicts) ---
IF EXIST "dist\yolo_counter" rmdir /S /Q "dist\yolo_counter"
IF EXIST "build\yolo_counter" rmdir /S /Q "build\yolo_counter"

ECHO --- Building binary ---
python -m PyInstaller --onedir --collect-all torch --collect-all ultralytics --runtime-hook "%REPO%\yolo_counter\pyi_rth_torch_cuda.py" --name yolo_counter "%SCRIPT%"
IF ERRORLEVEL 1 GOTO error

ECHO.
ECHO === Done! Folder: dist\yolo_counter\ (NVIDIA GPU / CUDA 12.1) ===
ECHO.
ECHO DEPLOY: xcopy /E /Y dist\yolo_counter\* D:\GO_YO\
ECHO         (copy the whole folder including _internal\ - do not copy only the .exe)
GOTO end

:error
ECHO.
ECHO === BUILD FAILED - check errors above ===
EXIT /B 1

:end
