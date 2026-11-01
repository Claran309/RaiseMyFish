// 从 dsh-bili-whale 移植固定弹幕表单；MIT 出处及边界见 SENDER_ADAPTATION.md。
import { pathToFileURL } from 'node:url'
import { credentialHeader } from './danmaku-await.mjs'
import { decimalID, parseWireJSON, MAX_BYTES, failure } from './protocol.mjs'

const sessions = new WeakMap()
const NAV = 'https://api.bilibili.com/x/web-interface/nav'
const ROOM = 'https://api.live.bilibili.com/room/v1/Room/get_info'
const SEND = 'https://api.live.bilibili.com/msg/send'
const HISTORY = 'https://api.live.bilibili.com/xlive/web-room/v1/dM/gethistory'
const negative = new Map([[10031, 'rate_limit'], [12006, 'content'], [12002, 'unavailable'], [-101, 'auth'], [-111, 'auth'], [1003212, 'invalid'], [-400, 'invalid']])
const token = value => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value)
const stableID = value => {
  if (typeof value !== 'string' && typeof value !== 'number') return null
  if (typeof value === 'number' && (!Number.isSafeInteger(value) || value <= 0)) return null
  const id = String(value)
  return token(id) && !/^0+$/.test(id) ? id : null
}
const result = (outcome, error_kind = '', msg_id = '', evidence = 'none') => ({ outcome, msg_id, verified: evidence !== 'none', error_kind, evidence })

function validRequest(input) {
  if (!input || !token(input.request_id) || !decimalID(input.room_id) || !decimalID(input.self_id) || input.private !== false || typeof input.text !== 'string' || !input.text.isWellFormed()) throw failure('invalid')
  const count = [...input.text].length
  if (count < 1 || count > 40) throw failure('invalid')
  return Object.freeze({ request_id: input.request_id, room_id: decimalID(input.room_id), self_id: decimalID(input.self_id), private: false, text: input.text })
}

function boundRequest(request, session) {
  const bound = sessions.get(session), given = validRequest(request)
  if (!bound || Object.keys(given).some(key => given[key] !== bound.request[key])) throw failure('invalid')
  return bound
}

function credentials(deps) {
  const cookie = deps.cookie === undefined ? credentialHeader(deps.env ?? process.env) : credentialHeader({ FISH_BILI_COOKIE: deps.cookie })
  const fields = new Map()
  for (const part of cookie.split(';')) {
    const match = part.trim().match(/^([^=\s]+)=([^;\s]+)$/)
    if (!match || fields.has(match[1])) throw failure('credentials')
    fields.set(match[1], match[2])
  }
  if (!fields.get('SESSDATA') || !fields.get('bili_jct') || !fields.get('buvid3')) throw failure('credentials')
  return { cookie, csrf: fields.get('bili_jct') }
}

async function responseText(response) {
  if (!response.body?.getReader) {
    const text = await response.text()
    if (typeof text !== 'string' || Buffer.byteLength(text) > MAX_BYTES) throw failure('protocol')
    return text
  }
  const reader = response.body.getReader(), chunks = []
  let size = 0
  try {
    while (true) {
      const next = await reader.read()
      if (next.done) break
      size += next.value.byteLength
      if (size > MAX_BYTES) throw failure('protocol')
      chunks.push(Buffer.from(next.value))
    }
  } finally {
    try { await reader.cancel() } catch { /* 不输出网络异常或平台报文。 */ }
    reader.releaseLock()
  }
  return Buffer.concat(chunks).toString('utf8')
}

// 固定目标、禁止重定向；超时覆盖 fetch 与整个响应读取，调用取消不会触发重试。
async function http(url, method, session, deps, body) {
  const target = new URL(url)
  if (target.protocol !== 'https:' || target.port || target.username || target.password || !['api.bilibili.com', 'api.live.bilibili.com'].includes(target.hostname)) throw failure('invalid')
  const timeoutMs = deps.httpTimeoutMs ?? 10000
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 60000) throw failure('invalid')
  const timeout = AbortSignal.timeout(timeoutMs)
  const signal = deps.signal ? AbortSignal.any([deps.signal, timeout]) : timeout
  signal.throwIfAborted()
  let onAbort
  const aborted = new Promise((_, reject) => { onAbort = () => reject(failure('network')); signal.addEventListener('abort', onAbort, { once: true }) })
  try {
    return await Promise.race([aborted, (async () => {
      const response = await (deps.fetch ?? globalThis.fetch)(target.href, { method, redirect: 'error', signal, headers: {
        cookie: session.cookie, origin: 'https://live.bilibili.com', referer: `https://live.bilibili.com/${session.request.room_id}`,
        'user-agent': 'Mozilla/5.0 RaiseMyFish/0.8', ...(method === 'POST' ? { 'content-type': 'application/x-www-form-urlencoded' } : {}),
      }, ...(body === undefined ? {} : { body }) })
      // HTTP 失败不能用看似成功的 JSON 伪装为可信回执。
      if (response.status === 401 || response.status === 403) throw failure('auth')
      if (!response.ok || response.status < 200 || response.status >= 300) throw failure('network')
      return parseWireJSON(await responseText(response))
    })()])
  } finally { signal.removeEventListener('abort', onAbort) }
}

