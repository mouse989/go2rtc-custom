@ECHO OFF
REM Build yolo_counter.exe for Windows on this machine.
REM Run on the target Windows PC (not via CI).
REM Requires Python 3.11 and pip in PATH.
REM
REM Usage:
REM   Double-click or run from cmd:  scripts\build_yolo_win.bat
REM
REM For NVIDIA GPU support use:      scripts\build_yolo_win_gpu.bat
REM The binary will be at:           dist\yolo_counter.exe

ECHO === Building yolo_counter.exe for Windows (CPU-only) ===
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

ECHO --- Installing PyTorch (CPU) ---
pip install torch --index-url https://download.pytorch.org/whl/cpu
IF ERRORLEVEL 1 GOTO error

ECHO --- Installing ultralytics, opencv, web stack, PyInstaller ---
pip install ultralytics opencv-python-headless fastapi "uvicorn[standard]" pyinstaller
IF ERRORLEVEL 1 GOTO error

ECHO --- Building binary ---
python -m PyInstaller --onefile --name yolo_counter "%SCRIPT%"
IF ERRORLEVEL 1 GOTO error

ECHO.
ECHO === Done! Binary: dist\yolo_counter.exe (CPU-only) ===
GOTO end

:error
ECHO.
ECHO === BUILD FAILED - check errors above ===
EXIT /B 1

:end
