#!/usr/bin/env bash
# 0.11 冒烟启动脚本：从 credentials.json 构造 cookie（不落盘），启动 fishd
set -u
cd "$(dirname "$0")/.."

COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
if [ -z "$COOKIE" ]; then
  echo "cookie 构造失败" >&2
  exit 1
fi
echo "cookie 已构造（长度 ${#COOKIE}）"

FISH_BILI_COOKIE="$COOKIE" go run ./cmd/fishd --env-file .env
