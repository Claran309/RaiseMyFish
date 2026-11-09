#!/usr/bin/env bash
# 跑 fishd 迁移（应用 0004/0005/0006 等）
set -u
cd "$(dirname "$0")/.."
# embedding key 由调用方环境或 .env 提供，脚本不写死（F01）
COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
if [ -z "$COOKIE" ]; then
  echo "cookie 构造失败" >&2
  exit 1
fi
export FISH_BILI_COOKIE="$COOKIE"
go run ./cmd/fishd --env-file .env migrate up
