@echo off
title go2rtc-custom
echo Starting go2rtc-custom...
echo Open http://localhost:1984 in your browser
echo Login: admin / admin
echo.
echo Press Ctrl+C to stop
echo.
"%~dp0dist\go2rtc-custom.exe" -c "%~dp0go2rtc.yaml"
pause
