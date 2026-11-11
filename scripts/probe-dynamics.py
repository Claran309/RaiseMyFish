"""实测 space_dynamics 412：不同 header 组合"""
import json
import urllib.request

CRED = r'C:\Users\Claran\.bili-ai\credentials.json'
c = json.load(open(CRED, encoding='utf-8'))
cookie = f"SESSDATA={c['sessdata']}; bili_jct={c['bili_jct']}; buvid3={c['buvid3']}"
mid = '3747574135655903'

base = f'https://api.bilibili.com/x/polymer/web-dynamic/v1/feed/space?host_mid={mid}&offset=&features=itemOpusStyle&platform=web&timezone_offset=-480'

cases = [
    ('Chrome UA + space referer', {'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36', 'Referer': 'https://space.bilibili.com/'}),
    ('Chrome UA + 无 referer', {'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36'}),
    ('Chrome UA + space + 无 platform', {'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36', 'Referer': 'https://space.bilibili.com/'}),
]
for name, headers in cases:
    h = {'Cookie': cookie, **headers}
    req = urllib.request.Request(base, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            body = resp.read().decode('utf-8', 'replace')
        try:
            d = json.loads(body)
            print(f'{name}: HTTP 200 code={d.get("code")} msg={str(d.get("message"))[:30]}')
        except Exception:
            print(f'{name}: HTTP 200 非JSON {body[:60]}')
    except urllib.error.HTTPError as e:
        print(f'{name}: HTTP {e.code}')
    except Exception as e:
        print(f'{name}: ERR {e}')
