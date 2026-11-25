# 最小 R8：只验证 fishd 在独立 staging PG 停机 30 秒时的退出/恢复行为。
# 用 postgres.exe 直接启动，避免 Windows 下 pg_ctl start 卡住。
param([int]$Reps = 2)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
. (Join-Path $PSScriptRoot "bench-env.ps1")
$bin = "C:\Program Files\PostgreSQL\17\bin"
$data = "bench/out/pgdata"
$pgLog = "bench/out/pgdata/postgres.log"
$psql = "$bin/psql.exe"

function PgReady {
  & $psql $env:FISH_BENCH_DSN -tAc "SELECT 1" 2>$null | Out-Null
  return $LASTEXITCODE -eq 0
}
function StartPg {
  if (PgReady) { return }
  if (Test-Path "$data/postmaster.pid") {
    $pidText = Get-Content "$data/postmaster.pid" | Select-Object -First 1
    if (-not (Get-Process -Id ([int]$pidText) -ErrorAction SilentlyContinue)) {
      [System.IO.File]::Delete((Resolve-Path "$data/postmaster.pid").Path)
    }
  }
  Start-Process -FilePath "$bin/postgres.exe" -ArgumentList @("-D",$data,"-p","55632","-h","127.0.0.1") -WindowStyle Hidden -RedirectStandardOutput $pgLog -RedirectStandardError "$pgLog.err" | Out-Null
  $deadline = (Get-Date).AddSeconds(45)
  while ((Get-Date) -lt $deadline) { if (PgReady) { return }; Start-Sleep -Milliseconds 500 }
  throw "staging PG 未就绪"
}
function StopPg {
  & "$bin/pg_ctl.exe" -D $data -m fast stop | Out-Null
  $deadline = (Get-Date).AddSeconds(30)
  while ((Get-Date) -lt $deadline) { if (-not (PgReady)) { return }; Start-Sleep -Milliseconds 500 }
  throw "staging PG 未停止"
}

StartPg
go build -o bench/out/bin/fishd.exe ./cmd/fishd
foreach ($rep in 1..$Reps) {
  $run = "R8min-$(Get-Date -Format yyyyMMdd-HHmmss)-r$rep"
  $dir = "bench/raw/$run"
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  $fish = Start-Process -FilePath "bench/out/bin/fishd.exe" -ArgumentList @("--config","config/fishd.bench.toml","--env-file","bench/out/bench.env") -PassThru -WindowStyle Hidden -RedirectStandardOutput "$dir/fishd.log" -RedirectStandardError "$dir/fishd.err"
  try {
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
      try { Invoke-WebRequest -UseBasicParsing http://127.0.0.1:19091/metrics -TimeoutSec 2 | Out-Null; break } catch { Start-Sleep -Milliseconds 500 }
    }
    $stoppedAt = Get-Date
    StopPg
    Start-Sleep -Seconds 30
    $restoredAt = Get-Date
    StartPg
    Start-Sleep -Seconds 5
    $fish.Refresh()
    $metrics = $false
    try { $metrics = (Invoke-WebRequest -UseBasicParsing http://127.0.0.1:19091/metrics -TimeoutSec 2).StatusCode -eq 200 } catch {}
    $tail = Get-Content "$dir/fishd.err" -ErrorAction SilentlyContinue | Select-Object -Last 5
    [pscustomobject]@{
      run=$run; pg_port=55632; stopped_at=$stoppedAt.ToString("o"); pg_restored_at=$restoredAt.ToString("o");
      fishd_exited=$fish.HasExited; metrics_after_restart=$metrics; fishd_tail=$tail;
      note="staging PG 55632 stopped 30s; production 5432 untouched"
    } | ConvertTo-Json -Compress | Set-Content "$dir/pg-stop.json"
  } finally {
    Stop-Process -Id $fish.Id -Force -ErrorAction SilentlyContinue
    Get-NetTCPConnection -LocalPort 19091 -State Listen -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
  }
  StartPg
}
