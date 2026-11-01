import test from 'node:test'
import assert from 'node:assert/strict'
import zlib from 'node:zlib'
import { PacketDecoder, packet, normalizeMessage, parseWireJSON, wbiQuery, decimalID } from './protocol.mjs'
import { connectSession, bootstrap, credentialHeader, cliOptions, createOutput } from './danmaku-await.mjs'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { Writable } from 'node:stream'
import { spawnSync } from 'node:child_process'

const info = (id = 'platform-id-123', uid = '7', text = '你好鱼鱼') => {
  const header = Array(16).fill(0)
  header[4] = 1793520000000
  header[5] = 777 // nonce绝不能拿来充当稳定ID
  header[15] = { extra: JSON.stringify({ id_str: id }) }
  return { cmd: 'DANMU_MSG:4:0:2:2:2:0', info: [header, text, [uid, '观众']] }
}

test('平台稳定ID/时间与大整数保留，不回放nonce或拼接内容ID', () => {
  const msg = normalizeMessage(info(), '99', '8')
  assert.equal(msg.msg_id, 'platform-id-123')
  assert.equal(msg.room_id, '99')
  assert.equal(msg.from_id, '7')
  assert.equal(msg.at, '2026-11-01T08:00:00.000Z')
  const source = JSON.stringify(info()).replace('"platform-id-123"', '900719925474099312345')
  // 直接验证JSON数字字面，不允许IEEE754静默取整。
  assert.equal(parseWireJSON('{"id_str":900719925474099312345}').id_str, '900719925474099312345')
  assert.ok(source.length > 0)
  const missing = info(); missing.info[0][15] = {}
  assert.throws(() => normalizeMessage(missing, '99', '8'), { code: 'missing_message_id' })
  assert.equal(normalizeMessage(info('same', '8'), '99', '8'), null)
  assert.equal(normalizeMessage(info(), '99', '8').msg_id, normalizeMessage(info(), '99', '8').msg_id)
})

test('分帧/粘包、zlib/brotli与长度上限', () => {
  const inner = packet(5, JSON.stringify(info()))
  for (const [version, compress] of [[2, zlib.deflateSync], [3, zlib.brotliCompressSync]]) {
    const decoder = new PacketDecoder()
    const compressed = packet(5, compress(inner), version)
    assert.deepEqual(decoder.push(compressed.subarray(0, 9)), [])
    const frames = decoder.push(Buffer.concat([compressed.subarray(9), packet(3, Buffer.alloc(4))]))
    assert.equal(frames.length, 2)
    assert.equal(frames[0].data.cmd, info().cmd)
    assert.equal(frames[1].op, 3)
  }
  const bad = Buffer.alloc(16); bad.writeUInt32BE(8)
  assert.throws(() => new PacketDecoder().push(bad), { code: 'protocol' })
  assert.throws(() => new PacketDecoder().push(Buffer.alloc(2 * 1024 * 1024)), { code: 'protocol' })
  assert.throws(() => normalizeMessage(info('bad:id'), '99', '8'), { code: 'missing_message_id' })
})

test('WBI签名不丢大房间号，固定排序并有摘要', () => {
  const query = new URLSearchParams(wbiQuery('9007199254740993123', 'a'.repeat(64), 123))
  assert.equal(query.get('id'), '9007199254740993123')
  assert.equal(query.get('type'), '0')
  assert.match(query.get('w_rid'), /^[a-f0-9]{32}$/)
})

const options = { room: '1', selfId: '8', httpTimeoutMs: 1000, connectTimeoutMs: 5000, heartbeatMs: 30000, idleMs: 60000, hostIndex: 0 }
const nav = { code: 0, data: { isLogin: true, mid: 8, wbi_img: { img_url: 'https://i0.hdslb.com/a.png', sub_url: 'https://i0.hdslb.com/b.png' } } }
const responses = [nav, { code: 0, data: { room_id: 99 } }, { code: 0, data: { token: 'secret-token-canary', host_list: [{ host: 'test.chat.bilibili.com', wss_port: 443 }] } }]
function fakeFetch() {
  let index = 0
  return async (url, init) => {
    assert.equal(new URL(url).protocol, 'https:')
    assert.ok(['api.bilibili.com', 'api.live.bilibili.com'].includes(new URL(url).hostname))
    assert.equal(init.headers.cookie, 'SESSDATA=credential-canary')
    assert.ok(init.signal)
    return { ok: true, status: 200, text: async () => JSON.stringify(responses[index++]) }
  }
}

