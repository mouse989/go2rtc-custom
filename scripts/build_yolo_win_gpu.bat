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

REM Move to repo root (script is in scripts\ subfolder)
cd /d "%~dp0.."

ECHO --- Installing PyTorch (CUDA 12.1) ---
pip install torch --index-url https://download.pytorch.org/whl/cu121
IF ERRORLEVEL 1 GOTO error

ECHO --- Installing ultralytics, opencv, web stack, PyInstaller ---
pip install ultralytics opencv-python-headless fastapi "uvicorn[standard]" pyinstaller
IF ERRORLEVEL 1 GOTO error

ECHO --- Building binary ---
python -m PyInstaller --onefile --name yolo_counter yolo_counter\counter.py
IF ERRORLEVEL 1 GOTO error

ECHO.
ECHO === Done! Binary: dist\yolo_counter.exe (NVIDIA GPU / CUDA 12.1) ===
GOTO end

:error
ECHO.
ECHO === BUILD FAILED - check errors above ===
EXIT /B 1

:end
