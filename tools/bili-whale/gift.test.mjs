import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { runGift } from './gift.mjs'

const cookie = 'SESSDATA=fixture-session; bili_jct=fixture-csrf; buvid3=fixture-buvid'
const base = ['--room', '99', '--gift-id', '31036', '--num', '1']
const giftID = '31036'
const panel = (items = [{ id: 31036, price: 100, bag_gift: 0 }]) => ({ code: 0, data: { list: items } })
function response(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => (typeof body === 'string' ? body : JSON.stringify(body)) }
}
// streamResponse 模拟真实 fetch 的 body.getReader 路径，避免只测到 text() 回退分支。
function streamResponse(body, status = 200) {
  const text = typeof body === 'string' ? body : JSON.stringify(body)
  const data = Buffer.from(text)
  let offset = 0
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => text,
    body: {
      getReader() {
        return {
          async read() {
            if (offset >= data.length) return { done: true, value: undefined }
            const value = data.subarray(offset, offset + 64 * 1024)
            offset += value.length
            return { done: false, value }
          },
          async cancel() {},
          releaseLock() {},
        }
      },
    },
  }
}
function preparation(overrides = {}) {
  const calls = []
  return {
    calls,
    env: { FISH_BILI_COOKIE: cookie },
    fetch: async (url, init) => {
      calls.push({ url, init })
      if (url.includes('/Room/get_info')) return response({ code: 0, data: { room_id: 99, uid: 8, short_id: 0, ...overrides.room } })
      if (url.includes('/giftConfig')) return response(overrides.panel ?? panel())
      if (url.includes('/nav')) return response({ code: 0, data: { isLogin: true, mid: 8, ...overrides.nav } })
      if (url.includes('/sendGoldMultiUser')) return response(overrides.post ?? { code: 0 })
      throw Error('unexpected-url')
    },
  }
}

test('默认dry-run不读凭据、不访问平台，只输出将发送的帧', async () => {
  let calls = 0
  const frame = await runGift(base, { env: {}, fetch: async () => { calls++; throw Error('must-not-fetch') } })
  assert.deepEqual(frame, { protocol: 1, type: 'sent', gift_id: giftID, num: 1 })
  assert.equal(calls, 0)
  assert.deepEqual(await runGift([...base, '--dry-run'], { env: {} }), frame)
})

test('参数严格：缺失/重复/未知/非法ID数量/双开关均拒绝，不接触平台', async () => {
  let calls = 0
  const deps = { env: { FISH_BILI_COOKIE: cookie }, fetch: async () => { calls++; throw Error('must-not-fetch') } }
  for (const args of [
    [], ['--room', '99', '--gift-id', giftID], ['--room', '99', '--num', '1'],
    ['--room', '0', '--gift-id', giftID, '--num', '1'],
    ['--room', '99', '--gift-id', '0', '--num', '1'],
    ['--room', '99', '--gift-id', '31036', '--num', '0'],
    ['--room', '99', '--gift-id', '31036', '--num', '101'],
    ['--room', '99', '--gift-id', '31036', '--num', '01'],
    [...base, '--unknown'], [...base, '--room', '100'], [...base, '--room'],
    [...base, '--dry-run', '--go'],
    [...base, '--max-price', '0'], [...base, '--max-price', '10001'],
    [...base, '--max-price', 'abc'], [...base, '--max-price', '01'], [...base, '--max-price'],
    [...base, '--max-price', '100', '--max-price', '100'],
    [...base, '--max-num', '0'], [...base, '--max-num', '101'],
    [...base, '--max-num', 'abc'], [...base, '--max-num'],
    ['--room', '99', '--gift-id', '31036', '--num', '2', '--max-num', '1'],
    [...base, '--go', '--max-price', '0'], [...base, '--go', '--max-num', '101'],
  ]) {
    const frame = await runGift(args, deps)
    assert.equal(frame.type, 'error', JSON.stringify(args))
    assert.equal(frame.kind, 'fatal', JSON.stringify(args))
    assert.equal(typeof frame.message, 'string')
    assert.ok(!frame.message.includes('fixture'))
  }
  assert.equal(calls, 0)
})

test('上限参数：默认100/100，边界值可接受且数量不得超过max-num', async () => {
  const frame = { protocol: 1, type: 'sent', gift_id: giftID, num: 1 }
  assert.deepEqual(await runGift([...base, '--max-price', '10000', '--max-num', '1'], { env: {} }), frame)
  assert.deepEqual(await runGift([...base, '--max-price', '100', '--max-num', '100'], { env: {} }), frame)
  const five = ['--room', '99', '--gift-id', giftID, '--num', '5', '--max-num', '5']
  assert.deepEqual(await runGift(five, { env: {} }), { protocol: 1, type: 'sent', gift_id: giftID, num: 5 })
  const hundred = ['--room', '99', '--gift-id', giftID, '--num', '100']
  assert.deepEqual(await runGift(hundred, { env: {} }), { protocol: 1, type: 'sent', gift_id: giftID, num: 100 })
})