test('显式凭据、自账号校验与固定平台目标，无自动DSH配置', async () => {
  assert.equal(credentialHeader({ FISH_BILI_COOKIE: 'SESSDATA=credential-canary' }), 'SESSDATA=credential-canary')
  assert.throws(() => credentialHeader({}), { code: 'credentials' })
  assert.throws(() => credentialHeader({ FISH_BILI_COOKIE: 'SESSDATA=foo\r\nx:bar' }), { code: 'credentials' })
  const result = await bootstrap(options, { fetch: fakeFetch(), cookie: 'SESSDATA=credential-canary' })
  assert.equal(result.room, '99')
  assert.equal(result.selfId, '8')
  await assert.rejects(bootstrap({ ...options, selfId: '9' }, { fetch: fakeFetch(), cookie: 'SESSDATA=credential-canary' }), { code: 'auth' })
})

class FakeWebSocket extends EventTarget {
  static instance
  constructor(url) { super(); this.url = url; this.sent = []; this.closed = false; FakeWebSocket.instance = this }
  send(data) { this.sent.push(Buffer.from(data)) }
  close() { this.closed = true; this.dispatchEvent(new Event('close')) }
  receive(data) { this.dispatchEvent(new MessageEvent('message', { data })) }
}

test('WS常驻无25秒轮换；认证后ready，所有观众消息输出且重复交Bus去重', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'setInterval'] })
  const controller = new AbortController(), output = []
  const promise = connectSession({ ...options, signal: controller.signal }, { room: '99', selfId: '8', token: 'secret-token-canary', host: 'test.chat.bilibili.com', port: 443 }, { WebSocket: FakeWebSocket, emit: frame => { output.push(frame); return true } })
  const ws = FakeWebSocket.instance
  assert.match(ws.url, /^wss:\/\/test\.chat\.bilibili\.com:443\/sub$/)
  ws.dispatchEvent(new Event('open'))
  assert.equal(ws.sent[0].readUInt32BE(8), 7)
  ws.receive(packet(8, '{"code":0}'))
  ws.receive(packet(5, JSON.stringify(info())))
  ws.receive(packet(5, JSON.stringify(info())))
  ws.receive(packet(5, JSON.stringify(info('self', '8'))))
  assert.equal(output.filter(f => f.type === 'ready').length, 1)
  assert.equal(output.filter(f => f.type === 'event').length, 2)
  assert.ok(!JSON.stringify(output).includes('secret-token-canary'))
  t.mock.timers.tick(26000)
  assert.equal(ws.closed, false)
  controller.abort()
  await promise
  assert.equal(ws.closed, true)
})

test('鉴权失败暂停，不输出服务端原始异常/凭据', async () => {
  const controller = new AbortController(), output = []
  const promise = connectSession({ ...options, signal: controller.signal }, { room: '99', selfId: '8', token: 'secret-token-canary', host: 'test.chat.bilibili.com', port: 443 }, { WebSocket: FakeWebSocket, emit: f => { output.push(f); return true } })
  const ws = FakeWebSocket.instance; ws.dispatchEvent(new Event('open')); ws.receive(packet(8, '{"code":-101,"message":"private-cookie-canary"}'))
  await assert.rejects(promise, { code: 'auth' })
  assert.ok(!JSON.stringify(output).includes('private-cookie-canary'))
})

test('ID仅接受显式字符串或精确整数，不接受布尔/数组或nonce', () => {
  for (const id of [false, ['platform-id'], 'x'.repeat(129), 'bad:id']) {
    assert.throws(() => normalizeMessage(info(id), '99', '8'), { code: 'missing_message_id' })
  }
  const missing = info(); delete missing.info[0][15]
  assert.throws(() => normalizeMessage(missing, '99', '8'), { code: 'missing_message_id' })
  const top = info(); top.id_str = 'abcdef123'; delete top.info[0][15]
  assert.equal(normalizeMessage(top, '99', '8').msg_id, 'abcdef123')
  const numeric = parseWireJSON('{"cmd":"DANMU_MSG","id_str":900719925474099312345,"info":[[0,0,0,0,1793520000],"内容",[900719925474099312346,"观众"]]}')
  assert.equal(normalizeMessage(numeric, '99', '8').from_id, '900719925474099312346')
  assert.equal(normalizeMessage(numeric, '99', '8').msg_id, '900719925474099312345')
  assert.equal(normalizeMessage(numeric, '99', '8').at, '2026-11-01T08:00:00.000Z')
  for (const time of [undefined, null, 0, 'private-canary', -1]) {
    const message = info(); message.info[0][4] = time
    assert.throws(() => normalizeMessage(message, '99', '8'), { code: 'missing_timestamp' })
  }
  assert.equal(normalizeMessage({ cmd: 'SUPER_CHAT_MESSAGE' }, '99', '8'), null)
})

