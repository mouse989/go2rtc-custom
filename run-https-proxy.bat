@echo off
title https-proxy
echo Starting https-proxy (HTTPS for go2rtc and other web sites)...
echo Config page: http://127.0.0.1:8090
echo First-run admin password: see https-proxy-initial-password.txt
echo.
echo To run it as a Windows service instead (auto start, auto restart),
echo open a console as Administrator and run:
echo   dist\https-proxy.exe -service install
echo   dist\https-proxy.exe -service start
echo.
"%~dp0dist\https-proxy.exe" -c "%~dp0https-proxy.json"
pause
