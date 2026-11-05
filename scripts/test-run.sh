#!/usr/bin/env bash
# 正式测试启动：直播间 30308220（主人直播间），带 cookie + embedding key
set -u
cd "$(dirname "$0")/.."
COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
if [ -z "$COOKIE" ]; then
  echo "cookie 构造失败" >&2
  exit 1
fi
export FISH_BILI_COOKIE="$COOKIE"
export FISH_EMBEDDING_KEY="08b483c8b87e49809f6304e28a12ab72.raOTIf27OMT1zyFd"
exec go run ./cmd/fishd --env-file .env