test('解析器限制递归深度、包数、解压量且错误不回显payload', () => {
  const secret = 'private-payload-canary'
  assert.throws(() => parseWireJSON(secret), error => error.code === 'protocol' && !error.message.includes(secret))
  let nested = packet(5, JSON.stringify(info()))
  for (let depth = 0; depth < 5; depth++) nested = packet(5, zlib.deflateSync(nested), 2)
  assert.throws(() => new PacketDecoder().push(nested), { code: 'protocol' })
  const bomb = packet(5, zlib.deflateSync(Buffer.alloc(1024 * 1024 + 1)), 2)
  assert.throws(() => new PacketDecoder().push(bomb), { code: 'protocol' })
  const tooMany = Buffer.concat(Array(257).fill(packet(3, Buffer.alloc(4))))
  assert.throws(() => new PacketDecoder().push(tooMany), { code: 'protocol' })
  const badHeader = packet(3, Buffer.alloc(4)); badHeader.writeUInt16BE(32, 4)
  assert.throws(() => new PacketDecoder().push(badHeader), { code: 'protocol' })
  const truncated = packet(5, zlib.deflateSync(packet(3, Buffer.alloc(4)).subarray(0, 19)), 2)
  assert.throws(() => new PacketDecoder().push(truncated), { code: 'protocol' })
  const decoder = new PacketDecoder(), original = packet(5, JSON.stringify(info()))
  for (let index = 0; index < original.length - 1; index++) assert.deepEqual(decoder.push(original.subarray(index, index + 1)), [])
  assert.equal(decoder.push(original.subarray(-1))[0].data.cmd, info().cmd)
})

test('显式凭据文件支持Cookie和旧JSON并限制大小，不回显读文件异常', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'fish-cookie-test-'))
  const file = path.join(dir, 'credential.txt')
  try {
    fs.writeFileSync(file, 'SESSDATA=test; bili_jct=csrf\n')
    assert.equal(credentialHeader({ FISH_BILI_COOKIE_FILE: file }), 'SESSDATA=test; bili_jct=csrf')
    fs.writeFileSync(file, JSON.stringify({ sessdata: 'test', bili_jct: 'csrf', buvid3: 'device' }))
    assert.equal(credentialHeader({ FISH_BILI_COOKIE_FILE: file }), 'SESSDATA=test; bili_jct=csrf; buvid3=device')
    assert.equal(credentialHeader({ FISH_BILI_COOKIE: 'SESSDATA=explicit', FISH_BILI_COOKIE_FILE: 'not-a-file' }), 'SESSDATA=explicit')
    fs.writeFileSync(file, 'SESSDATA=' + 'x'.repeat(64 * 1024))
    assert.throws(() => credentialHeader({ FISH_BILI_COOKIE_FILE: file }), { code: 'credentials' })
    fs.writeFileSync(file, JSON.stringify({ sessdata: 'private-canary; injected=yes' }))
    assert.throws(() => credentialHeader({ FISH_BILI_COOKIE_FILE: file }), error => error.code === 'credentials' && !error.message.includes('private-canary'))
    assert.throws(() => credentialHeader({ FISH_BILI_COOKIE_FILE: path.join(dir, 'private-canary') }), error => error.code === 'credentials' && !error.message.includes('private-canary'))
  } finally { fs.rmSync(dir, { recursive: true }) }
})

