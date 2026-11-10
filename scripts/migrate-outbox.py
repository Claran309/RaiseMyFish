"""raw 数据迁移：
1. danmaku-archive/outbox-*.log（392 个，鱼发言归档）→ events（out）
   格式：2026/10/28 02:06:26\t房间1992099976\t正文\t(回复 昵称)
2. danmaku-outbox.log（今日）→ 同上
3. danmaku-corpus.jsonl → 参考（跳过，与 danmaku-log 重复）
"""
import glob
import hashlib
import os
import re

ROOT = r'C:\Users\Claran\.dsh\bili-live-export\ALL\raw'
OUT = 'migrate_outbox.sql'

def sql_str(s):
    return "'" + str(s).replace("'", "''") + "'"

lines = ['BEGIN;']
n = 0
files = glob.glob(os.path.join(ROOT, 'danmaku-archive', '*.log')) + [os.path.join(ROOT, 'danmaku-outbox.log')]

for f in files:
    if not os.path.exists(f):
        continue
    for line in open(f, encoding='utf-8'):
        line = line.rstrip('\n')
        if not line.strip():
            continue
        parts = line.split('\t')
        if len(parts) < 3:
            continue
        ts_raw = parts[0].strip()  # 2026/10/28 02:06:26
        room_raw = parts[1].strip()  # 房间1992099976
        text = parts[2].strip()
        reply = parts[3].strip() if len(parts) > 3 else ''
        if not text:
            continue
        m = re.match(r'(\d{4})/(\d{2})/(\d{2}) (\d{2}:\d{2}:\d{2})', ts_raw)
        if not m:
            continue
        at = f"{m.group(1)}-{m.group(2)}-{m.group(3)}T{m.group(4)}+08:00"
        room = re.sub(r'[^0-9]', '', room_raw)
        if not room:
            continue
        h = hashlib.sha1((at + room + text + reply).encode('utf-8')).hexdigest()[:20]
        eid = 'dsh:outbox:' + h
        import json as _json
        meta = _json.dumps({"import": "dsh_outbox", "reply": reply}, ensure_ascii=False)
        lines.append(
            f"INSERT INTO events (id, channel, dir, session, from_id, from_name, text, at, status, stage, meta) "
            f"VALUES ({sql_str(eid)}, 'live', 'out', {sql_str(room)}, '', '大肥鱼', {sql_str(text)}, {sql_str(at)}, "
            f"'done', 'verify', {sql_str(meta)}::jsonb) "
            f"ON CONFLICT (id) DO NOTHING;"
        )
        n += 1

lines.append('COMMIT;')
open(OUT, 'w', encoding='utf-8').write('\n'.join(lines))
print(f"outbox: {n} 条 → {OUT}")
