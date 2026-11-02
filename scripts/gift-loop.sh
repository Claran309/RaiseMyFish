#!/usr/bin/env bash
# 娇娇直播间送礼循环：每 15 分钟送 1 朵小花花（31036，1 电池）
set -u
cd "$(dirname "$0")/.."

COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
if [ -z "$COOKIE" ]; then
  echo "cookie 构造失败" >&2
  exit 1
fi

while true; do
  echo "[$(date '+%H:%M:%S')] 送小花花..."
  FISH_BILI_COOKIE="$COOKIE" go run ./cmd/fishctl gift send --room 1992099976 2>&1 | tail -2
  sleep 900
done
