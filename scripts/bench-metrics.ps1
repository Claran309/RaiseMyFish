# 抓一次 fishd /metrics 原文，证明指标端点在真实运行中可用。
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
. (Join-Path $PSScriptRoot "bench-env.ps1")
. (Join-Path $PSScriptRoot "bench-pg.ps1") start | Out-Null
New-Item -ItemType Directory -Force -Path bench/out/bin,bench/raw | Out-Null
go build -o bench/out/bin/fishd.exe ./cmd/fishd
go build -o bench/out/bin/mockllm.exe ./cmd/mockllm
go build -o bench/out/bin/fishload.exe ./cmd/fishload
go run ./bench/fixtures --generate 100 --spacing 1s --out bench/out/metrics-events.jsonl | Out-Null
"FISH_BENCH_DSN=$($env:FISH_BENCH_DSN)`nFISH_MOCK_LLM_KEY=mock" | Set-Content -Encoding ascii bench/out/bench.env
$mock = Start-Process -FilePath "bench/out/bin/mockllm.exe" -ArgumentList @("-addr","127.0.0.1:18081","-latency","150ms") -PassThru -WindowStyle Hidden -RedirectStandardOutput bench/out/metrics-mock.log -RedirectStandardError bench/out/metrics-mock.err
$fish = Start-Process -FilePath "bench/out/bin/fishd.exe" -ArgumentList @("--config","config/fishd.bench.toml","--env-file","bench/out/bench.env") -PassThru -WindowStyle Hidden -RedirectStandardOutput bench/out/metrics-fishd.log -RedirectStandardError bench/out/metrics-fishd.err
try {
  $deadline = (Get-Date).AddSeconds(30)
  while ((Get-Date) -lt $deadline) {
    try { Invoke-WebRequest -UseBasicParsing http://127.0.0.1:19091/metrics -TimeoutSec 2 | Out-Null; break } catch { Start-Sleep -Milliseconds 500 }
  }
  & bench/out/bin/fishload.exe --config config/fishd.bench.toml --env-file bench/out/bench.env --file bench/out/metrics-events.jsonl --rate 120 --limit 50 --rewrite-at --record bench/raw/metrics-inject.jsonl | Out-Null
  Invoke-WebRequest -UseBasicParsing http://127.0.0.1:19091/metrics -TimeoutSec 5 | Select-Object -ExpandProperty Content | Set-Content bench/raw/prometheus-metrics.txt
  Write-Host "metrics 快照：bench/raw/prometheus-metrics.txt"
} finally {
  Stop-Process -Id $fish.Id -Force -ErrorAction SilentlyContinue
  Stop-Process -Id $mock.Id -Force -ErrorAction SilentlyContinue
  Get-NetTCPConnection -LocalPort 19091 -State Listen -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
}
