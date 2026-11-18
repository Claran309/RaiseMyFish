#!/usr/bin/env bash
# 测试 fishd.exe 能不能起（模拟 bat 的启动）
set -u
cd "$(dirname "$0")/.."
COOKIE=$(python scripts/cookie.py 2>/dev/null)
if [ -z "$COOKIE" ]; then
  echo "COOKIE_EMPTY" >&2
  exit 1
fi
export FISH_BILI_COOKIE="$COOKIE"
exec ./fishd.exe --env-file .env
