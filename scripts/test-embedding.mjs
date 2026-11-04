// 测 embedding 额度：批量 32 条中文梗，看返回与耗时
import { createRequire } from 'module'
const require = createRequire(import.meta.url)

const KEY = process.env.FISH_EMBEDDING_KEY || '08b483c8b87e49809f6304e28a12ab72.raOTIf27OMT1zyFd'
const URL = 'https://open.bigmodel.cn/api/paas/v4/embeddings'

async function embed(input, dims) {
  const body = { model: 'embedding-3', input, dimensions: dims }
  const t0 = Date.now()
  const res = await fetch(URL, {
    method: 'POST',
    headers: { Authorization: `Bearer ${KEY}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const ms = Date.now() - t0
  const data = await res.json()
  if (!res.ok) return { ok: false, error: data, ms }
  const vec = data.data?.[0]?.embedding
  return { ok: true, dims: vec?.length, ms, tokens: data.usage?.total_tokens, first: vec?.slice(0, 2) }
}

// 1. 单条
console.log('单条:', JSON.stringify(await embed('摸摸你', 1024)))

// 2. 批量 32 条（模拟真实入库批次）
const batch = Array.from({ length: 32 }, (_, i) => `测试梗${i}：这个词怎么接`)
const t0 = Date.now()
const res = await fetch(URL, {
  method: 'POST',
  headers: { Authorization: `Bearer ${KEY}`, 'Content-Type': 'application/json' },
  body: JSON.stringify({ model: 'embedding-3', input: batch, dimensions: 1024 }),
})
const data = await res.json()
console.log('批量32:', res.status, '条数:', data.data?.length, '耗时:', Date.now() - t0, 'ms', '错误:', data.error?.message || '')
