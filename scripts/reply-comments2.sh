#!/usr/bin/env bash
# 补跑失败的评论回复（B站限流：间隔至少 10 秒 → sleep 12）
set -u
BILI=~/.dsh/bili-cli/.venv/Scripts/bili.exe
OID=415114992
declare -A REPLIES=(
  [319823790033]="截图就截图吧，娇娇看了也行——本鱼说的话不收回"
  [319835035329]="摸摸就摸，饭也吃，碗底别抖"
  [319834710529]="黑历史就黑历史，鱼不认账，你剪吧"
  [323596915392]="谢啦伊蓮娜，新家还行，窝是软的"
  [319832954273]="能玩，看主人安排，鱼不挑地方"
  [323589990000]="谢啦，乔迁不发红包，发白饭"
)
for rpid in "${!REPLIES[@]}"; do
  text="${REPLIES[$rpid]}"
  out=$($BILI comment reply "$OID" "$rpid" "$text" --type draw 2>&1)
  echo "$rpid -> $(echo "$out" | head -c 100)"
  sleep 12
done
