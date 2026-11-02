import json
import subprocess

url = "https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig?platform=pc&room_id=30308220"
out = subprocess.run(["curl", "-s", "-m", "10", url], capture_output=True, encoding="utf-8").stdout
data = json.loads(out)
lines = []
for g in data["data"]["list"]:
    lines.append(f"{g['id']}\t{g['name']}\t{g['price']}\t{g.get('coin_type')}\t{g.get('bag_gift')}")
open("gifts3.txt", "w", encoding="utf-8").write("\n".join(lines))

# 找含"花"的名字
hits = [l for l in lines if "花" in l]
open("gifts3_hits.txt", "w", encoding="utf-8").write("\n".join(hits) if hits else "(no 花)")
