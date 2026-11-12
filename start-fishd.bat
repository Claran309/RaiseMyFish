@echo off
REM fishd one-click start (ASCII only, avoid codepage issues)
cd /d %~dp0

REM 1. Build B-site cookie via python helper
for /f "delims=" %%i in ('python scripts\cookie.py') do set FISH_BILI_COOKIE=%%i

if "%FISH_BILI_COOKIE%"=="" (
  echo [ERROR] cookie build failed - check C:\Users\Claran\.bili-ai\credentials.json
  pause
  exit /b 1
)

REM 2. Start fishd in its own window
if exist "fishd.exe" (
  echo Starting fishd.exe in new window ...
  start "fishd" fishd.exe --env-file .env
) else (
  echo Starting go run ^(compiling...^) in new window ...
  start "fishd" cmd /c "go run ./cmd/fishd --env-file .env"
)

REM 3. Wait for panel then open browser
timeout /t 6 /nobreak >nul
start http://127.0.0.1:8090

echo.
echo fishd started. Panel: http://127.0.0.1:8090
echo Close the "fishd" console window to stop it, or use the panel close button.
echo.
pause
