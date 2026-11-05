// 验证语义检索：生成「有人叹气」等查询向量 → psql 查最相近的梗
import { execFileSync } from 'child_process'


const KEY = '08b483c8b87e49809f6304e28a12ab72.raOTIf27OMT1zyFd'

async function embed(text) {
  const res = await fetch('https://open.bigmodel.cn/api/paas/v4/embeddings', {
    method: 'POST',
    headers: { Authorization: `Bearer ${KEY}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: 'embedding-3', input: text, dimensions: 1024 }),
  })
  const data = await res.json()
  return data.data[0].embedding
}

function psql(sql) {
  return execFileSync(
    "C:/Program Files/PostgreSQL/17/bin/psql.exe",
    ['-h', '127.0.0.1', '-U', 'fish', '-d', 'raisemyfish', '-t', '-A', '-F', '|', '-c', sql],
    { env: { ...process.env, PGPASSWORD: 'fish_pass' }, encoding: 'utf8' }
  )
}

const queries = ['有人叹气', '她俩好配', '你咋这么魔']
for (const q of queries) {
  const vec = await embed(q)
  const lit = '[' + vec.join(',') + ']'
  const rows = psql(
    `SELECT title, ROUND((1 - (embedding <=> '${lit}'::vector))::numeric, 3) AS sim FROM knowledge_embeddings ORDER BY embedding <=> '${lit}'::vector LIMIT 3;`
  ).trim().split('\n')
  console.log(`\n查询「${q}」→`)
  for (const r of rows) console.log('  ', r)
}
