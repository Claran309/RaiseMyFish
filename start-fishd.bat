@echo off
REM ============================================
REM fishd 一键启动（大肥鱼）——当前项目
REM 双击 start-fishd.bat 即可
REM ============================================
cd /d %~dp0

REM 1. 构造 B站 cookie（从本机凭据文件）
for /f "delims=" %%i in ('python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])"') do set FISH_BILI_COOKIE=%%i

if "%FISH_BILI_COOKIE%"=="" (
  echo [错误] cookie 构造失败——检查 C:\Users\Claran\.bili-ai\credentials.json
  pause
  exit /b 1
)

REM 2. 启动 fishd（优先用编译好的 exe；没有则 go run）
if exist "fishd.exe" (
  echo 启动 fishd.exe...
  fishd.exe --env-file .env
) else (
  echo 启动 go run（编译中...）...
  go run ./cmd/fishd --env-file .env
)
pause
