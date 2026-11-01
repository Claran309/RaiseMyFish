// 单连接采集器；重连、持久化与去重由 Go 父进程负责。
import fs from 'node:fs'
import { pathToFileURL } from 'node:url'
import { PacketDecoder, MAX_BYTES, packet, normalizeMessage, parseWireJSON, decimalID, wbiQuery, failure } from './protocol.mjs'

const COOKIE_LIMIT = 64 * 1024
const HTTP_TARGETS = new Set(['api.bilibili.com', 'api.live.bilibili.com'])
const SAFE_CODES = new Set(['auth', 'credentials', 'runtime', 'protocol', 'network', 'timeout', 'rate_limit', 'missing_message_id', 'missing_timestamp', 'invalid_message', 'backpressure'])
const safeCode = error => SAFE_CODES.has(error?.code) ? error.code : 'runtime'
const fatalCode = code => ['auth', 'credentials', 'runtime'].includes(code)
const errorFrame = code => ({ protocol: 1, type: 'error', kind: code, fatal: fatalCode(code), retryable: !fatalCode(code) })

// 只读取调用者明确指定的文件，且读取大小有硬上限。
export function credentialHeader(env = process.env) {
  try {
    let value = env.FISH_BILI_COOKIE
    if (!value && env.FISH_BILI_COOKIE_FILE) {
      const fd = fs.openSync(env.FISH_BILI_COOKIE_FILE, 'r')
      try {
        if (!fs.fstatSync(fd).isFile()) throw failure('credentials')
        const buffer = Buffer.alloc(COOKIE_LIMIT + 1)
        const size = fs.readSync(fd, buffer, 0, buffer.length, 0)
        if (size > COOKIE_LIMIT) throw failure('credentials')
        value = buffer.subarray(0, size).toString('utf8').trim()
      } finally { fs.closeSync(fd) }
      if (value.startsWith('{')) {
        const data = parseWireJSON(value)
        if (typeof data.sessdata !== 'string' || !data.sessdata) throw failure('credentials')
        value = [['SESSDATA', data.sessdata], ['bili_jct', data.bili_jct], ['buvid3', data.buvid3]]
          .filter(([, item]) => item !== undefined && item !== '')
          .map(([key, item]) => {
            if (typeof item !== 'string' || /[;\r\n\x00-\x1f\x7f]/.test(item)) throw failure('credentials')
            return `${key}=${item}`
          }).join('; ')
      }
    }
    if (typeof value !== 'string' || !value || Buffer.byteLength(value) > COOKIE_LIMIT || /[\x00-\x1f\x7f]/.test(value) || !/(?:^|;\s*)SESSDATA=[^;\s]+/.test(value)) throw failure('credentials')
    return value
  } catch { throw failure('credentials') }
}

