#!/usr/bin/env bash
# 直接跑 danmaku-await 20 秒，看能不能收到事件帧（验证 ws 通不通）
set -u
cd "$(dirname "$0")/.."
COOKIE=$(python scripts/cookie.py 2>/dev/null)
[ -z "$COOKIE" ] && { echo "no cookie"; exit 1; }
export FISH_BILI_COOKIE="$COOKIE"
timeout 22 node tools/bili-whale/danmaku-await.mjs --room 1992099976 \
  --self-id 3747574135655903 --http-timeout-ms 10000 --connect-timeout-ms 10000 \
  --heartbeat-ms 5000 --idle-ms 60000 2>&1 | head -12
