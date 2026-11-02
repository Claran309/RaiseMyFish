import json
import subprocess
import sys

url = "https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig?platform=pc&room_id=30308220"
out = subprocess.run(["curl", "-s", "-m", "10", url], capture_output=True, encoding="utf-8").stdout
data = json.loads(out)
lines = []
for g in data["data"]["list"]:
    if "花" in g["name"] or g["price"] <= 100:
        lines.append(f"{g['id']}\t{g['name']}\t{g['price']}\t{g.get('coin_type')}\t{g.get('bag_gift')}")
open("gifts.txt", "w", encoding="utf-8").write("\n".join(lines))
