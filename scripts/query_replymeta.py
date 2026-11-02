import subprocess, os, json

env = dict(os.environ, PGPASSWORD="fish_pass", PGCLIENTENCODING="UTF8")
sql = """
SELECT to_char(at,'HH24:MI:SS'), from_name, left(text,30), meta::text
FROM events
WHERE session='1992099976' AND dir='in'
  AND meta::text LIKE '%reply_to%'
ORDER BY at DESC LIMIT 10
"""
subprocess.run(
    ["C:/Program Files/PostgreSQL/17/bin/psql.exe", "-h", "127.0.0.1", "-U", "fish",
     "-d", "raisemyfish", "-t", "-A", "-F", "\t", "-o", "replymeta.txt", "-c", sql],
    env=env, capture_output=True,
)
data = open("replymeta.txt", "rb").read().decode("utf-8", "replace")
found = 0
for line in data.splitlines():
    parts = line.split("\t")
    if len(parts) < 4:
        continue
    found += 1
    try:
        meta = json.loads(parts[3])
    except Exception:
        meta = {}
    print(parts[0], "|", parts[1], "|", "reply_to_name=", meta.get("reply_to_name"), "| reply_to_id=", meta.get("reply_to_id"))
if found == 0:
    print("no reply meta events")
