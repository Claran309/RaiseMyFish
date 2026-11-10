"""迁移完整性校验：旧家 vs 新家逐条比对

旧家：C:/Users/Claran/.dsh/bili-live-export/ALL/
新家：raisemyfish 库（events/viewer_profiles/memory_records/knowledge）

检查：
1. 弹幕：旧家 danmaku-log/*.jsonl 每条 → 新家 events（id=dsh:dm:<dmid>）
2. 观众：旧家 people/*.md → 新家 viewer_profiles（uid）
3. 对话：旧家 dialogue.jsonl → 新家 events（dsh:dlg:in/out）
4. 活动：旧家 activity.jsonl → 新家 events（dsh:act:*）
5. 记忆：旧家 all-memory.md → 新家 memory_records（dsh:amem:*）
6. 梗：旧家 memes-export.json → 新家 knowledge（title 集合）

输出报告（缺失/多余），不写入数据库。
"""
import json
import glob
import os
import re
import subprocess

ROOT = r'C:\Users\Claran\.dsh\bili-live-export\ALL'
PG = r'C:\Program Files\PostgreSQL\17\bin\psql.exe'
ENV = {'PGPASSWORD': 'fish_pass', 'PGCLIENTENCODING': 'UTF8'}

def query(sql):
    r = subprocess.run([PG, '-h', '127.0.0.1', '-U', 'fish', '-d', 'raisemyfish', '-t', '-A', '-c', sql],
                       capture_output=True, text=True, encoding='utf-8', env={**os.environ, **ENV})
    return [line for line in r.stdout.splitlines() if line.strip()]

def sql_str(s):
    return "'" + str(s).replace("'", "''") + "'"

report = []

# 1. 弹幕
dm_ids = []
for f in glob.glob(os.path.join(ROOT, 'danmaku-log', '*.jsonl')):
    for line in open(f, encoding='utf-8'):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get('dmid'):
            dm_ids.append('dsh:dm:' + str(r['dmid']))
db_dm = set(query("SELECT id FROM events WHERE id LIKE 'dsh:dm:%';"))
missing = [i for i in set(dm_ids) if i not in db_dm]
report.append(f"弹幕：旧家 {len(set(dm_ids))} 条，新家 {len(db_dm)} 条，缺 {len(missing)}")

# 2. 观众
people = {}
for f in glob.glob(os.path.join(ROOT, 'people', '*.md')):
    if os.path.basename(f) == 'INDEX.md':
        continue
    m = re.search(r'- uid：(\d+)', open(f, encoding='utf-8').read())
    if m:
        people[m.group(1)] = os.path.basename(f)
db_uids = set(query("SELECT uid FROM viewer_profiles;"))
missing = [u for u in people if u not in db_uids]
report.append(f"观众：旧家 {len(people)} 人，新家 {len(db_uids)} 人，缺 {len(missing)}")

# 3. 对话
dlg_keys = []
for line in open(os.path.join(ROOT, 'dialogue', 'dialogue.jsonl'), encoding='utf-8'):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except Exception:
        continue
    if r.get('at') and r.get('uid'):
        dlg_keys.append(f"{r['uid']}:{r['at']}")
db_dlg = set(query("SELECT id FROM events WHERE id LIKE 'dsh:dlg:in:%';"))
missing = [k for k in dlg_keys if ('dsh:dlg:in:' + k) not in db_dlg]
report.append(f"对话：旧家 {len(dlg_keys)} 对，新家入向 {len(db_dlg)}，缺 {len(missing)}")

# 4. 活动
act_n = sum(1 for line in open(os.path.join(ROOT, 'activity', 'activity.jsonl'), encoding='utf-8') if line.strip())
db_act = len(query("SELECT id FROM events WHERE id LIKE 'dsh:act:%';"))
report.append(f"活动：旧家 {act_n} 条，新家 {db_act} 条")

# 5. 记忆
amem = 0
s = open(os.path.join(ROOT, 'diary', 'all-memory.md'), encoding='utf-8').read()
for b in s.split('\n### ')[1:]:
    if re.match(r'\d{4}-\d{2}-\d{2}', b.strip()):
        amem += 1
db_amem = len(query("SELECT id FROM memory_records WHERE id LIKE 'dsh:amem:%';"))
report.append(f"记忆（all-memory）：旧家 {amem} 条，新家 {db_amem} 条")

# 6. 梗
d = json.load(open(os.path.join(ROOT, 'memes', 'memes-export.json'), encoding='utf-8'))
old_titles = {str(it.get('title', '')).strip() for it in d.get('items', []) if str(it.get('title', '')).strip()}
kb_titles = set()
kb_dir = 'personas/dafeiyu/knowledge/memes'
for f in os.listdir(kb_dir):
    if f.endswith('.md'):
        for line in open(os.path.join(kb_dir, f), encoding='utf-8'):
            m = re.match(r'\s*title:\s*"(.+)"\s*$', line)
            if m:
                kb_titles.add(m.group(1).strip())
missing = [t for t in old_titles if t not in kb_titles]
report.append(f"梗：旧家 {len(old_titles)} 条，新家 {len(kb_titles)} 条，缺 {len(missing)}")

print('\n'.join(report))
