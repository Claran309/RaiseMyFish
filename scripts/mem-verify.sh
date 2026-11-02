#!/usr/bin/env bash
# 记忆提炼验证：只关 [channels.live]，保留 [memory]/[llm]，跑 fishd 看提炼结果
set -u
cd "$(dirname "$0")/.."

python - <<'EOF'
p = 'config/fishd.toml'
s = open(p, encoding='utf-8').read()
s = s.replace('[channels.live]\nenabled = true', '[channels.live]\nenabled = false')
open('tmp-fishd-mem.toml', 'w', encoding='utf-8').write(s)
EOF

go build -o bin/fishd.exe ./cmd/fishd || { echo "build failed"; exit 1; }

./bin/fishd.exe --config tmp-fishd-mem.toml --env-file .env > tmp-mem-verify.log 2>&1 &
PID=$!
sleep 18
kill $PID 2>/dev/null

echo "=== stdout 关键行 ==="
grep -E "memory_extract|记忆提炼|fishd已停止|error" tmp-mem-verify.log 2>/dev/null | head -8
rm -f tmp-fishd-mem.toml tmp-mem-verify.log
