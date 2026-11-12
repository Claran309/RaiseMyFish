"""构造 B站 cookie（供 start-fishd.bat 调用，避免 bat 内嵌 Python 引号问题）"""
import json
import sys

CRED = r'C:\Users\Claran\.bili-ai\credentials.json'
try:
    c = json.load(open(CRED, encoding='utf-8'))
    print(f"SESSDATA={c['sessdata']}; bili_jct={c['bili_jct']}; buvid3={c['buvid3']}")
except Exception as e:
    print('', end='')
    sys.exit(1)
