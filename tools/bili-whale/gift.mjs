// 从 dsh-bili-whale@0.4.5 tools/commands/gift.mjs 移植（MIT，来源与边界见 ADAPTATION.md）。
//
// 与上游的差异：
//   - 默认 dry-run，且 dry-run 不读凭据、不访问平台；真送必须显式 --go。
//   - 凭据只从 FISH_BILI_COOKIE / FISH_BILI_COOKIE_FILE 读取。
//   - 去掉 DSH 配置、房间/礼物白名单、本地账本与宿主日志，改由 Go 侧做每日限额。
//   - stdout 只输出一行 JSONL 协议帧，便于 Go 薄封装严格解析。
import { pathToFileURL } from 'node:url'
import { credentialHeader } from './danmaku-await.mjs'
import { decimalID } from './protocol.mjs'

const PROTOCOL = 1
const ROOM_URL = 'https://api.live.bilibili.com/room/v1/Room/get_info'
const GIFT_URL = 'https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig'
const NAV_URL = 'https://api.bilibili.com/x/web-interface/nav'
const SEND_URL = 'https://api.live.bilibili.com/xlive/revenue/v2/gift/sendGoldMultiUser'
const WEB_LOCATION = '444.8'
const PRICE_CEILING = 100 // price 100 = 1 电池；只防价格暴涨
const MAX_RESPONSE = 1024 * 1024
const MAX_NUM = 100
const HTTP_TIMEOUT_MS = 10000
const UA = 'Mozilla/5.0 RaiseMyFish/0.13'

class GiftError extends Error {
  constructor(kind, message) {
    super(message)
    this.kind = kind
  }
}

const fatal = message => new GiftError('fatal', message)
const retryable = message => new GiftError('retryable', message)

// 平台 ID 既可能是数字字面量也可能是字符串；统一成可安全放进 JSON 数字的十进制串。
const safeDecimalID = value => {
  const id = decimalID(value)
  if (!id || id.length > 16 || !Number.isSafeInteger(Number(id))) return null
  return id
}

function parseArgs(argv) {
  const values = new Map()
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    if (arg === '--dry-run' || arg === '--go') {
      if (values.has(arg)) throw fatal('参数重复')
      values.set(arg, true)
      continue
    }
    if (!['--room', '--gift-id', '--num'].includes(arg)) throw fatal('参数无效')
    if (values.has(arg)) throw fatal('参数重复')
    const value = argv[index + 1]
    if (value === undefined || value.startsWith('--')) throw fatal('参数缺少值')
    values.set(arg, value)
    index += 1
  }
  const room = safeDecimalID(values.get('--room'))
  const giftID = safeDecimalID(values.get('--gift-id'))
  const numText = values.get('--num')
  const num = typeof numText === 'string' && /^[1-9][0-9]{0,2}$/.test(numText) ? Number(numText) : 0
  if (!room || !giftID || num < 1 || num > MAX_NUM) throw fatal('房间号/礼物ID/数量无效')
  if (values.has('--dry-run') && values.has('--go')) throw fatal('--dry-run 与 --go 不能同时使用')
  return { room, giftID, num, dryRun: !values.has('--go') }
}

function sentFrame(options) {
  return { protocol: PROTOCOL, type: 'sent', gift_id: options.giftID, num: options.num }
}

function safeMessage(error) {
  const message = typeof error?.message === 'string' ? error.message : ''
  return message && message.length <= 120 && !/[\x00-\x1f\x7f]/.test(message) ? message : '礼物发送失败'
}

function errorFrame(error) {
  return {
    protocol: PROTOCOL,
    type: 'error',
    kind: error?.kind === 'retryable' ? 'retryable' : 'fatal',
    message: safeMessage(error),
  }
}

async function readLimited(response) {
  if (!response.body?.getReader) {
    const text = await response.text()
    if (typeof text !== 'string' || Buffer.byteLength(text) > MAX_RESPONSE) throw new Error('oversize')
    return text
  }
  const reader = response.body.getReader()
  const chunks = []
  let size = 0
  try {
    while (true) {
      const next = await reader.read()
      if (next.done) break
      size += next.value.byteLength
      if (size > MAX_RESPONSE) throw new Error('oversize')
      chunks.push(Buffer.from(next.value))
    }
  } finally {
    try { await reader.cancel() } catch { /* 不输出网络异常。 */ }
    reader.releaseLock()
  }
  return Buffer.concat(chunks).toString('utf8')
}

// 固定 B站 HTTPS 主机、禁止重定向；POST 阶段任何异常都按「可能已送出」保守上报。
async function httpJSON(url, { method = 'GET', form, cookie, referer, deps, posted = false }) {
  const target = new URL(url)
  if (target.protocol !== 'https:' || target.port || target.username || target.password || !['api.bilibili.com', 'api.live.bilibili.com'].includes(target.hostname)) {
    throw fatal('请求地址无效')
  }
  const timeoutMs = deps.httpTimeoutMs ?? HTTP_TIMEOUT_MS
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 60000) throw fatal('HTTP超时配置无效')
  const timeout = AbortSignal.timeout(timeoutMs)
  const signal = deps.signal ? AbortSignal.any([deps.signal, timeout]) : timeout
  const headers = {
    'user-agent': UA,
    referer,
    origin: 'https://live.bilibili.com',
    cookie,
    ...(method === 'POST' ? { 'content-type': 'application/x-www-form-urlencoded' } : {}),
  }
  let response
  try {
    response = await (deps.fetch ?? globalThis.fetch)(target.href, { method, redirect: 'error', signal, headers, ...(form === undefined ? {} : { body: form }) })
  } catch {
    throw posted ? fatal('发送结果未知，需人工核对', true) : retryable('网络请求失败')
  }
  if (!response || typeof response.status !== 'number' || typeof response.text !== 'function') {
    throw posted ? fatal('发送结果未知，需人工核对', true) : retryable('网络响应无效')
  }
  let text
  try {
    text = await readLimited(response)
  } catch {
    throw posted ? fatal('发送结果未知，需人工核对', true) : retryable('网络响应无效')
  }
  if (response.status === 401 || response.status === 403) throw fatal('登录态失效或无权操作')
  if (!response.ok || response.status < 200 || response.status >= 300) {
    throw posted ? fatal('发送结果未知，需人工核对', true) : retryable('平台暂时不可用')
  }
  try {
    return JSON.parse(text)
  } catch {
    throw posted ? fatal('发送结果未知，需人工核对', true) : retryable('平台响应无效')
  }
}

