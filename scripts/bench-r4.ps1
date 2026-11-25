# R4：mock LLM 直接并发基线（10/50），每档 3 次，原始 JSON 写 bench/raw。
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$out = "bench/raw/R4-$(Get-Date -Format yyyyMMdd-HHmmss)"
New-Item -ItemType Directory -Force -Path $out | Out-Null
if (-not (Test-Path bench/out/bin/mockllm.exe)) { go build -o bench/out/bin/mockllm.exe ./cmd/mockllm }
if (-not (Test-Path bench/out/bin/bench-mockload.exe)) { go build -o bench/out/bin/bench-mockload.exe ./bench/mockload }
$mock = Start-Process -FilePath "bench/out/bin/mockllm.exe" -ArgumentList @("-addr","127.0.0.1:18081","-latency","1s") -PassThru -WindowStyle Hidden -RedirectStandardOutput "$out/mockllm.log" -RedirectStandardError "$out/mockllm.err"
try {
  Start-Sleep -Seconds 1
  foreach ($c in @(10,50)) {
    foreach ($r in 1..3) {
      $json = & bench/out/bin/bench-mockload.exe -url http://127.0.0.1:18081 -concurrency $c -requests ($c*2)
      $json | Set-Content (Join-Path $out "c$c-r$r.json")
      Write-Host "R4 c=$c rep=$r $json"
    }
  }
} finally {
  Stop-Process -Id $mock.Id -Force -ErrorAction SilentlyContinue
}
