# RaiseMyFish · 开发期一键启停（Windows PowerShell）
# 用法：
#   .\scripts\dev.ps1 start   启动开发环境
#   .\scripts\dev.ps1 stop    停止
#   .\scripts\dev.ps1 status  查看状态
#
# 注意：本脚本为开发期辅助，依赖以下组件（实现后填实）：
#   - PostgreSQL 本地服务（默认 127.0.0.1:5432）
#   - fishd（go run ./cmd/fishd）
#   - 协议端（NapCat/LLBot，Phase 3 起）
# 当前 Phase 0 只需 PG + fishd。

param(
    [Parameter(Position = 0)]
    [ValidateSet("start", "stop", "status")]
    [string]$Action = "status"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$PidFile = Join-Path $Root ".dev-fishd.pid"

function Check-PG {
    try {
        $tcp = New-Object System.Net.Sockets.TcpClient
        $tcp.Connect("127.0.0.1", 5432)
        $tcp.Close()
        return $true
    } catch { return $false }
}

function Start-Dev {
    if (-not (Test-Path (Join-Path $Root ".env"))) {
        Write-Host "[!] 缺少 .env，请从 .env.example 复制并填写" -ForegroundColor Yellow
        exit 1
    }
    if (-not (Check-PG)) {
        Write-Host "[!] PostgreSQL 未运行（127.0.0.1:5432）" -ForegroundColor Red
        exit 1
    }
    Write-Host "[*] 启动 fishd..." -ForegroundColor Cyan
    $proc = Start-Process -FilePath "go" -ArgumentList "run", "./cmd/fishd" `
        -WorkingDirectory $Root -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $Root "logs/fishd.out.log") `
        -RedirectStandardError (Join-Path $Root "logs/fishd.err.log")
    $proc.Id | Out-File -Encoding ascii $PidFile
    Write-Host "[✓] fishd 已启动 (PID $($proc.Id))" -ForegroundColor Green
}

function Stop-Dev {
    if (Test-Path $PidFile) {
        $fishdPid = Get-Content $PidFile
        try { Stop-Process -Id $fishdPid -Force; Write-Host "[✓] 已停止 fishd ($fishdPid)" -ForegroundColor Green }
        catch { Write-Host "[!] 进程已不存在 ($fishdPid)" -ForegroundColor Yellow }
        Remove-Item $PidFile -Force
    } else {
        Write-Host "[*] 没有记录中的 fishd 进程" -ForegroundColor Yellow
    }
}

function Show-Status {
    Write-Host "=== RaiseMyFish 开发状态 ===" -ForegroundColor Cyan
    Write-Host ("PostgreSQL : " + $(if (Check-PG) { "运行中" } else { "未运行" }))
    if (Test-Path $PidFile) {
        $fishdPid = Get-Content $PidFile
        $alive = Get-Process -Id $fishdPid -ErrorAction SilentlyContinue
        Write-Host ("fishd      : " + $(if ($alive) { "运行中 (PID $fishdPid)" } else { "已退出（PID 文件残留）" }))
    } else {
        Write-Host "fishd      : 未启动"
    }
}

switch ($Action) {
    "start"  { Start-Dev }
    "stop"   { Stop-Dev }
    "status" { Show-Status }
}
