// 临时验证：把 MAX_RESPONSE 从 1MiB 提到 4MiB 后，真实送礼能否成功
import { readFileSync, writeFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { credentialHeader } from '../tools/bili-whale/danmaku-await.mjs'

const src = readFileSync('tools/bili-whale/gift.mjs', 'utf8')
const patched = src.replace('const MAX_RESPONSE = 1024 * 1024', 'const MAX_RESPONSE = 4 * 1024 * 1024')
writeFileSync('tools/bili-whale/tmp-gift-fixed.mjs', patched)

const { sendGift } = await import(pathToFileURL(process.cwd() + '/tools/bili-whale/tmp-gift-fixed.mjs').href)
const cookie = credentialHeader(process.env)
const calls = []
const wrapped = async (url, init) => {
  const res = await globalThis.fetch(url, init)
  calls.push({ url: String(url).slice(0, 80), status: res.status })
  return res
}
try {
  const out = await sendGift(
    { room: '1992099976', giftID: '31036', num: 1, dryRun: false, maxPrice: 100, maxNum: 10 },
    { fetch: wrapped, env: process.env }
  )
  console.log('OK', JSON.stringify(out))
} catch (e) {
  console.log('ERR', e.kind, e.message)
}
console.log('CALLS', JSON.stringify(calls))
