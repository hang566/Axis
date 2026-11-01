@echo off
rem AV Workbench launcher: build first if needed, then serve and open browser.
rem Keep it pure ASCII so cmd works with any code page.
cd /d "%~dp0"
if exist "stt-sub.exe" goto run
echo [1/2] stt-sub.exe not found, building with Go ...
go build -o stt-sub.exe .
if errorlevel 1 (
  echo Build failed. Install Go 1.21+ and run this script again.
  pause
  exit /b 1
)
:run
echo [2/2] Starting AV Workbench ...
stt-sub.exe serve -open
pause
