#!/usr/bin/env bash
# 娇娇直播间测试：启动 fishd（采集+回复+看直播）
set -u
cd "$(dirname "$0")/.."

COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
if [ -z "$COOKIE" ]; then
  echo "cookie 构造失败" >&2
  exit 1
fi
echo "cookie 已构造（长度 ${#COOKIE}）"

FISH_BILI_COOKIE="$COOKIE" go run ./cmd/fishd --env-file .env
