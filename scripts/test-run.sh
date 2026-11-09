#!/usr/bin/env bash
# 正式测试启动：直播间 30308220（主人直播间），带 cookie + embedding key
# 凭据只从环境变量或 .env 读取，脚本内不存任何字面值（F01）。
set -u
cd "$(dirname "$0")/.."
COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
if [ -z "$COOKIE" ]; then
  echo "cookie 构造失败" >&2
  exit 1
fi
export FISH_BILI_COOKIE="$COOKIE"
# embedding key 由调用方环境或 fishd 自身 .env 提供，脚本不写死。
exec go run ./cmd/fishd --env-file "${FISH_ENV_FILE:-.env}"
