// 调试：包装 fetch 打印每次请求的 URL/status，定位送礼失败点
import { pathToFileURL } from 'node:url'
import { credentialHeader } from '../tools/bili-whale/danmaku-await.mjs'

const cookie = credentialHeader(process.env)
const calls = []
const wrapped = async (url, init) => {
  try {
    const res = await globalThis.fetch(url, init)
    calls.push({ url: String(url).slice(0, 100), status: res.status })
    return res
  } catch (e) {
    calls.push({ url: String(url).slice(0, 100), error: String(e).slice(0, 120) })
    throw e
  }
}

const { sendGift } = await import('../tools/bili-whale/gift.mjs')
try {
  const out = await sendGift(
    { room: '1992099976', giftID: '31036', num: 1, dryRun: false, maxPrice: 100, maxNum: 10 },
    { fetch: wrapped, env: process.env }
  )
  console.log('OK', JSON.stringify(out))
} catch (e) {
  console.log('ERR', e.kind, e.message)
  console.log('STACK', String(e.stack).split('\n').slice(0, 6).join('\n'))
}
console.log('CALLS', JSON.stringify(calls, null, 1))