test('bootstrap拒绝未知WS目标、HTTP失败并取消响应体超限', async () => {
  const run = (changes = {}) => {
    let index = 0
    return bootstrap(options, { cookie: 'SESSDATA=credential-canary', fetch: async () => {
      const value = responses[index++]
      return { ok: true, status: 200, text: async () => JSON.stringify(index === 3 ? { ...value, data: { ...value.data, ...changes } } : value) }
    } })
  }
  await assert.rejects(run({ host_list: [{ host: 'test.chat.bilibili.com.attacker.invalid', wss_port: 443 }] }), { code: 'protocol' })
  await assert.rejects(run({ host_list: [{ host: '127.0.0.1', wss_port: 443 }] }), { code: 'protocol' })
  for (const [status, code] of [[401, 'auth'], [429, 'rate_limit'], [500, 'network']]) {
    await assert.rejects(bootstrap(options, { cookie: 'SESSDATA=credential-canary', fetch: async () => ({ ok: false, status }) }), { code })
  }
  await assert.rejects(bootstrap(options, { cookie: 'SESSDATA=credential-canary', fetch: async () => { throw new Error('private-network-canary') } }), error => error.code === 'network' && !error.message.includes('private-network-canary'))
  let cancelled = false
  await assert.rejects(bootstrap(options, { cookie: 'SESSDATA=credential-canary', fetch: async () => ({ ok: true, status: 200, body: { getReader: () => ({ read: async () => ({ done: false, value: Buffer.alloc(1024 * 1024 + 1) }), cancel: async () => { cancelled = true }, releaseLock() {} }) } }) }), { code: 'protocol' })
  assert.equal(cancelled, true)
})

const session = { room: '99', selfId: '8', token: 'secret-token-canary', host: 'test.chat.bilibili.com', port: 443 }
test('WS握手与op3存活超时、错误和关闭均结束单次连接', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'setInterval'] })
  let promise = connectSession(options, session, { WebSocket: FakeWebSocket })
  let rejected = assert.rejects(promise, { code: 'timeout' })
  t.mock.timers.tick(options.connectTimeoutMs)
  await rejected
  assert.equal(FakeWebSocket.instance.closed, true)
  const output = []
  promise = connectSession(options, session, { WebSocket: FakeWebSocket, emit: value => { output.push(value); return true } })
  let ws = FakeWebSocket.instance
  ws.dispatchEvent(new Event('open')); ws.receive(packet(8, '{"code":0}'))
  t.mock.timers.tick(30000)
  assert.equal(output.filter(value => value.type === 'heartbeat').length, 0)
  ws.receive(packet(3, Buffer.alloc(4)))
  assert.equal(output.filter(value => value.type === 'heartbeat').length, 1)
  t.mock.timers.tick(59000)
  assert.equal(ws.closed, false)
  rejected = assert.rejects(promise, { code: 'timeout' })
  t.mock.timers.tick(1000); await rejected
  for (const event of ['close', 'error']) {
    promise = connectSession(options, session, { WebSocket: FakeWebSocket })
    rejected = assert.rejects(promise, { code: 'network' })
    ws = FakeWebSocket.instance; ws.dispatchEvent(new Event(event))
    await rejected
  }
})

test('单条缺ID/时间安全报错并继续；认证前消息与背压终止', async () => {
  const controller = new AbortController(), output = []
  let promise = connectSession({ ...options, signal: controller.signal }, session, { WebSocket: FakeWebSocket, emit: frame => { output.push(frame); return true } })
  let ws = FakeWebSocket.instance
  ws.dispatchEvent(new Event('open')); ws.receive(packet(8, '{"code":0}'))
  const noId = info(); noId.info[0][15] = {}
  const noTime = info(); noTime.info[0][4] = null
  for (const message of [noId, noTime, info()]) ws.receive(packet(5, JSON.stringify(message)))
  assert.deepEqual(output.filter(frame => frame.type === 'error').map(frame => frame.kind), ['missing_message_id', 'missing_timestamp'])
  assert.equal(output.filter(frame => frame.type === 'event').length, 1)
  controller.abort(); await promise
  promise = connectSession(options, session, { WebSocket: FakeWebSocket })
  let rejected = assert.rejects(promise, { code: 'protocol' })
  ws = FakeWebSocket.instance; ws.receive(packet(5, JSON.stringify(info())))
  await rejected
  promise = connectSession(options, session, { WebSocket: FakeWebSocket, emit: () => false })
  rejected = assert.rejects(promise, { code: 'backpressure' })
  ws = FakeWebSocket.instance; ws.receive(packet(8, '{"code":0}'))
  await rejected
})

