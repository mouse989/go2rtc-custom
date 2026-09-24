@ECHO OFF
REM Build the standalone HTTPS reverse proxy (cmd\https-proxy) for Windows.
REM Output: dist\https-proxy.exe
CD /D "%~dp0.."
IF NOT EXIST dist MKDIR dist
SET GOOS=windows
SET GOARCH=amd64
SET CGO_ENABLED=0
go build -ldflags "-s -w" -trimpath -o dist\https-proxy.exe .\cmd\https-proxy
IF ERRORLEVEL 1 (
  ECHO Build FAILED
  EXIT /B 1
)
ECHO Built dist\https-proxy.exe
