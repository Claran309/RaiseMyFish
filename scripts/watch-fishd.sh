#!/usr/bin/env bash
# 监控 fishd：检测卡死（日志长时间不更新）并抓 goroutine dump
# 用法：bash scripts/watch-fishd.sh（后台跑，日志写 state/watchdog-monitor.log）
set -u
cd "$(dirname "$0")/.."
LOG="${1:-tmp-test6.log}"
OUT="state/watchdog-monitor.log"
mkdir -p state
echo "=== monitor start $(date '+%H:%M:%S') ===" >> "$OUT"

prev_mtime=0
stuck_count=0
while true; do
  sleep 30
  if ! tasklist 2>/dev/null | grep -qi "fishd.exe"; then
    echo "[$(date '+%H:%M:%S')] fishd 不在运行" >> "$OUT"
    continue
  fi
  mtime=$(stat -c %Y "$LOG" 2>/dev/null || echo 0)
  now=$(date +%s)
  age=$((now - mtime))
  if [ "$age" -gt 300 ]; then
    stuck_count=$((stuck_count + 1))
    echo "[$(date '+%H:%M:%S')] 日志 ${age}s 未更新（可能卡死），抓 dump" >> "$OUT"
    curl -s -m 10 "http://127.0.0.1:8090/debug/pprof/goroutine?debug=2" -o "state/stuck-dump-$(date '+%H%M%S').txt" 2>/dev/null
    echo "[$(date '+%H:%M:%S')] dump 已保存（第 ${stuck_count} 次）" >> "$OUT"
  fi
done
