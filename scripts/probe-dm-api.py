"""探测 B站私信消息 API 路径（哪个不 404）"""
import json
import urllib.request
import urllib.parse

CRED = r'C:\Users\Claran\.bili-ai\credentials.json'
c = json.load(open(CRED, encoding='utf-8'))
cookie = f"SESSDATA={c['sessdata']}; bili_jct={c['bili_jct']}; buvid3={c['buvid3']}"
peer = '400842618'

candidates = [
    ('旧 get_session_msgs', f'https://api.vc.bilibili.com/session_svr/v1/session_svr/get_session_msgs?talker_id={peer}&session_type=1&size=20'),
    ('v2 get_session_msgs', f'https://api.vc.bilibili.com/session_svr/v2/session_svr/get_session_msgs?talker_id={peer}&session_type=1&size=20'),
    ('x/im session msg', f'https://api.vc.bilibili.com/x/im/session/msg?talker_id={peer}&session_type=1&size=20'),
    ('x/im session sync', f'https://api.vc.bilibili.com/x/im/session/sync?talker_id={peer}&session_type=1&size=20'),
    ('session_msgs (无 svr)', f'https://api.vc.bilibili.com/session_svr/v1/session_svr/session_msgs?talker_id={peer}&session_type=1&size=20'),
    ('web im msg', f'https://message.bilibili.com/api/session_svr/v1/session_svr/get_session_msgs?talker_id={peer}&session_type=1&size=20'),
]
for name, url in candidates:
    req = urllib.request.Request(url, headers={
        'Cookie': cookie,
        'Referer': 'https://message.bilibili.com/',
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36',
    })
    try:
        with urllib.request.urlopen(req, timeout=12) as resp:
            body = resp.read().decode('utf-8', 'replace')
            code = resp.status
        try:
            d = json.loads(body)
            print(f'{name}: HTTP {code} | code={d.get("code")} msg={d.get("message","")[:40]}')
        except Exception:
            print(f'{name}: HTTP {code} | 非JSON: {body[:60]}')
    except urllib.error.HTTPError as e:
        print(f'{name}: HTTP {e.code}')
    except Exception as e:
        print(f'{name}: ERR {e}')
