#!/usr/bin/env bash
set -u
cd "$(dirname "$0")/.."
COOKIE=$(python -c "import json;c=json.load(open(r'C:\Users\Claran\.bili-ai\credentials.json'));print('SESSDATA='+c['sessdata']+'; bili_jct='+c['bili_jct']+'; buvid3='+c['buvid3'])")
FISH_BILI_COOKIE="$COOKIE" node scripts/verify-gift-fix.mjs 2>&1 | head -10
rm -f tmp-gift-fixed.mjs