function validateHost(host, port) {
  if (typeof host !== 'string' || !/^(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+chat\.bilibili\.com$/.test(host) || !Number.isInteger(port) || port < 1 || port > 65535) throw failure('protocol')
}

function validateOptions(options) {
  if (!decimalID(options.room) || !decimalID(options.selfId)) throw failure('runtime')
  for (const key of ['httpTimeoutMs', 'connectTimeoutMs', 'heartbeatMs', 'idleMs']) {
    if (!Number.isSafeInteger(options[key]) || options[key] < 1 || options[key] > 2147483647) throw failure('runtime')
  }
  if (!Number.isSafeInteger(options.hostIndex) || options.hostIndex < 0) throw failure('runtime')
}

async function responseText(response) {
  // 测试替身可只提供 text；原生 fetch 使用流读取，避免远端无界响应。
  if (!response.body?.getReader) {
    const text = await response.text()
    if (Buffer.byteLength(text) > MAX_BYTES) throw failure('protocol')
    return text
  }
  const reader = response.body.getReader(), chunks = []
  let size = 0
  try {
    while (true) {
      const result = await reader.read()
      if (result.done) break
      size += result.value.byteLength
      if (size > MAX_BYTES) throw failure('protocol')
      chunks.push(Buffer.from(result.value))
    }
  } finally {
    try { await reader.cancel() } catch { /* 释放响应，不输出底层异常。 */ }
    reader.releaseLock()
  }
  return Buffer.concat(chunks).toString('utf8')
}

export async function bootstrap(options, deps = {}) {
  validateOptions(options)
  if (options.signal?.aborted) return null
  const fetcher = deps.fetch ?? globalThis.fetch
  const cookie = credentialHeader({ FISH_BILI_COOKIE: deps.cookie ?? credentialHeader() })
  const get = async address => {
    const url = new URL(address)
    if (url.protocol !== 'https:' || !HTTP_TARGETS.has(url.hostname) || url.port || url.username || url.password) throw failure('protocol')
    const timeout = AbortSignal.timeout(options.httpTimeoutMs)
    const signal = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout
    try {
      const response = await fetcher(url.href, { method: 'GET', redirect: 'error', signal, headers: {
        cookie, 'user-agent': 'Mozilla/5.0 RaiseMyFish/0.5', origin: 'https://live.bilibili.com', referer: `https://live.bilibili.com/${options.room}`,
      } })
      if (response.status === 401 || response.status === 403) throw failure('auth')
      if (response.status === 429) throw failure('rate_limit')
      if (!response.ok) throw failure('network')
      const data = parseWireJSON(await responseText(response))
      if (data?.code === -101 || data?.code === -111) throw failure('auth')
      if (data?.code === -412 || data?.code === -509) throw failure('rate_limit')
      if (data?.code !== 0) throw failure('protocol')
      return data.data
    } catch (error) {
      if (options.signal?.aborted) throw failure('cancelled')
      if (timeout.aborted) throw failure('timeout')
      throw failure(SAFE_CODES.has(error?.code) ? error.code : 'network')
    }
  }
  try {
    const nav = await get('https://api.bilibili.com/x/web-interface/nav')
    if (nav?.isLogin !== true || decimalID(nav.mid) !== options.selfId) throw failure('auth')
    const roomInfo = await get(`https://api.live.bilibili.com/room/v1/Room/get_info?room_id=${options.room}`)
    const room = decimalID(roomInfo?.room_id)
    if (!room) throw failure('protocol')
    const keyOf = value => {
      if (typeof value !== 'string') throw failure('protocol')
      return value.slice(value.lastIndexOf('/') + 1, value.lastIndexOf('.'))
    }
    const raw = keyOf(nav.wbi_img?.img_url) + keyOf(nav.wbi_img?.sub_url)
    const dan = await get(`https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo?${wbiQuery(room, raw)}`)
    const hosts = dan?.host_list
    if (!Array.isArray(hosts) || !hosts.length || typeof dan.token !== 'string' || !dan.token || Buffer.byteLength(dan.token) > COOKIE_LIMIT) throw failure('protocol')
    const host = hosts[options.hostIndex % hosts.length]
    validateHost(host?.host, host?.wss_port)
    return { room, selfId: options.selfId, token: dan.token, host: host.host, port: host.wss_port }
  } catch (error) {
    if (options.signal?.aborted) return null
    throw error
  }
}

export function connectSession(options, session, deps = {}) {
  return new Promise((resolve, reject) => {
    let ws, connectTimer, idleTimer, beatTimer, settled = false, ready = false
    const listeners = []
    const finish = error => {
      if (settled) return
      settled = true
      clearTimeout(connectTimer); clearTimeout(idleTimer); clearInterval(beatTimer)
      options.signal?.removeEventListener('abort', cancel)
      for (const [name, callback] of listeners) ws?.removeEventListener(name, callback)
      try { ws?.close() } catch { /* 关闭失败不暴露底层异常。 */ }
      if (error) reject(failure(safeCode(error)))
      else resolve()
    }
    const cancel = () => finish()
    const emit = frame => {
      try {
        if ((deps.emit ?? (() => true))(frame) === false) throw failure('backpressure')
      } catch { throw failure('backpressure') }
    }
    const send = (op, body) => {
      try { ws.send(packet(op, body)) } catch { throw failure('network') }
    }
    const resetIdle = () => {
      clearTimeout(idleTimer)
      idleTimer = setTimeout(() => finish(failure('timeout')), options.idleMs)
    }
    const on = (name, callback) => { listeners.push([name, callback]); ws.addEventListener(name, callback) }
    try {
      validateOptions(options)
      if (!decimalID(session?.room) || session?.selfId !== options.selfId || typeof session?.token !== 'string' || !session.token || Buffer.byteLength(session.token) > COOKIE_LIMIT) throw failure('protocol')
      validateHost(session.host, session.port)
      if (options.signal?.aborted) { finish(); return }
      const WebSocket = deps.WebSocket ?? globalThis.WebSocket
      if (!WebSocket) throw failure('runtime')
      ws = new WebSocket(`wss://${session.host}:${session.port}/sub`)
      ws.binaryType = 'arraybuffer'
      const decoder = new PacketDecoder()
      options.signal?.addEventListener('abort', cancel, { once: true })
      connectTimer = setTimeout(() => finish(failure('timeout')), options.connectTimeoutMs)
      on('open', () => {
        try {
          // 数字字面直接拼入经过正十进制校验的 ID，避免 Number 丢精度。
          send(7, `{"uid":${session.selfId},"roomid":${session.room},"protover":3,"platform":"web","type":2,"key":${JSON.stringify(session.token)}}`)
        } catch (error) { finish(error) }
      })
      on('message', event => {
        if (settled) return
        try {
          const data = event.data
          if (!(data instanceof ArrayBuffer) && !ArrayBuffer.isView(data)) throw failure('protocol')
          const bytes = data instanceof ArrayBuffer ? Buffer.from(data) : Buffer.from(data.buffer, data.byteOffset, data.byteLength)
          for (const frame of decoder.push(bytes)) {
            if (frame.op === 8) {
              if (frame.data?.code !== 0) throw failure('auth')
              if (ready) throw failure('protocol')
              ready = true
              clearTimeout(connectTimer)
              resetIdle()
              emit({ protocol: 1, type: 'ready', room_id: session.room, self_id: session.selfId })
              send(2, '[object Object]')
              beatTimer = setInterval(() => {
                try { send(2, '[object Object]') } catch (error) { finish(error) }
              }, options.heartbeatMs)
            } else if (frame.op === 3) {
              if (!ready) throw failure('protocol')
              resetIdle()
              emit({ protocol: 1, type: 'heartbeat' })
            } else if (frame.op === 5) {
              if (!ready) throw failure('protocol')
              let message
              try { message = normalizeMessage(frame.data, session.room, session.selfId) }
              catch (error) {
                const code = safeCode(error)
                if (!['missing_message_id', 'missing_timestamp', 'invalid_message', 'protocol'].includes(code)) throw error
                emit({ ...errorFrame(code), fatal: false, retryable: false })
                continue
              }
              if (message) emit({ protocol: 1, type: 'event', ...message })
            }
          }
        } catch (error) { finish(error) }
      })
      on('error', () => finish(failure('network')))
      on('close', () => finish(failure('network')))
    } catch (error) { finish(error) }
  })
}

// Writable 自身队列也计算在上限中；不另建无界消息队列。
export function createOutput(stream, maxBytes = MAX_BYTES) {
  return frame => {
    const line = JSON.stringify(frame) + '\n'
    const size = Buffer.byteLength(line)
    if (size > maxBytes || stream.writableLength + size > maxBytes || stream.destroyed) return false
    stream.write(line)
    return true
  }
}

export function cliOptions(argv) {
  const names = new Map([
    ['room', 'room'], ['self-id', 'selfId'], ['http-timeout-ms', 'httpTimeoutMs'], ['connect-timeout-ms', 'connectTimeoutMs'],
    ['heartbeat-ms', 'heartbeatMs'], ['idle-ms', 'idleMs'], ['host-index', 'hostIndex'],
  ])
  const options = { httpTimeoutMs: 10000, connectTimeoutMs: 15000, heartbeatMs: 30000, idleMs: 70000, hostIndex: 0 }
  const seen = new Set()
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index]?.slice(2), key = names.get(name), value = argv[index + 1]
    if (!argv[index].startsWith('--') || !key || seen.has(key) || !value || value.startsWith('--')) throw failure('runtime')
    seen.add(key)
    if (key === 'room' || key === 'selfId') options[key] = value
    else {
      if (!/^[0-9]+$/.test(value)) throw failure('runtime')
      options[key] = Number(value)
    }
  }
  validateOptions(options)
  return options
}

