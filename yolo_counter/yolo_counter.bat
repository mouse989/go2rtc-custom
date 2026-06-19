@echo off
:: ─────────────────────────────────────────────────────────────────────────────
:: yolo_counter.bat  --  Python wrapper for counter.py
::
:: Place this file next to go2rtc.exe (same folder).
:: Also place counter.py and the models/ folder there, OR update COUNTER_PY
:: below to point to where counter.py lives.
::
:: go2rtc will prefer yolo_counter.exe if it exists. If you want to use this
:: batch file instead, remove or rename yolo_counter.exe.
:: ─────────────────────────────────────────────────────────────────────────────

:: Path to counter.py  (default: same folder as this .bat file)
set "COUNTER_PY=%~dp0counter.py"

:: Python executable to use.
:: Leave as "python" to use whatever is first on PATH.
:: Or set an absolute path, e.g.:
::   set "PYTHON=C:\Program Files\Python312\python.exe"
set "PYTHON=python"

:: ── Run ───────────────────────────────────────────────────────────────────────
"%PYTHON%" "%COUNTER_PY%" %*
