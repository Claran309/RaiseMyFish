#!/usr/bin/env bash
# 检查 .env 是否包含 embedding key（只输出键名与长度，不输出值）
set -u
cd "$(dirname "$0")/.."
if [ ! -f .env ]; then
  echo ".env 不存在"
  exit 1
fi
echo "=== .env 键名 ==="
grep -oE '^[A-Z_]+' .env | sort -u
echo "=== embedding key 检查 ==="
if grep -q '^FISH_EMBEDDING_KEY=' .env 2>/dev/null; then
  len=$(grep '^FISH_EMBEDDING_KEY=' .env | head -1 | cut -d= -f2- | tr -d '"' | wc -c)
  echo "FISH_EMBEDDING_KEY 存在（长度 ${len}）"
else
  echo "FISH_EMBEDDING_KEY 不存在（需要添加）"
fi