export async function preflight(request, deps = {}) {
  try {
    const immutable = validRequest(request), secret = credentials(deps)
    const bound = { request: immutable, ...secret }
    const nav = await http(NAV, 'GET', bound, deps)
    if (nav?.code !== 0 || nav.data?.isLogin !== true || decimalID(nav.data?.mid) !== immutable.self_id) throw failure('auth')
    const room = await http(`${ROOM}?room_id=${immutable.room_id}`, 'GET', bound, deps)
    if (room?.code !== 0 || decimalID(room.data?.room_id) !== immutable.room_id) throw failure('invalid')
    const session = Object.freeze({ request_id: immutable.request_id, room_id: immutable.room_id, self_id: immutable.self_id })
    sessions.set(session, bound)
    return session
  } catch (error) { throw failure(['auth', 'credentials', 'invalid'].includes(error?.code) ? error.code : 'network') }
}

function validPermit(permit, request) {
  return permit && permit.request_id === request.request_id && token(permit.attempt_id) && decimalID(permit.nonce) && typeof permit.expires_at === 'string' && Number.isFinite(Date.parse(permit.expires_at)) && Date.parse(permit.expires_at) > Date.now()
}

function receiptID(data) {
  let extra = data?.mode_info?.extra
  if (typeof extra === 'string') extra = parseWireJSON(extra)
  return [data?.id_str, data?.dmid, data?.msg_id, extra?.id_str, extra?.dmid].map(stableID).find(Boolean) ?? ''
}

export async function postOnce(request, session, permit, deps = {}) {
  let bound, nonce
  try {
    bound = boundRequest(request, session)
    if (deps.signal?.aborted) return result('not_posted', 'cancelled')
    if (!validPermit(permit, bound.request)) return result('not_posted', 'permit')
    nonce = decimalID(permit.nonce)
  } catch { return result('not_posted', 'invalid') }
  const form = new URLSearchParams({ csrf: bound.csrf, csrf_token: bound.csrf, roomid: bound.request.room_id, msg: bound.request.text, rnd: nonce,
    fontsize: '25', color: '16777215', mode: '1', bubble: '0', room_type: '0', jumpfrom: '0', statistics: JSON.stringify({ appId: 100, platform: 5 }) })
  try {
    const data = await http(SEND, 'POST', bound, deps, form.toString())
    if (data?.code === 0) {
      const id = receiptID(data.data)
      return result('accepted', '', id, id ? 'receipt_id' : 'none')
    }
    return negative.has(data?.code) ? result('rejected', negative.get(data.code)) : result('unknown', 'protocol')
  } catch (error) { return result('unknown', error?.code === 'auth' ? 'auth' : 'network') }
}

export async function verifyOnce(request, session, expected, deps = {}) {
  try {
    const bound = boundRequest(request, session)
    if (!expected || expected.request_id !== bound.request.request_id || !token(expected.attempt_id)) return result('unknown', 'invalid')
    const knownID = expected.msg_id === undefined || expected.msg_id === '' ? '' : stableID(expected.msg_id)
    if (knownID === null || (!knownID && !decimalID(expected.nonce))) return result('unknown', 'invalid')
    // 核验允许许可已过期；只读持久化的尝试ID/nonce，不产生新投递授权。
    const nonce = decimalID(expected.nonce)
    const data = await http(`${HISTORY}?roomid=${bound.request.room_id}&room_type=0`, 'GET', bound, deps)
    if (data?.code !== 0 || !Array.isArray(data.data?.room)) return result('unknown', 'protocol')
    for (const row of data.data.room) {
      if (decimalID(row?.uid) !== bound.request.self_id || row?.text !== bound.request.text) continue
      const id = receiptID(row)
      if (!id) continue
      if (knownID ? id === knownID : decimalID(row.rnd) === nonce) return result('accepted', '', id, knownID ? 'history_id' : 'history_nonce')
    }
    return result('unknown', 'unconfirmed')
  } catch (error) { return result('unknown', error?.code === 'auth' ? 'auth' : 'network') }
}

