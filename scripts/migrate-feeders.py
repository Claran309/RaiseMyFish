"""feeders.json（喂饭金主名单）→ memory_records

13 人：喂过饭/礼物/电池 → 金主。全注入后，金主开口时记忆自动进 prompt。
每人一条 memory_records（kind=preference, subject=viewer, asserted_by=mid, from_id=mid）。
"""
import json
import os

SRC = r'C:\Users\Claran\.dsh\bili-live-export\ALL\feeders.json'
OUT = 'migrate_feeders.sql'

def sql_str(s):
    return "'" + str(s).replace("'", "''") + "'"

d = json.load(open(SRC, encoding='utf-8'))
people = d.get('people', [])
rule = d.get('rule', '喂过饭/礼物/电池 都算，金主')

lines = ['BEGIN;']
n = 0
for i, p in enumerate(people):
    name = str(p.get('name', '')).strip()
    mid = str(p.get('mid', '')).strip()
    note = str(p.get('note', '')).strip()
    if not name:
        continue
    # 无 mid 的（Luckzynnn）用名字作为 from_id 兜底（不会命中现有观众，记录留档）
    fid = mid if mid.isdigit() else ('name:' + name)
    eid = 'dsh:feeder:' + (mid if mid.isdigit() else 'name-' + str(i))
    summary = f"{name} 喂过饭（金主）：{note}。{rule}"
    lines.append(
        f"INSERT INTO memory_records (id, source, kind, title, summary, body, importance, confidence, private, "
        f"subject, asserted_by_id, asserted_by_name, from_id, from_name, valid_from) "
        f"VALUES ({sql_str(eid)}, 'content', 'preference', {sql_str('喂饭金主: ' + name)}, "
        f"{sql_str(summary)}, {sql_str(summary)}, 0.9, 0.95, false, "
        f"'viewer', {sql_str(fid)}, {sql_str(name)}, {sql_str(fid)}, {sql_str(name)}, now()) "
        f"ON CONFLICT (id) DO NOTHING;"
    )
    n += 1
lines.append('COMMIT;')
open(OUT, 'w', encoding='utf-8').write('\n'.join(lines))
print(f"feeders: {n} 条 → {OUT}")
