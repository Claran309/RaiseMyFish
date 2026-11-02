#!/usr/bin/env bash
# 调试真实送礼：直接 node gift.mjs --go 看完整错误
set -u
cd "$(dirname "$0")/.."

COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
FISH_BILI_COOKIE="$COOKIE" node tools/bili-whale/gift.mjs --room 1992099976 --gift-id 31036 --num 1 --go 2>&1 | head -6
