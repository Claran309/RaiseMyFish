# 载入生产 .env 中的连接串，派生独立的 bench 库 DSN。
# 只在当前 PowerShell 进程生效；不打印、不写回文件。
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".env"
$line = (Select-String -Path $envFile -Pattern '^FISH_DB_DSN=').Line
if (-not $line) { throw "缺少 FISH_DB_DSN" }
$dsn = $line -replace '^FISH_DB_DSN=', ''
if ($dsn -notmatch '^postgres(ql)?://') { throw "FISH_DB_DSN 必须是 postgres URL" }
$env:FISH_DB_DSN = $dsn
# staging 使用独立 PG 实例（55632），不写生产 5432；DSN 无密码，仅回环 trust。
# 端口避开 Windows/Hyper-V 的 excludedportrange（55343-55542）。
$env:FISH_BENCH_DSN = "postgres://postgres@127.0.0.1:55632/raisemyfish_bench?sslmode=disable"
$env:FISH_MOCK_LLM_KEY = "mock"
Write-Host "已设置 FISH_BENCH_DSN（127.0.0.1:55632/raisemyfish_bench）"
