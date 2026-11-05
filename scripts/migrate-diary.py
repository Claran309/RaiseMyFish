"""DSH 日记记忆迁移 → memory_records

来源：diary/recent-14d.md（35 条账号记忆：动态/投稿/互动）
每条 → memory_records（kind=session, source=content）
"""
import re
import json

SRC = r'C:\Users\Claran\.dsh\bili-live-export\ALL\diary\recent-14d.md'
OUT = 'migrate_diary.sql'

def sql_str(s):
    if s is None:
        return 'NULL'
    return "'" + str(s).replace("'", "''") + "'"

text = open(SRC, encoding='utf-8').read()

# 按 ### 切分条目
blocks = re.split(r'\n### ', text)
entries = []
for b in blocks[1:]:
    lines = b.split('\n')
    head = lines[0].strip()  # 2026-10-27 22:50:08 · 动态
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

lines = ['BEGIN;']
for i, e in enumerate(entries):
    eid = 'dsh:diary:' + re.sub(r'[^0-9a-zA-Z]', '', e['at'])[:14] + ':' + str(i)
    lines.append(
        f"INSERT INTO memory_records (id, source, kind, title, summary, body, importance, confidence, private, valid_from) "
        f"VALUES ({sql_str(eid)}, 'content', 'session', {sql_str(e['title'])}, {sql_str(e['summary'])}, {sql_str(e['body'])}, "
        f"0.7, 0.8, false, {sql_str(e['at'].replace(' ', 'T') + '+08:00')}) ON CONFLICT (id) DO NOTHING;"
    )
lines.append('COMMIT;')
open(OUT, 'w', encoding='utf-8').write('\n'.join(lines))
print(f'日记条目 {len(entries)} → {OUT}')
