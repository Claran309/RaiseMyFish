"""DSH 历史数据迁移 → RaiseMyFish 库

数据源：C:/Users/Claran/.dsh/bili-live-export/ALL/
迁移：
1. danmaku-log/*.jsonl → events（in，历史归档）
2. dialogue/dialogue.jsonl → events（in+out 对）
3. activity/activity.jsonl → events（out）
4. people/*.md → viewer_profiles（194 人档案）
5. diary/recent-14d.md → memory_records（账号记忆）

生成 SQL 文件，由 psql 执行（ON CONFLICT DO NOTHING 幂等）。
"""
import json
import os
import re
import glob

SRC = r'C:\Users\Claran\.dsh\bili-live-export\ALL'
OUT = 'migrate_dsh.sql'

def sql_str(s):
    if s is None:
        return 'NULL'
    return "'" + str(s).replace("'", "''") + "'"

lines = []
lines.append("BEGIN;")

# ========== 1. danmaku-log → events ==========
n_dm = 0
for f in glob.glob(os.path.join(SRC, 'danmaku-log', '*.jsonl')):
    for line in open(f, encoding='utf-8'):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        eid = 'dsh:dm:' + str(r.get('dmid', ''))
        room = str(r.get('room', ''))
        at = r.get('at', '')
        if not at or not room:
            continue
        lines.append(
            f"INSERT INTO events (id, channel, dir, session, from_id, from_name, text, at, status, stage, meta) "
            f"VALUES ({sql_str(eid)}, 'live', 'in', {sql_str(room)}, {sql_str(str(r.get('uid','')))}, "
            f"{sql_str(r.get('name',''))}, {sql_str(r.get('text',''))}, {sql_str(at)}, 'done', 'verify', "
            f"{sql_str(json.dumps({'import':'dsh_danmaku','via':r.get('via','')}, ensure_ascii=False))}::jsonb) ON CONFLICT (id) DO NOTHING;"
        )
        n_dm += 1

# ========== 2. dialogue → events（in/out 对）==========
n_dlg = 0
dlg_path = os.path.join(SRC, 'dialogue', 'dialogue.jsonl')
if os.path.exists(dlg_path):
    for line in open(dlg_path, encoding='utf-8'):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        at = r.get('at', '')
        room = str(r.get('room', ''))
        uid = str(r.get('uid', ''))
        if not at or not room:
            continue
        key = f"{uid}:{at}"
        in_id = 'dsh:dlg:in:' + key
        out_id = 'dsh:dlg:out:' + key
        lines.append(
            f"INSERT INTO events (id, channel, dir, session, from_id, from_name, text, at, status, stage, meta) "
            f"VALUES ({sql_str(in_id)}, 'live', 'in', {sql_str(room)}, {sql_str(uid)}, {sql_str(r.get('from',''))}, "
            f"{sql_str(r.get('their_text',''))}, {sql_str(at)}, 'done', 'verify', {sql_str(json.dumps({'import':'dsh_dialogue'}, ensure_ascii=False))}::jsonb) ON CONFLICT (id) DO NOTHING;"
        )
        lines.append(
            f"INSERT INTO events (id, channel, dir, session, from_id, from_name, text, at, status, stage, meta) "
            f"VALUES ({sql_str(out_id)}, 'live', 'out', {sql_str(room)}, '', '大肥鱼', "
            f"{sql_str(r.get('fish_text',''))}, {sql_str(at)}, 'done', 'verify', {sql_str(json.dumps({'import':'dsh_dialogue','reply_to':r.get('from','')}, ensure_ascii=False))}::jsonb) ON CONFLICT (id) DO NOTHING;"
        )
        n_dlg += 1

# ========== 3. activity → events（out）==========
n_act = 0
act_path = os.path.join(SRC, 'activity', 'activity.jsonl')
if os.path.exists(act_path):
    for line in open(act_path, encoding='utf-8'):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        at = r.get('at', '')
        text = r.get('text', '')
        if not at or not text:
            continue
        ch = {'live': 'live', 'comment': 'comment', 'dm': 'dm', 'qq': 'qq_private'}.get(r.get('type', ''), 'live')
        eid = 'dsh:act:' + re.sub(r'[^0-9a-zA-Z]', '', at)[:20] + ':' + re.sub(r'[^0-9a-zA-Z]', '', text)[:12]
        lines.append(
            f"INSERT INTO events (id, channel, dir, session, from_id, from_name, text, at, status, stage, meta) "
            f"VALUES ({sql_str(eid)}, {sql_str(ch)}, 'out', 'dsh', '', '大肥鱼', {sql_str(text)}, {sql_str(at)}, "
            f"'done', 'verify', {sql_str(json.dumps({'import':'dsh_activity','to':r.get('to','')}, ensure_ascii=False))}::jsonb) ON CONFLICT (id) DO NOTHING;"
        )
        n_act += 1

# ========== 4. people → viewer_profiles ==========
n_ppl = 0
people_dir = os.path.join(SRC, 'people')
for f in glob.glob(os.path.join(people_dir, '*.md')):
    if os.path.basename(f) == 'INDEX.md':
        continue
    text = open(f, encoding='utf-8').read()
    m_uid = re.search(r'- uid：(\d+)', text)
    if not m_uid:
        continue
    uid = m_uid.group(1)
    m_name = re.search(r'^# (.+)$', text, re.M)
    name = m_name.group(1).strip() if m_name else uid
    m_hits = re.search(r'- 命中次数：(\d+)', text)
    m_rep = re.search(r'- 本鱼回过：(\d+)', text)
    m_seen = re.search(r'- 最后出现：([0-9T:.Z-]+)', text)
    hits = int(m_hits.group(1)) if m_hits else 0
    replies = int(m_rep.group(1)) if m_rep else 0
    seen = m_seen.group(1) if m_seen else '2026-11-03T00:00:00Z'
    # 最近说过的话（`HH:MM` 文本）
    said = re.findall(r'^- `(\d{2}:\d{2})` (.+)$', text, re.M)
    replies_txt = re.findall(r'^- `(\d{2}:\d{2})` (.+)$', text.split('## 本鱼回过的')[1], re.M) if '## 本鱼回过的' in text else []
    recent_texts = json.dumps([t for _, t in said[:15]], ensure_ascii=False)
    recent_replies = json.dumps([t for _, t in replies_txt[:10]], ensure_ascii=False)
    lines.append(
        f"INSERT INTO viewer_profiles (uid, name, hits, replies, last_seen, recent_texts, recent_replies) "
        f"VALUES ({sql_str(uid)}, {sql_str(name)}, {hits}, {replies}, {sql_str(seen)}, "
        f"{sql_str(recent_texts)}::jsonb, {sql_str(recent_replies)}::jsonb) "
        f"ON CONFLICT (uid) DO UPDATE SET name=EXCLUDED.name, hits=GREATEST(viewer_profiles.hits, EXCLUDED.hits), "
        f"replies=GREATEST(viewer_profiles.replies, EXCLUDED.replies), "
        f"recent_texts=EXCLUDED.recent_texts, recent_replies=EXCLUDED.recent_replies, updated_at=clock_timestamp();"
    )
    n_ppl += 1

lines.append("COMMIT;")
open(OUT, 'w', encoding='utf-8').write('\n'.join(lines))
print(f"SQL 生成：danmaku={n_dm} dialogue_pairs={n_dlg} activity={n_act} people={n_ppl} → {OUT}")
