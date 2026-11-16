@echo off
REM fishd one-click start (go run version - works around exe write restriction)
cd /d %~dp0

REM 1. Build B-site cookie via python helper
for /f "delims=" %%i in ('python scripts\cookie.py') do set FISH_BILI_COOKIE=%%i

if "%FISH_BILI_COOKIE%"=="" (
  echo [ERROR] cookie build failed - check C:\Users\Claran\.bili-ai\credentials.json
  pause
  exit /b 1
)

REM 2. Ensure local ASR service on 127.0.0.1:8077 (voice sensing depends on it)
powershell -NoProfile -Command "try { $c = New-Object Net.Sockets.TcpClient; $c.Connect('127.0.0.1',8077); $c.Close(); exit 0 } catch { exit 1 }" >nul 2>&1
if %errorlevel%==0 goto asr_ready
set "ASR_START=%USERPROFILE%\.dsh\skills\asr-server\start-asr.bat"
if exist "%ASR_START%" (
  echo Starting local ASR service ...
  start "asr" /min cmd /c ""%ASR_START%""
) else (
  echo [WARN] ASR start script not found: %ASR_START%
  goto asr_ready
)
set /a asrtries=0
:asrwait
set /a asrtries+=1
powershell -NoProfile -Command "try { $c = New-Object Net.Sockets.TcpClient; $c.Connect('127.0.0.1',8077); $c.Close(); exit 0 } catch { exit 1 }" >nul 2>&1
if %errorlevel%==0 goto asr_ready
if %asrtries% geq 30 goto asrwarn
timeout /t 2 /nobreak >nul
goto asrwait
:asrwarn
echo [WARN] ASR port 8077 not ready after 60s - voice sensing will degrade
:asr_ready

REM 3. Start fishd via go run in its own window
echo Starting fishd ^(go run^) in new window ...
start "fishd" cmd /c "go run ./cmd/fishd --env-file .env"

REM 4. Wait for panel port (up to 90s for go compile), then open browser
set /a tries=0
:waitloop
set /a tries+=1
powershell -NoProfile -Command "try { $c = New-Object Net.Sockets.TcpClient; $c.Connect('127.0.0.1',8090); $c.Close(); exit 0 } catch { exit 1 }" >nul 2>&1
if %errorlevel%==0 goto openpanel
if %tries% geq 45 goto timeout
timeout /t 2 /nobreak >nul
goto waitloop

:openpanel
start http://127.0.0.1:8090
echo.
echo fishd is running. Panel: http://127.0.0.1:8090
echo Close the "fishd" window or use the panel close button to stop.
echo.
pause
exit /b 0

:timeout
echo [WARN] panel port not ready after 90s - check the "fishd" window for errors
pause
exit /b 1
