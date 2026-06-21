@echo off
:: ─────────────────────────────────────────────────────────────────────────────
:: yolo_counter.bat  --  Python wrapper for counter.py
::
:: Place this file next to go2rtc.exe (same folder).
:: Also place counter.py and the models/ folder there, OR update COUNTER_PY.
::
:: go2rtc will prefer yolo_counter.exe if it exists. If you want to use this
:: batch file instead, remove or rename yolo_counter.exe.
:: ─────────────────────────────────────────────────────────────────────────────

:: Path to counter.py  (default: same folder as this .bat file)
set "COUNTER_PY=%~dp0counter.py"

:: Python executable.
:: Uses yolo_venv\ (created by setup_yolo_win_gpu.bat) if present.
:: Exits with a clear error if the venv is missing instead of silently
:: falling back to system Python (which lacks the required packages).
set "VENV_PYTHON=%~dp0yolo_venv\Scripts\python.exe"
if exist "%VENV_PYTHON%" (
    set "PYTHON=%VENV_PYTHON%"
) else (
    echo [yolo_counter] ERROR: yolo_venv not found at %~dp0yolo_venv\
    echo [yolo_counter] Run setup first from the repo root:
    echo [yolo_counter]   scripts\setup_yolo_win_gpu.bat %~dp0
    exit /b 1
)

:: ── Default args used when go2rtc launches this file ─────────────────────────
:: go2rtc passes its own args when it auto-launches this file, so these only
:: apply when you run the .bat manually without arguments.
set "DEFAULT_ARGS=--port 8765 --model models/trained_20260617_090601.pt --conf 0.35 --rtsp-base rtsp://localhost:8554"

:: ── Run ───────────────────────────────────────────────────────────────────────
if "%~1"=="" (
    :: No args from caller -- use defaults (manual run)
    "%PYTHON%" "%COUNTER_PY%" %DEFAULT_ARGS%
) else (
    :: Args provided by go2rtc -- pass them through unchanged
    "%PYTHON%" "%COUNTER_PY%" %*
)
