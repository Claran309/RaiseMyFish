#!/usr/bin/env bash
# 打 Windows 稳定包到 dist/，并生成 fishd-stable-<version>.zip。
# 用法：bash scripts/build-dist.sh 0.1.0
set -euo pipefail

VERSION="${1:-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="$ROOT/dist"

echo "[build-dist] 构建 fishd.exe（version=$VERSION）"
go build -C "$ROOT" -trimpath -ldflags "-X main.version=$VERSION" -o "$DIST/fishd.exe" ./cmd/fishd

mkdir -p "$DIST/config" "$DIST/personas" "$DIST/scripts"
cp "$ROOT/config/fishd.toml" "$DIST/config/fishd.toml"
cp "$ROOT/.env.example" "$DIST/.env.example"
cp "$ROOT/scripts/cookie.py" "$DIST/scripts/cookie.py"
cp "$ROOT/scripts/dist-start.bat" "$DIST/start.bat"
cp "$ROOT/scripts/dist-README.md" "$DIST/README.md"
cp -a "$ROOT/personas/." "$DIST/personas/"

# cmd.exe 对 CRLF 更稳；没有 unix2dos 时保持原样也能运行。
if command -v unix2dos >/dev/null 2>&1; then
  unix2dos -q "$DIST/start.bat"
fi

echo "[build-dist] 校验注入版本"
"$DIST/fishd.exe" version

ZIP="$ROOT/fishd-stable-$VERSION.zip"
rm -f "$ZIP"
python -c "import shutil,sys; shutil.make_archive(sys.argv[1][:-4], 'zip', root_dir=sys.argv[2])" "$ZIP" "$DIST"
echo "[build-dist] 已生成 $ZIP"