test('自定义价格上限在真送前生效', async () => {
  const rejected = preparation()
  const frame = await runGift([...base, '--go', '--max-price', '50'], rejected)
  assert.equal(frame.type, 'error')
  assert.equal(frame.kind, 'fatal')
  assert.equal(frame.message, '礼物价格超过配置上限')
  assert.equal(rejected.calls.length, 2)
  assert.ok(rejected.calls.every(call => call.init.method === 'GET'))

  const accepted = preparation()
  assert.deepEqual(await runGift([...base, '--go', '--max-price', '100'], accepted), { protocol: 1, type: 'sent', gift_id: giftID, num: 1 })
  assert.equal(accepted.calls.length, 4)
})

test('礼物面板大响应（>1MiB）仍能正常解析并发送', async () => {
  const padding = 'x'.repeat(1024 * 1024 + 1024)
  const bigPanel = { code: 0, data: { list: [{ id: 31036, price: 100, bag_gift: 0 }], padding } }
  assert.ok(Buffer.byteLength(JSON.stringify(bigPanel)) > 1024 * 1024)
  for (const [name, makeResponse] of [['stream', streamResponse], ['text', response]]) {
    const deps = preparation()
    const fetch = deps.fetch
    deps.fetch = async (url, init) => {
      if (!url.includes('/giftConfig')) return fetch(url, init)
      deps.calls.push({ url, init })
      return makeResponse(bigPanel)
    }
    const frame = await runGift([...base, '--go'], deps)
    assert.deepEqual(frame, { protocol: 1, type: 'sent', gift_id: giftID, num: 1 }, name)
    assert.equal(deps.calls.length, 4, name)
  }
})

test('超过4MiB的响应仍按超限拒绝', async () => {
  const padding = 'x'.repeat(4 * 1024 * 1024 + 1024)
  const tooBig = { code: 0, data: { list: [{ id: 31036, price: 100, bag_gift: 0 }], padding } }
  const deps = preparation({ panel: tooBig })
  const frame = await runGift([...base, '--go'], deps)
  assert.equal(frame.type, 'error')
  assert.equal(frame.kind, 'retryable')
  assert.equal(frame.message, '网络响应无效')
  assert.equal(deps.calls.length, 2)
})

test('真送完整流程：面板数字ID、直购校验、表单与CSRF、单次POST', async () => {
  const deps = preparation()
  const frame = await runGift([...base, '--go'], deps)
  assert.deepEqual(frame, { protocol: 1, type: 'sent', gift_id: giftID, num: 1 })
  assert.equal(deps.calls.length, 4)
  for (const call of deps.calls.slice(0, 3)) {
    assert.equal(call.init.method, 'GET')
    assert.equal(call.init.redirect, 'error')
    assert.ok(call.init.signal)
    assert.equal(call.init.headers.cookie, cookie)
    assert.ok(!call.url.includes('fixture'))
  }
  const post = deps.calls[3]
  assert.equal(post.init.method, 'POST')
  assert.equal(post.init.redirect, 'error')
  assert.equal(post.url, 'https://api.live.bilibili.com/xlive/revenue/v2/gift/sendGoldMultiUser')
  assert.equal(post.init.headers['content-type'], 'application/x-www-form-urlencoded')
  const form = new URLSearchParams(post.init.body)
  for (const [key, value] of Object.entries({
    uid: '8', gift_id: giftID, ruid: '8', send_ruid: '0', gift_num: '1', coin_type: 'gold',
    bag_id: '0', platform: 'pc', biz_code: 'Live', biz_id: '99', price: '100', web_location: '444.8',
    csrf: 'fixture-csrf', csrf_token: 'fixture-csrf',
  })) assert.equal(form.get(key), value, key)
  assert.deepEqual(JSON.parse(form.get('receive_users')), [{ uid: 8 }])
  assert.ok(!post.init.body.includes('fixture-session'))
})

test('礼物必须是直购且价格不超过配置上限，否则不发POST', async () => {
  const cases = [
    { panel: panel([{ id: 31036, price: 101, bag_gift: 0 }]), message: '礼物价格超过配置上限' },
    { panel: panel([{ id: 31036, price: 100, bag_gift: 1 }]), message: '该礼物不是直购礼物' },
    { panel: panel([{ id: 31036, price: 100, bag_gift: null }]), message: '该礼物不是直购礼物' },
    { panel: panel([{ id: 31036, price: null, bag_gift: 0 }]), message: '礼物价格无效' },
    { panel: panel([{ id: 1, price: 100, bag_gift: 0 }]), message: '礼物ID不在当前房间面板' },
    { panel: { code: 0, data: { list: 'bad' } }, message: '礼物面板查询失败' },
  ]
  for (const item of cases) {
    const deps = preparation({ panel: item.panel })
    const frame = await runGift([...base, '--go'], deps)
    assert.equal(frame.type, 'error')
    assert.equal(frame.kind, 'fatal')
    assert.equal(frame.message, item.message)
    assert.equal(deps.calls.length, 2)
    assert.ok(deps.calls.every(call => call.init.method === 'GET'))
  }
  const stringIDs = preparation({ panel: panel([{ id: '31036', price: '100', bag_gift: '0' }]) })
  assert.equal((await runGift([...base, '--go'], stringIDs)).type, 'sent')
})

