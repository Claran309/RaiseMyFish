# 初始化 staging 库：校验独立库/扩展 → 迁移 → 灌 1k 观众 / 1k 会话 / 1 万事件。
# 需要先由 PostgreSQL 超级用户创建 raisemyfish_bench 并安装 vector 扩展。
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
. (Join-Path $PSScriptRoot "bench-env.ps1")

$psql = "C:\Program Files\PostgreSQL\17\bin\psql.exe"
if (-not (Test-Path $psql)) { throw "找不到 psql.exe" }

$db = & $psql $env:FISH_BENCH_DSN -tAc "SELECT current_database()" 2>&1
if ($LASTEXITCODE -ne 0) {
  Write-Host @"
无法连接 raisemyfish_bench。请先用 PostgreSQL 超级用户执行：
  psql -U postgres -d postgres -c "CREATE DATABASE raisemyfish_bench OWNER fish;"
  psql -U postgres -d raisemyfish_bench -c "CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS pg_trgm;"
原始错误：$db
"@
  exit 1
}
if ($db.Trim() -ne "raisemyfish_bench") { throw "连接到了错误数据库：$db" }
$ext = & $psql $env:FISH_BENCH_DSN -tAc "SELECT extname FROM pg_extension WHERE extname='vector'"
if ($ext.Trim() -ne "vector") {
  throw "raisemyfish_bench 缺少 vector 扩展；需要超级用户执行 CREATE EXTENSION vector"
}

$benchEnv = "bench/out/bench.env"
New-Item -ItemType Directory -Force -Path "bench/out" | Out-Null
"FISH_BENCH_DSN=$($env:FISH_BENCH_DSN)`nFISH_MOCK_LLM_KEY=mock" | Set-Content -Encoding ascii $benchEnv
go run ./cmd/fishd --config config/fishd.bench.toml --env-file $benchEnv migrate up
if ($LASTEXITCODE -ne 0) { throw "迁移失败" }
go run ./bench/seed --config config/fishd.bench.toml --env-file $benchEnv --reset --viewers 1000 --conversations 1000 --events 10000
if ($LASTEXITCODE -ne 0) { throw "灌种子失败" }

Write-Host "bench 初始化完成。"
