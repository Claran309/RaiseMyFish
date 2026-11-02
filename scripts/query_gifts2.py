import json
import subprocess

url = "https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/roomGiftConfig?platform=pc&room_id=30308220"
out = subprocess.run(["curl", "-s", "-m", "10", url], capture_output=True, encoding="utf-8").stdout
data = json.loads(out)
lines = []
for key in ("global_gift", "room_gift"):
    block = data["data"].get(key) or {}
    for g in block.get("list", []):
        lines.append(f"{key}\t{g['id']}\t{g['name']}\t{g['price']}\t{g.get('coin_type')}\t{g.get('bag_gift')}")
open("gifts2.txt", "w", encoding="utf-8").write("\n".join(lines))
