#!/usr/bin/env bash
# 跑知识库全量 embedding（6834 条梗 → pgvector）
set -u
cd "$(dirname "$0")/.."
# embedding key 由调用方环境或 .env 提供，脚本不写死（F01）
# fishctl 的配置校验会检查真实送礼凭据：注入 cookie 构造（与 jiaojiao-test.sh 同源）
COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
if [ -z "$COOKIE" ]; then
  echo "cookie 构造失败" >&2
  exit 1
fi
export FISH_BILI_COOKIE="$COOKIE"
go run ./cmd/fishctl --env-file .env knowledge embed