test('stdout写入队列有字节上限且CLI不接受Cookie或未知参数', () => {
  const chunks = []
  const blocked = new Writable({ write(chunk, _encoding, _callback) { chunks.push(chunk) } })
  const emit = createOutput(blocked, 100)
  assert.equal(emit({ protocol: 1, type: 'heartbeat' }), true)
  assert.equal(emit({ protocol: 1, type: 'heartbeat' }), true)
  assert.equal(emit({ protocol: 1, type: 'heartbeat' }), false)
  assert.ok(blocked.writableLength <= 100)
  blocked.destroy()
  assert.equal(cliOptions(['--room', '1', '--self-id', '8']).heartbeatMs, 30000)
  for (const argv of [[], ['--room', '1', '--self-id', '8', '--cookie', 'secret'], ['--room', '1', '--self-id', '8', '--idle-ms', '-1'], ['--room', '1', '--self-id', '8', '--host-index', 'x']]) {
    assert.throws(() => cliOptions(argv), { code: 'runtime' })
  }
})

test('模块导入无凭据/网络/输出副作用，CLI缺凭据安全退出10', () => {
  const script = path.join(import.meta.dirname, 'danmaku-await.mjs')
  const imported = spawnSync(process.execPath, ['--input-type=module', '-e', `await import(${JSON.stringify(new URL('./danmaku-await.mjs', import.meta.url).href)})`], { env: {}, encoding: 'utf8', timeout: 5000 })
  assert.equal(imported.status, 0)
  assert.equal(imported.stdout, '')
  assert.equal(imported.stderr, '')
  const missing = spawnSync(process.execPath, [script, '--room', '1', '--self-id', '8'], { env: {}, encoding: 'utf8', timeout: 5000 })
  assert.equal(missing.status, 10)
  assert.equal(missing.stderr, '')
  assert.deepEqual(JSON.parse(missing.stdout), { protocol: 1, type: 'error', kind: 'credentials', fatal: true, retryable: false })
})

test('正十进制ID拒绝数组、嵌套数组、布尔、null和对象', () => {
  for (const value of [[7], [[7]], true, false, null, {}, { toString: () => '7' }]) {
    assert.equal(decimalID(value), null)
    const message = info(); message.info[2][0] = value
    assert.throws(() => normalizeMessage(message, '99', '8'), { code: 'invalid_message' })
    assert.throws(() => normalizeMessage(info(), value, '8'), { code: 'invalid_message' })
  }
  assert.equal(decimalID(7), '7')
  assert.equal(decimalID('900719925474099312345'), '900719925474099312345')
  assert.equal(decimalID(Number.MAX_SAFE_INTEGER + 1), null)
})

test('bootstrap拒绝nav UID及规范房间ID的非标量类型', async () => {
  for (const value of [[8], [[8]], true, false, null, {}]) {
    let calls = 0
    await assert.rejects(bootstrap(options, { cookie: 'SESSDATA=credential-canary', fetch: async () => {
      calls++
      return { ok: true, status: 200, text: async () => JSON.stringify({ ...nav, data: { ...nav.data, mid: value } }) }
    } }), { code: 'auth' })
    assert.equal(calls, 1)
  }
  for (const value of [[99], [[99]], true, false, null, {}]) {
    let calls = 0
    await assert.rejects(bootstrap(options, { cookie: 'SESSDATA=credential-canary', fetch: async () => {
      calls++
      return { ok: true, status: 200, text: async () => JSON.stringify(calls === 1 ? nav : { code: 0, data: { room_id: value } }) }
    } }), { code: 'protocol' })
    assert.equal(calls, 2)
  }
})

test('平台原始时间仅接受字符串或安全整数，拒绝容器与其他类型', () => {
  for (const value of [[1793520000], [[1793520000]], null, true, false, {}, { toString: () => '1793520000' }, Number.MAX_SAFE_INTEGER + 1]) {
    const message = info(); message.info[0][4] = value
    assert.throws(() => normalizeMessage(message, '99', '8'), { code: 'missing_timestamp' })
  }
  for (const value of ['1793520000', 1793520000, '1793520000000', 1793520000000]) {
    const message = info(); message.info[0][4] = value
    assert.equal(normalizeMessage(message, '99', '8').at, '2026-11-01T08:00:00.000Z')
  }
})
