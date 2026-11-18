@echo off
REM RaiseMyFish fishd one-click start (stable package, ASCII to avoid GBK mojibake)
setlocal
cd /d %~dp0

if not exist "fishd.exe" (
  echo [ERROR] fishd.exe not found in %CD%
  pause
  exit /b 1
)
if not exist ".env" (
  echo [WARN] .env not found - copy .env.example to .env and fill FISH_DB_DSN / FISH_LLM_KEY / FISH_EMBEDDING_KEY
)

REM 1. Build B-site cookie via optional helper; .env or DB active account also works.
if exist "scripts\cookie.py" (
  where python >nul 2>&1
  if %errorlevel%==0 (
    for /f "delims=" %%i in ('python scripts\cookie.py') do set FISH_BILI_COOKIE=%%i
  )
)

REM 2. Ensure local ASR service on 127.0.0.1:8077 (voice sensing depends on it).
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

REM 3. Print version (injected at build time).
echo fishd version:
fishd.exe version

REM 4. Start fishd in its own window so this console can open the panel.
echo Starting fishd ...
start "fishd" cmd /c "fishd.exe --env-file .env"

REM 5. Wait for the panel port (up to 90s), then open the browser.
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
