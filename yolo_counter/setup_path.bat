@echo off
:: ─────────────────────────────────────────────────────────────────────────────
:: setup_path.bat  —  Thêm Python Scripts vào PATH (User), không cần Admin.
:: Chạy một lần sau khi cài pip packages.
:: ─────────────────────────────────────────────────────────────────────────────
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0setup_path.ps1"
if %ERRORLEVEL% NEQ 0 (
    echo.
    echo [!!] setup_path.ps1 gap loi. Xem thong bao phia tren.
    pause
    exit /b 1
)
pause
