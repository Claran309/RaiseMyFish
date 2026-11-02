#!/usr/bin/env bash
# 带 cookie 跑 fishctl status（静默/日报检查用）
set -u
cd "$(dirname "$0")/.."

COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
if [ -z "$COOKIE" ]; then
  echo "cookie 构造失败" >&2
  exit 1
fi
FISH_BILI_COOKIE="$COOKIE" go run ./cmd/fishctl status
