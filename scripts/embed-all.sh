#!/usr/bin/env bash
# 跑记忆与观众档案的向量化（增量）
set -u
cd "$(dirname "$0")/.."
export FISH_EMBEDDING_KEY="08b483c8b87e49809f6304e28a12ab72.raOTIf27OMT1zyFd"
COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
export FISH_BILI_COOKIE="$COOKIE"
echo "=== memory embed ==="
go run ./cmd/fishctl --env-file .env memory embed 2>&1 | tail -3
echo "=== viewer embed ==="
go run ./cmd/fishctl --env-file .env viewer embed 2>&1 | tail -3
