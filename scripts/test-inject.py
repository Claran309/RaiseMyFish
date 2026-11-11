"""测试 Web 注入：写一条注入（confirm=yes）"""
import json
import urllib.request

BASE = 'http://127.0.0.1:8090'

# 拿 token
with urllib.request.urlopen(BASE + '/api/token', timeout=5) as r:
    token = json.load(r).get('token', '')
print('token:', token[:10] + '...')

body = json.dumps({'content': '测试注入：本鱼今晚要安静，多听少说', 'ttl_seconds': 3600}, ensure_ascii=False).encode('utf-8')
req = urllib.request.Request(
    BASE + '/api/inject?confirm=yes',
    data=body,
    method='POST',
    headers={'Content-Type': 'application/json', 'X-Panel-Token': token},
)
try:
    with urllib.request.urlopen(req, timeout=10) as r:
        print('写入:', r.read().decode('utf-8')[:300])
except urllib.error.HTTPError as e:
    print('HTTP', e.code, e.read().decode('utf-8', 'replace')[:300])

# 列表
with urllib.request.urlopen(BASE + '/api/inject', timeout=5) as r:
    d = json.load(r)
print('列表 count:', d.get('count'), '| items:', json.dumps(d.get('items', [])[:2], ensure_ascii=False)[:300])
