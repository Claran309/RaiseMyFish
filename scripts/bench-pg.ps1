# 管理 staging 专用 PostgreSQL 17 实例（127.0.0.1:55632），不碰生产 5432。
param([ValidateSet("init","start","stop","status")][string]$Action = "status")
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$bin = "C:\Program Files\PostgreSQL\17\bin"
$data = "bench/out/pgdata"
$log = "bench/out/pgdata/postgres.log"
$env:FISH_BENCH_DSN = "postgres://postgres@127.0.0.1:55632/raisemyfish_bench?sslmode=disable"

function Ini {
  if (-not (Test-Path "$data/PG_VERSION")) {
    New-Item -ItemType Directory -Force -Path $data | Out-Null
    & "$bin/initdb.exe" -D $data -U postgres -A trust --encoding=UTF8 --locale=C
    if ($LASTEXITCODE -ne 0) { throw "initdb 失败" }
  }
}
function StartPg {
  if (-not (Test-Path "$data/postmaster.pid")) {
    & "$bin/pg_ctl.exe" -D $data -l $log -o '"-p" "55632" "-h" "127.0.0.1"' start
    if ($LASTEXITCODE -ne 0) { throw "pg_ctl start 失败" }
  }
  $psql = "$bin/psql.exe"
  $exists = & $psql -h 127.0.0.1 -p 55632 -U postgres -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='raisemyfish_bench'"
  if ($exists -ne "1") {
    & $psql -h 127.0.0.1 -p 55632 -U postgres -d postgres -c "CREATE DATABASE raisemyfish_bench"
  }
  & $psql -h 127.0.0.1 -p 55632 -U postgres -d raisemyfish_bench -c "CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS pg_trgm;" | Out-Null
  Write-Host "staging PG 就绪：$env:FISH_BENCH_DSN"
}
function StopPg {
  if (Test-Path "$data/postmaster.pid") {
    & "$bin/pg_ctl.exe" -D $data -m fast stop
  }
}

switch ($Action) {
  "init"   { Ini; StartPg }
  "start"  { Ini; StartPg }
  "stop"   { StopPg }
  "status" {
    if (Test-Path "$data/postmaster.pid") {
      & "$bin/pg_ctl.exe" -D $data status
    } else { Write-Host "staging PG 未运行" }
  }
}
