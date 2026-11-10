"""旧家剩余数据迁移：all-memory.md → memory_records；memes 梗库 → knowledge 比对

1. all-memory.md：账号记忆条目（总结/正文）→ memory_records（kind=session, source=content）
2. memes-export.json：177 条梗 → 与现有 personas/dafeiyu/knowledge/memes 比对 title
   - 已存在的跳过
   - 缺的写入 personas/dafeiyu/knowledge/memes/legacy-old-home.md（K0 格式）

幂等：记忆 ID 用 diary 时间+索引；梗按 title 比对。
"""
import json
import os
import re

ROOT = r'C:\Users\Claran\.dsh\bili-live-export\ALL'
OUT = 'migrate_rest.sql'

def sql_str(s):
    if s is None:
        return 'NULL'
    return "'" + str(s).replace("'", "''") + "'"

lines = ['BEGIN;']

# ===== 1. all-memory.md → memory_records =====
src = os.path.join(ROOT, 'diary', 'all-memory.md')
text = open(src, encoding='utf-8').read()
blocks = re.split(r'\n### ', text)
entries = []
for b in blocks[1:]:
    lines_b = b.split('\n')
    head = lines_b[0].strip()
    m = re.match(r'(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\s*·\s*(.*)$', head)
    if not m:
        continue
    at, title = m.group(1), m.group(2).strip()
    summary = ''
    body = ''
    m_sum = re.search(r'\*\*总结\*\*：(.*)', b)
    if m_sum:
        summary = m_sum.group(1).strip()
    m_body = re.search(r'\*\*正文\*\*：(.*)', b)
    if m_body:
        body = m_body.group(1).strip()
    entries.append({'at': at, 'title': title, 'summary': summary or title, 'body': body or summary or title})

seen = set()
n_mem = 0
for i, e in enumerate(entries):
    key = re.sub(r'[^0-9a-zA-Z]', '', e['at'])[:14] + ':' + re.sub(r'[^0-9a-zA-Z]', '', e['title'])[:20]
    if key in seen:
        continue
    seen.add(key)
    eid = 'dsh:amem:' + key[:40]
    ts = e['at'].replace(' ', 'T') + '+08:00'
    lines.append(
        f"INSERT INTO memory_records (id, source, kind, title, summary, body, importance, confidence, private, valid_from) "
        f"VALUES ({sql_str(eid)}, 'content', 'session', {sql_str(e['title'])}, {sql_str(e['summary'])}, {sql_str(e['body'])}, "
        f"0.7, 0.8, false, {sql_str(ts)}) ON CONFLICT (id) DO NOTHING;"
    )
    n_mem += 1

lines.append('COMMIT;')
open(OUT, 'w', encoding='utf-8').write('\n'.join(lines))

# ===== 2. memes 比对（不写 SQL，输出缺失清单） =====
d = json.load(open(os.path.join(ROOT, 'memes', 'memes-export.json'), encoding='utf-8'))
items = d.get('items', [])
kb_dir = 'personas/dafeiyu/knowledge/memes'
existing = set()
for f in os.listdir(kb_dir):
    if f.endswith('.md'):
        for line in open(os.path.join(kb_dir, f), encoding='utf-8'):
            m = re.match(r'^## (.+)$', line.strip())
            if m:
                existing.add(m.group(1).strip())
missing = [it for it in items if str(it.get('title', '')).strip() and str(it.get('title', '')).strip() not in existing]
print(f"all-memory: {n_mem} 条 → {OUT}")
print(f"梗库: 旧家 {len(items)} 条，现有 {len(existing)} 条，缺 {len(missing)} 条")
json.dump([{'title': it.get('title', ''), 'usage': it.get('usage', ''), 'tags': it.get('tags', '')} for it in missing],
          open('tmp_missing_memes.json', 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