function cookieFields(cookie) {
  const fields = new Map()
  for (const part of cookie.split(';')) {
    const match = part.trim().match(/^([^=\s]+)=([^;\s]+)$/)
    if (!match || fields.has(match[1])) throw fatal('B站凭据无效')
    fields.set(match[1], match[2])
  }
  if (!fields.get('SESSDATA') || !fields.get('bili_jct') || !fields.get('buvid3')) throw fatal('B站凭据缺少SESSDATA/bili_jct/buvid3')
  return fields
}

export async function sendGift(options, deps = {}) {
  let cookie
  try {
    cookie = credentialHeader(deps.env ?? process.env)
  } catch {
    throw fatal('B站凭据无效')
  }
  const fields = cookieFields(cookie)
  const csrf = fields.get('bili_jct')
  const roomArg = options.room

  const info = await httpJSON(`${ROOM_URL}?room_id=${encodeURIComponent(roomArg)}`, {
    cookie, referer: `https://live.bilibili.com/${roomArg}`, deps,
  })
  if (info?.code !== 0 || !info.data) throw fatal('房间查询失败')
  const roomID = safeDecimalID(info.data.room_id)
  const anchor = safeDecimalID(info.data.uid)
  if (!roomID || !anchor) throw fatal('房间信息无效')
  const shortID = safeDecimalID(info.data.short_id) ?? roomID

  const config = await httpJSON(`${GIFT_URL}?room_id=${roomID}&platform=pc`, {
    cookie, referer: `https://live.bilibili.com/${shortID}`, deps,
  })
  if (config?.code !== 0 || !Array.isArray(config.data?.list)) throw fatal('礼物面板查询失败')
  const gift = config.data.list.find(item => safeDecimalID(item.id) === options.giftID)
  if (!gift) throw fatal('礼物ID不在当前房间面板')
  if (gift.bag_gift !== 0 && gift.bag_gift !== '0') throw fatal('该礼物不是直购礼物')
  const rawPrice = gift.price
  if (typeof rawPrice !== 'number' && typeof rawPrice !== 'string') throw fatal('礼物价格无效')
  const price = Number(rawPrice)
  if (!Number.isSafeInteger(price) || price < 0) throw fatal('礼物价格无效')
  if (price > PRICE_CEILING) throw fatal('礼物价格超过1电池上限')

  const nav = await httpJSON(NAV_URL, { cookie, referer: `https://live.bilibili.com/${shortID}`, deps })
  if (nav?.code !== 0 || nav.data?.isLogin !== true) throw fatal('登录态无效')
  const mid = safeDecimalID(nav.data.mid)
  if (!mid) throw fatal('账号信息无效')

  const form = new URLSearchParams({
    uid: mid,
    gift_id: options.giftID,
    ruid: anchor,
    send_ruid: '0',
    gift_num: String(options.num),
    coin_type: 'gold',
    bag_id: '0',
    platform: 'pc',
    biz_code: 'Live',
    biz_id: roomID,
    storm_beat_id: '0',
    price: String(price),
    receive_users: JSON.stringify([{ uid: Number(anchor) }]),
    all_flag: '0',
    live_statistics: JSON.stringify({
      pc_client: '', jumpfrom: '-99998', room_category: '-99998', source_event: 0, trackid: '',
      official_channel: { program_room_id: '-99998', program_up_id: '-99998' },
    }),
    statistics: JSON.stringify({ platform: 'pc', pc_client: '', appId: 100 }),
    web_location: WEB_LOCATION,
    csrf,
    csrf_token: csrf,
  })
  const response = await httpJSON(SEND_URL, {
    method: 'POST', form: form.toString(), cookie, referer: `https://live.bilibili.com/${shortID}`, deps, posted: true,
  })
  if (response?.code === 0) return sentFrame(options)
  // POST 之后任何非零都不再标记 retryable：避免对可能已扣费的请求自动重试。
  const code = Number.isSafeInteger(response?.code) ? response.code : 'unknown'
  throw fatal(`礼物发送被拒绝（code=${code}）`)
}

export async function runGift(argv, deps = {}) {
  try {
    const options = parseArgs(argv)
    return options.dryRun ? sentFrame(options) : await sendGift(options, deps)
  } catch (error) {
    return errorFrame(error)
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const frame = await runGift(process.argv.slice(2))
  process.stdout.write(`${JSON.stringify(frame)}\n`)
  if (frame.type === 'error') process.exitCode = frame.kind === 'retryable' ? 1 : 2
}
