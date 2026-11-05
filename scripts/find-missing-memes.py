"""从真实弹幕里找「高频但无独立知识条目的词」（RAG 召回缺口）"""
import re
import subprocess
import os

# 1. 读知识库现有 title
titles = set()
for f in os.listdir('personas/dafeiyu/knowledge/memes'):
    p = os.path.join('personas/dafeiyu/knowledge/memes', f)
    if not p.endswith('.md'):
        continue
    for line in open(p, encoding='utf-8'):
        m = re.match(r'\s*title:\s*"(.*)"', line)
        if m:
            titles.add(m.group(1).strip())

print('知识库 title 数:', len(titles))

# 2. 读 events 里的真实弹幕文本
env = dict(os.environ, PGPASSWORD='fish_pass', PGCLIENTENCODING='UTF8')
subprocess.run(
    ["C:/Program Files/PostgreSQL/17/bin/psql.exe", "-h", "127.0.0.1", "-U", "fish",
     "-d", "raisemyfish", "-t", "-A", "-o", "dm_texts.txt", "-c",
     "SELECT text FROM events WHERE dir='in' AND length(text) BETWEEN 2 AND 30;"],
    env=env, capture_output=True,
)
texts = open('dm_texts.txt', 'rb').read().decode('utf-8', 'replace')
lines = [l.strip() for l in texts.splitlines() if l.strip()]
print('弹幕样本:', len(lines))

# 3. 提取 2-8 字中文 n-gram，统计词频
from collections import Counter
cnt = Counter()
for t in lines:
    for n in range(2, 9):
        for i in range(len(t) - n + 1):
            w = t[i:i+n]
            if re.fullmatch(r'[\u4e00-\u9fff]+', w):
                cnt[w] += 1

# 4. 找「高频但无独立条目」的词（排除已有 title 的子串/包含关系）
missing = []
for w, c in cnt.most_common(2000):
    if c < 3:
        break
    # 已有 title 包含该词（或反之）→ 视为已覆盖
    covered = any(w in t or t in w for t in titles)
    if not covered:
        missing.append((w, c))

print('\n=== 高频未收录候选（Top 30）===')
for w, c in missing[:30]:
    print(f'{w}\t{c}')
