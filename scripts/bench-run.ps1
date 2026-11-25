# 单场景压测编排：mockllm + fishd + fishload + 周期 snapshot。
#
# 用法示例：
#   .\scripts\bench-run.ps1 -Scenario R1 -Rate 24 -DurationSeconds 300
#   .\scripts\bench-run.ps1 -Scenario R3 -Rate 600 -DurationSeconds 120
#   .\scripts\bench-run.ps1 -Scenario R7 -Rate 120 -DurationSeconds 180
#
# 原始数据写 bench/raw/<run-id>/；不进入 git。
param(
  [ValidateSet("R1","R2","R3","R4","R5","R6","R7","R8")]
  [string]$Scenario = "R1",
  [int]$Rate = 24,
  [int]$DurationSeconds = 300,
  [int]$WarmupSeconds = 60,
  [ValidateSet("fixed","burst")] [string]$Mode = "fixed",
  [int]$BurstCount = 100,
  [int]$BurstWindowSeconds = 10,
  [int]$Workers = 1,
  [int]$SessionShards = 1,
  [int]$Events = 0,
  [string]$MockLatency = "150ms",
  [int]$DrainSeconds = 120,
  [switch]$SkipBuild
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
. (Join-Path $PSScriptRoot "bench-env.ps1")

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$runId = "$Scenario-$stamp"
$outDir = "bench/out/$runId"
$rawDir = "bench/raw/$runId"
New-Item -ItemType Directory -Force -Path $outDir, $rawDir, "bench/out/bin", "bench/out/logs", "bench/out/state" | Out-Null
$benchEnv = "bench/out/bench.env"
"FISH_BENCH_DSN=$($env:FISH_BENCH_DSN)`nFISH_MOCK_LLM_KEY=mock" | Set-Content -Encoding ascii $benchEnv

if (-not $SkipBuild) {
  go build -o bench/out/bin/fishd.exe ./cmd/fishd
  go build -o bench/out/bin/fishwatchdog.exe ./cmd/fishwatchdog
  go build -o bench/out/bin/fishctl.exe ./cmd/fishctl
  go build -o bench/out/bin/mockllm.exe ./cmd/mockllm
  go build -o bench/out/bin/fishload.exe ./cmd/fishload
}

$psql = "C:\Program Files\PostgreSQL\17\bin\psql.exe"
function Psql-Scalar([string]$sql) {
  $value = & $psql $env:FISH_BENCH_DSN -tAc $sql
  if ($LASTEXITCODE -ne 0) { throw "psql 失败：$sql" }
  return ($value -join "").Trim()
}
function Wait-Http([string]$url, [int]$seconds) {
  $deadline = (Get-Date).AddSeconds($seconds)
  while ((Get-Date) -lt $deadline) {
    try { Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 2 | Out-Null; return $true } catch { Start-Sleep -Milliseconds 500 }
  }
  return $false
}
function Snapshot([string]$path) {
  & bench/out/bin/fishctl.exe --config config/fishd.bench.toml --env-file "" bench snapshot --metrics-url http://127.0.0.1:19091/metrics |
    Tee-Object -FilePath $path -Append | Out-Null
}
function Stop-Tree($proc) {
  if ($null -eq $proc) { return }
  try { if (-not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue } } catch {}
}

$mock = $null
$fishd = $null
$watchdog = $null
$loads = @()
$killedAt = $null
$recoveredAt = $null
try {
  if (-not (Test-Path $psql)) { throw "找不到 psql.exe" }
  if ((Psql-Scalar "SELECT current_database()") -ne "raisemyfish_bench") { throw "当前不是 bench 库" }
  & $psql $env:FISH_BENCH_DSN -f bench/sql/10_bench_counts.sql | Set-Content (Join-Path $rawDir "counts-before.txt")

  # 事件样本
  $fixture = Join-Path $rawDir "events.jsonl"
  $total = if ($Events -gt 0) { $Events } else { [Math]::Max(2000, [int]($Rate * ($WarmupSeconds + $DurationSeconds) / 60) + 500) }
  $fixtureArgs = @("--generate", [string]$total)
  if ($Scenario -eq "R3") { $fixtureArgs += "--same-user" }
  $fixtureArgs += @("--spacing", "1s", "--session-shards", [string]$SessionShards, "--out", $fixture)
  go run ./bench/fixtures @fixtureArgs
  if ($LASTEXITCODE -ne 0) { throw "生成样本失败" }

  # mock LLM
  $mockLog = Join-Path $outDir "mockllm.log"
  $mock = Start-Process -FilePath "bench/out/bin/mockllm.exe" -ArgumentList @("-addr","127.0.0.1:18081","-latency",$MockLatency) -PassThru -WindowStyle Hidden -RedirectStandardOutput $mockLog -RedirectStandardError "$mockLog.err"
  if (-not (Wait-Http "http://127.0.0.1:18081/stats" 20)) { throw "mockllm 未就绪" }

  # fishd（watchdog 场景由 watchdog 拉起，避免同一端口两个进程）
  $fishLog = Join-Path $outDir "fishd.log"
  if ($Scenario -eq "R7") {
    $fishd = Start-Process -FilePath "bench/out/bin/fishd.exe" -ArgumentList @("--config","config/fishd.bench.toml","--env-file",$benchEnv) -PassThru -WindowStyle Hidden -RedirectStandardOutput $fishLog -RedirectStandardError "$fishLog.err"
    Start-Sleep -Seconds 2
    $watchdog = Start-Process -FilePath "bench/out/bin/fishwatchdog.exe" -ArgumentList @("--config","config/fishd.bench.toml","--env-file",$benchEnv) -PassThru -WindowStyle Hidden -RedirectStandardOutput "$outDir/watchdog.log" -RedirectStandardError "$outDir/watchdog.log.err"
  } else {
    $fishd = Start-Process -FilePath "bench/out/bin/fishd.exe" -ArgumentList @("--config","config/fishd.bench.toml","--env-file",$benchEnv) -PassThru -WindowStyle Hidden -RedirectStandardOutput $fishLog -RedirectStandardError "$fishLog.err"
  }
  if (-not (Wait-Http "http://127.0.0.1:19091/metrics" 60)) { throw "fishd /metrics 未就绪" }

  if ($Scenario -eq "R6") {
    # 只测记忆提炼：已有 done 事件在 extraction_jobs 中排队，观察 backlog 下降。
    $deadline = (Get-Date).AddSeconds($DurationSeconds)
    while ((Get-Date) -lt $deadline) {
      $pending = Psql-Scalar "SELECT count(*) FROM extraction_jobs WHERE status IN ('pending','running')"
      $done = Psql-Scalar "SELECT count(*) FROM extraction_jobs WHERE status='done'"
      [pscustomobject]@{ at=(Get-Date).ToString("o"); pending=[int]$pending; done=[int]$done } |
        ConvertTo-Json -Compress | Tee-Object -FilePath (Join-Path $rawDir "extraction-backlog.jsonl") -Append | Out-Null
      Start-Sleep -Seconds 5
    }
    [pscustomobject]@{
      run_id=$runId; scenario=$Scenario; duration_seconds=$DurationSeconds;
      started_at=$deadline.AddSeconds(-$DurationSeconds).ToString("o"); commit=(git rev-parse --short HEAD)
    } | ConvertTo-Json -Compress | Set-Content (Join-Path $rawDir "meta.json")
    & $psql $env:FISH_BENCH_DSN -f bench/sql/10_bench_counts.sql | Set-Content (Join-Path $rawDir "counts.txt")
    & $psql $env:FISH_BENCH_DSN -f bench/sql/12_memory_backlog.sql | Set-Content (Join-Path $rawDir "memory-backlog.txt")
    return
  }

  if ($Workers -gt 1) {
    $lines = Get-Content $fixture
    $chunk = [Math]::Ceiling($lines.Count / $Workers)
    for ($i = 0; $i -lt $Workers; $i++) {
      $part = Join-Path $rawDir ("events-$i.jsonl")
      $startIndex = $i * $chunk
      $endIndex = [Math]::Min(($i + 1) * $chunk - 1, $lines.Count - 1)
      $lines[$startIndex..$endIndex] | Set-Content -Encoding utf8 $part
      $loads += Start-Process -FilePath "bench/out/bin/fishload.exe" -ArgumentList @("--config","config/fishd.bench.toml","--env-file",$benchEnv,"--file",$part,"--mode",$Mode,"--rate",[string][Math]::Max(1,[int]($Rate/$Workers)),"--burst-count",[string]$BurstCount,"--burst-window",([string]$BurstWindowSeconds+"s"),"--limit",[string][Math]::Max(1,[int]($total/$Workers)),"--rewrite-at","--record",(Join-Path $rawDir "inject-$i.jsonl")) -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $rawDir "load-$i.json") -RedirectStandardError (Join-Path $rawDir "load-$i.err")
    }
  } else {
    $loads += Start-Process -FilePath "bench/out/bin/fishload.exe" -ArgumentList @("--config","config/fishd.bench.toml","--env-file",$benchEnv,"--file",$fixture,"--mode",$Mode,"--rate",[string]$Rate,"--burst-count",[string]$BurstCount,"--burst-window",([string]$BurstWindowSeconds+"s"),"--limit",[string][Math]::Max(1,[int]($Rate*($WarmupSeconds+$DurationSeconds)/60)+10),"--rewrite-at","--record",(Join-Path $rawDir "inject.jsonl")) -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $rawDir "load.json") -RedirectStandardError (Join-Path $rawDir "load.err")
  }

  Start-Sleep -Seconds $WarmupSeconds
  $measureStart = Get-Date
  $snapPath = Join-Path $rawDir "snapshots.jsonl"
  $deadline = $measureStart.AddSeconds($DurationSeconds)
  $faulted = $false
  while ((Get-Date) -lt $deadline) {
    Snapshot $snapPath
    if ($Scenario -eq "R7" -and -not $faulted -and ((Get-Date) -gt $measureStart.AddSeconds(10))) {
      $killedAt = Get-Date
      Stop-Process -Id $fishd.Id -Force
      $faulted = $true
    }
    if ($Scenario -eq "R7" -and $faulted -and $null -eq $recoveredAt) {
      $hb = Get-Content "bench/out/state/fishd.heartbeat" -Raw -ErrorAction SilentlyContinue | ConvertFrom-Json
      if ($null -ne $hb -and $hb.pid -ne $fishd.Id) {
        if (Wait-Http "http://127.0.0.1:19091/metrics" 1) {
          $recoveredAt = Get-Date
          [pscustomobject]@{ killed_at=$killedAt.ToString("o"); recovered_at=$recoveredAt.ToString("o"); recovery_seconds=($recoveredAt-$killedAt).TotalSeconds; old_pid=$fishd.Id; new_pid=$hb.pid } |
            ConvertTo-Json -Compress | Set-Content (Join-Path $rawDir "recovery.json")
        }
      }
    }
    if ($Scenario -eq "R8" -and -not $faulted -and ((Get-Date) -gt $measureStart.AddSeconds(10))) {
      $faulted = $true
      $killedAt = Get-Date
      $endFault = (Get-Date).AddSeconds(30)
      while ((Get-Date) -lt $endFault) {
        & $psql $env:FISH_BENCH_DSN -tAc "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='raisemyfish_bench' AND pid<>pg_backend_pid()" | Out-Null
        Start-Sleep -Seconds 2
      }
      [pscustomobject]@{ injected_at=$killedAt; duration_seconds=30; kind="bench connections terminated (PG service untouched)" } | ConvertTo-Json -Compress | Set-Content (Join-Path $rawDir "db-fault.json")
    }
    Start-Sleep -Seconds 5
  }

  $unfinishedSQL = "SELECT count(*) FROM events WHERE dir='in' AND status IN ('pending','queued','retry','replying')"
  $pendingBeforeDrain = [int](Psql-Scalar $unfinishedSQL)
  $pendingAtMeasureEnd = $pendingBeforeDrain
  foreach ($p in $loads) { Stop-Tree $p }
  $loads = @()
  $drainStart = Get-Date
  $drainDeadline = $drainStart.AddSeconds($DrainSeconds)
  while ($pendingBeforeDrain -gt 0 -and (Get-Date) -lt $drainDeadline) {
    Start-Sleep -Seconds 5
    Snapshot $snapPath
    $pendingBeforeDrain = [int](Psql-Scalar $unfinishedSQL)
  }
  [pscustomobject]@{
    started_at=$drainStart.ToString("o"); pending_after_measure=$pendingAtMeasureEnd; pending_final=$pendingBeforeDrain;
    drain_seconds=((Get-Date)-$drainStart).TotalSeconds; completed=($pendingBeforeDrain -eq 0)
  } | ConvertTo-Json -Compress | Set-Content (Join-Path $rawDir "drain.json")

  [pscustomobject]@{
    run_id=$runId; scenario=$Scenario; rate=$Rate; mode=$Mode; warmup_seconds=$WarmupSeconds;
    duration_seconds=$DurationSeconds; workers=$Workers; mock_latency=$MockLatency;
    started_at=$measureStart.ToString("o"); commit=(git rev-parse --short HEAD);
    event_count=$total; recovery=$recoveredAt
  } | ConvertTo-Json -Compress | Set-Content (Join-Path $rawDir "meta.json")
  & $psql $env:FISH_BENCH_DSN -f bench/sql/10_bench_counts.sql | Set-Content (Join-Path $rawDir "counts.txt")
  & $psql $env:FISH_BENCH_DSN -f bench/sql/11_bench_latency.sql | Set-Content (Join-Path $rawDir "latency.txt")
  & $psql $env:FISH_BENCH_DSN -f bench/sql/13_duplicate_sends.sql | Set-Content (Join-Path $rawDir "duplicate-sends.txt")
  & $psql $env:FISH_BENCH_DSN -c "SELECT channel, count(*) AS logs, count(*) FILTER (WHERE ok) AS ok, count(*) FILTER (WHERE NOT ok) AS failed FROM send_log GROUP BY channel ORDER BY channel" | Set-Content (Join-Path $rawDir "send-summary.txt")
  [pscustomobject]@{
    events = [int](Psql-Scalar "SELECT count(*) FROM events")
    inbound = [int](Psql-Scalar "SELECT count(*) FROM events WHERE dir='in'")
    send_log = [int](Psql-Scalar "SELECT count(*) FROM send_log")
    send_ok = [int](Psql-Scalar "SELECT count(*) FROM send_log WHERE ok IS TRUE")
    send_failed = [int](Psql-Scalar "SELECT count(*) FROM send_log WHERE ok IS FALSE")
    llm_usage = [int](Psql-Scalar "SELECT count(*) FROM llm_usage")
    dead = [int](Psql-Scalar "SELECT count(*) FROM events WHERE status='dead'")
    skipped = [int](Psql-Scalar "SELECT count(*) FROM events WHERE status='skipped'")
    unfinished = [int](Psql-Scalar "SELECT count(*) FROM events WHERE dir='in' AND status IN ('pending','queued','retry','replying')")
    duplicate_sends = [int](Psql-Scalar "SELECT count(*) FROM (SELECT event_id FROM send_log WHERE ok IS TRUE GROUP BY event_id HAVING count(*)>1) d")
  } | ConvertTo-Json -Compress | Set-Content (Join-Path $rawDir "counts.json")
  $latSQL = @"