// stdin 每行及单次进程总输入最多16KiB；只消费 prepare + commit 两帧。
function inputLines(stream, signal) {
  let buffer = Buffer.alloc(0), bytes = 0, ended = false, error, waiter
  const queued = []
  const wake = () => { waiter?.(); waiter = undefined }
  const fail = () => { error = failure('protocol'); wake() }
  const onData = chunk => {
    bytes += chunk.length
    if (bytes > 16384) return fail()
    buffer = Buffer.concat([buffer, chunk])
    let index
    while ((index = buffer.indexOf(10)) >= 0) {
      queued.push(buffer.subarray(0, index).toString('utf8')); buffer = buffer.subarray(index + 1)
      if (queued.length > 2) return fail()
    }
    wake()
  }
  const onEnd = () => { ended = true; if (buffer.length) fail(); wake() }
  const onAbort = () => { error = failure('cancelled'); wake() }
  stream.on('data', onData); stream.on('end', onEnd); stream.on('error', fail); signal.addEventListener('abort', onAbort)
  return {
    async next() {
      while (!error && !queued.length && !ended) await new Promise(resolve => { waiter = resolve })
      if (error) throw error
      if (!queued.length) throw failure('eof')
      const line = queued.shift()
      if (!line || Buffer.byteLength(line) > 16384) throw failure('protocol')
      return parseWireJSON(line)
    },
    close() { stream.off('data', onData); stream.off('end', onEnd); stream.off('error', fail); signal.removeEventListener('abort', onAbort); stream.pause() },
  }
}

async function main() {
  let request, attemptID = '', posted = false, lines
  const argv = process.argv.slice(2)
  const verifying = argv[0] === '--action' && argv[1] === 'verify'
  const send = argv.length === 3 && argv[0] === '--action' && argv[1] === 'send' && argv[2] === '--go'
  const verify = argv.length === 2 && verifying
  const controller = new AbortController(), stop = () => controller.abort()
  process.once('SIGINT', stop); process.once('SIGTERM', stop)
  process.stdin.once('end', stop)
  // 整个握手也有上限；Go父进程退出stdin或取消后不会继续投递。
  const timer = setTimeout(stop, 30000)
  const emit = frame => process.stdout.write(JSON.stringify({ protocol: 1, ...frame }) + '\n')
  try {
    if (!send && !verify) throw failure('disabled')
    lines = inputLines(process.stdin, controller.signal)
    const first = await lines.next()
    // 核验针对既有投递；预检失败也须保留原尝试，不能声称原投递尚未发生。
    if (verify) attemptID = token(first?.attempt_id) ? first.attempt_id : ''
    if (first?.protocol !== 1 || first.type !== (send ? 'prepare' : 'verify')) throw failure('protocol')
    request = validRequest(first)
    const session = await preflight(request, { signal: controller.signal })
    if (send) {
      emit({ type: 'ready', request_id: request.request_id, room_id: session.room_id, self_id: session.self_id })
      const commit = await lines.next()
      if (commit?.protocol !== 1 || commit.type !== 'commit') throw failure('protocol')
      attemptID = token(commit.attempt_id) ? commit.attempt_id : ''
      // 父进程须保持stdin到result；EOF及取消会撤销尚未投递的许可。
      const output = await postOnce(request, session, commit, { signal: controller.signal })
      posted = output.outcome !== 'not_posted'
      emit({ type: 'result', request_id: request.request_id, attempt_id: attemptID, result: output })
      if (output.outcome !== 'accepted') process.exitCode = 1
    } else {
      const output = await verifyOnce(request, session, first, { signal: controller.signal })
      emit({ type: 'result', request_id: request.request_id, attempt_id: attemptID, result: output })
      if (!output.verified) process.exitCode = 1
    }
  } catch (error) {
    const kind = ['disabled', 'protocol', 'credentials', 'auth', 'invalid', 'cancelled', 'eof'].includes(error?.code) ? error.code : 'network'
    emit({ type: 'result', request_id: request?.request_id ?? '', attempt_id: attemptID, result: result(verifying || posted ? 'unknown' : 'not_posted', kind) })
    process.exitCode = 1
  } finally {
    clearTimeout(timer); lines?.close(); process.off('SIGINT', stop); process.off('SIGTERM', stop); process.stdin.off('end', stop)
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main()