test('预检失败分类：网络/5xx可重试，鉴权与登录态必须fatal', async () => {
  const retryable = [
    { fetch: async () => { throw Error('offline') } },
    { fetch: async () => response({}, 500) },
    { fetch: async () => response('not-json') },
  ]
  for (const deps of retryable) {
    const frame = await runGift([...base, '--go'], { env: { FISH_BILI_COOKIE: cookie }, ...deps })
    assert.equal(frame.type, 'error')
    assert.equal(frame.kind, 'retryable')
  }
  const auth = preparation({ nav: { isLogin: false } })
  const frame = await runGift([...base, '--go'], auth)
  assert.equal(frame.kind, 'fatal')
  assert.equal(frame.message, '登录态无效')
  for (const status of [401, 403]) {
    const denied = await runGift([...base, '--go'], { env: { FISH_BILI_COOKIE: cookie }, fetch: async () => response({}, status) })
    assert.equal(denied.kind, 'fatal')
  }
})

test('POST之后任何失败都不得标记retryable，也不泄露平台报文', async () => {
  for (const deps of [
    { fetch: async () => { throw Error('credential-private-canary') } },
    { fetch: async () => response({ code: -500, message: 'private-canary' }) },
    { fetch: async () => response({ code: 10024, message: 'private-canary' }) },
    { fetch: async () => response({}, 500) },
    { fetch: async () => response('not-json') },
  ]) {
    const baseDeps = preparation()
    const frame = await runGift([...base, '--go'], {
      ...baseDeps,
      fetch: async (url, init) => (url.includes('/sendGoldMultiUser') ? deps.fetch(url, init) : baseDeps.fetch(url, init)),
    })
    assert.equal(frame.type, 'error')
    assert.equal(frame.kind, 'fatal')
    assert.ok(!JSON.stringify(frame).includes('canary'))
    assert.ok(!JSON.stringify(frame).includes('fixture'))
  }
})

test('凭据只认环境变量且必须包含SESSDATA/bili_jct/buvid3', async () => {
  for (const env of [{}, { FISH_BILI_COOKIE: 'SESSDATA=x' }, { FISH_BILI_COOKIE: 'SESSDATA=x; bili_jct=y' }]) {
    let calls = 0
    const frame = await runGift([...base, '--go'], { env, fetch: async () => { calls++; throw Error('must-not-fetch') } })
    assert.equal(frame.kind, 'fatal')
    assert.ok(frame.message.includes('凭据'))
    assert.equal(calls, 0)
  }
})

test('房间信息无效时不继续请求面板，错误信息有界且无控制字符', async () => {
  for (const room of [{ room_id: 0 }, { uid: 0 }, { room_id: '9'.repeat(17), uid: 8 }]) {
    const deps = preparation({ room })
    const frame = await runGift([...base, '--go'], deps)
    assert.equal(frame.kind, 'fatal')
    assert.equal(deps.calls.length, 1)
  }
  const frame = await runGift([...base, '--go'], { env: { FISH_BILI_COOKIE: cookie }, fetch: async () => { throw Error('外'.repeat(200)) } })
  assert.ok(frame.message.length <= 120)
  assert.ok(!/[\x00-\x1f\x7f]/.test(frame.message))
})

test('CLI导入无副作用；dry-run可离线；真送缺凭据失败且stderr为空', () => {
  const entry = new URL('./gift.mjs', import.meta.url)
  const imported = spawnSync(process.execPath, ['--input-type=module', '-e', `await import(${JSON.stringify(entry.href)})`], { encoding: 'utf8', timeout: 3000 })
  assert.equal(imported.status, 0)
  assert.equal(imported.stdout, '')
  assert.equal(imported.stderr, '')

  const env = { ...process.env }
  delete env.FISH_BILI_COOKIE
  delete env.FISH_BILI_COOKIE_FILE
  const dry = spawnSync(process.execPath, [fileURLToPath(entry), ...base], { env, encoding: 'utf8', timeout: 3000 })
  assert.equal(dry.status, 0)
  assert.equal(dry.stderr, '')
  assert.deepEqual(JSON.parse(dry.stdout.trim()), { protocol: 1, type: 'sent', gift_id: giftID, num: 1 })

  const real = spawnSync(process.execPath, [fileURLToPath(entry), ...base, '--go'], { env, encoding: 'utf8', timeout: 3000 })
  assert.equal(real.status, 2)
  assert.equal(real.stderr, '')
  const frame = JSON.parse(real.stdout.trim())
  assert.equal(frame.type, 'error')
  assert.equal(frame.kind, 'fatal')
  assert.ok(!real.stdout.includes('fixture'))

  const invalid = spawnSync(process.execPath, [fileURLToPath(entry), '--room', 'x'], { env, encoding: 'utf8', timeout: 3000 })
  assert.equal(invalid.status, 2)
  assert.equal(invalid.stderr, '')
  assert.equal(JSON.parse(invalid.stdout.trim()).type, 'error')
})
