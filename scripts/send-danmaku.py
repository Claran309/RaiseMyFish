"""发一条直播弹幕（娇娇直播间）：本鱼改了，不公式化鲸~"""
import json
import time
import urllib.parse
import urllib.request

CRED = r'C:\Users\Claran\.bili-ai\credentials.json'
c = json.load(open(CRED, encoding='utf-8'))
uid = c.get('uid') or '3493085031959205'
cookie = f"SESSDATA={c['sessdata']}; bili_jct={c['bili_jct']}; buvid3={c['buvid3']}"
room = '1992099976'
text = '本鱼改了，不公式化鲸~'

form = {
    'bubble': '0',
    'msg': text,
    'color': '16777215',
    'mode': '1',
    'fontsize': '25',
    'rnd': str(int(time.time())),
    'roomid': room,
    'csrf': c['bili_jct'],
    'csrf_token': c['bili_jct'],
}
data = urllib.parse.urlencode(form).encode()
req = urllib.request.Request(
    'https://api.live.bilibili.com/msg/send',
    data=data,
    method='POST',
    headers={
        'Cookie': cookie,
        'Referer': f'https://live.bilibili.com/{room}',
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36',
        'Content-Type': 'application/x-www-form-urlencoded',
    },
)
try:
    with urllib.request.urlopen(req, timeout=15) as resp:
        body = resp.read().decode('utf-8')
    print('HTTP', resp.status, body[:300])
    d = json.loads(body)
    print('code:', d.get('code'), 'msg:', d.get('message'), '| 发送:', '成功' if d.get('code') == 0 else '失败')
except Exception as e:
    print('ERR:', e)