WITH first_ok AS (
  SELECT DISTINCT ON (l.event_id) e.channel, e.at AS event_at, l.at AS sent_at
    FROM send_log l JOIN events e ON e.id=l.event_id
   WHERE l.ok IS TRUE AND e.dir='in' AND l.at>=e.at
   ORDER BY l.event_id,l.at ASC
)
SELECT COALESCE(json_agg(row_to_json(t)),'[]') FROM (
  SELECT channel, count(*) AS samples,
         percentile_cont(0.50) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at-event_at)))::float8 AS p50_s,
         percentile_cont(0.90) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at-event_at)))::float8 AS p90_s,
         percentile_cont(0.99) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at-event_at)))::float8 AS p99_s,
         max(EXTRACT(EPOCH FROM (sent_at-event_at)))::float8 AS max_s
    FROM first_ok GROUP BY channel
) t
"@
  (& $psql $env:FISH_BENCH_DSN -tAc $latSQL).Trim() | Set-Content (Join-Path $rawDir "latency.json")
  try { Invoke-RestMethod -Uri "http://127.0.0.1:18081/stats" | ConvertTo-Json -Compress | Set-Content (Join-Path $rawDir "mock-stats.json") } catch {}
  Write-Host "完成：$rawDir"
}
finally {
  foreach ($p in $loads) { Stop-Tree $p }
  Stop-Tree $fishd
  Stop-Tree $watchdog
  Stop-Tree $mock
  # R7 的 watchdog 可能拉起新 fishd；只清理 bench 指标端口，避免碰生产进程。
  try {
    Get-NetTCPConnection -LocalPort 19091 -State Listen -ErrorAction SilentlyContinue |
      ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
  } catch {}
}