async function main() {
  const emit = createOutput(process.stdout), controller = new AbortController()
  const cancel = () => controller.abort()
  process.once('SIGTERM', cancel); process.once('SIGINT', cancel)
  process.stdout.on('error', cancel)
  let code = null
  try {
    if (Number(process.versions.node.split('.')[0]) < 24) throw failure('runtime')
    const options = { ...cliOptions(process.argv.slice(2)), signal: controller.signal }
    const session = await bootstrap(options)
    if (session && !controller.signal.aborted) await connectSession(options, session, { emit })
  } catch (error) {
    if (!controller.signal.aborted) { code = safeCode(error); emit(errorFrame(code)) }
  } finally {
    process.removeListener('SIGTERM', cancel); process.removeListener('SIGINT', cancel)
  }
  process.exitCode = code === null ? 0 : ['auth', 'credentials'].includes(code) ? 10 : code === 'runtime' ? 2 : code === 'timeout' ? 12 : ['protocol', 'backpressure'].includes(code) ? 13 : 11
  // 父进程完全停止读管道时也必须退出；给最后一条错误帧有限排空时间。
  if (process.stdout.writableLength > 0) {
    const timer = setTimeout(() => process.exit(process.exitCode), 1000)
    process.stdout.once('drain', () => clearTimeout(timer))
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main()
