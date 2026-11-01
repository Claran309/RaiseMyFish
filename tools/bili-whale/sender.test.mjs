import test from 'node:test'
import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { preflight, postOnce, verifyOnce } from './danmaku.mjs'

const cookie = 'SESSDATA=fixture-session; bili_jct=fixture-csrf; buvid3=fixture-buvid'
const request = { request_id: 'request-1', room_id: '99', self_id: '8', private: false, text: '本地模拟弹幕' }
const permit = () => ({ attempt_id: 'attempt-1', request_id: request.request_id, nonce: '1793520000', expires_at: new Date(Date.now() + 10000).toISOString() })
function response(body, status = 200) { return { ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(body) } }
function preparation(overrides = {}) {
  const calls = []
  return { calls, cookie, fetch: async (url, init) => {
    calls.push({ url, init })
    assert.equal(init.method, 'GET')
    assert.equal(init.redirect, 'error')
    assert.ok(init.signal)
    assert.ok(['api.bilibili.com', 'api.live.bilibili.com'].includes(new URL(url).hostname))
    return response(url.includes('/nav') ? { code: 0, data: { isLogin: true, mid: 8, ...overrides.nav } } : { code: 0, data: { room_id: 99, ...overrides.room } })
  } }
}

test('预检只读，账号与规范房间须匹配，不复用DSH gate/目录', async () => {
  const deps = preparation(), session = await preflight(request, deps)
  assert.equal(deps.calls.length, 2)
  assert.equal(session.room_id, '99')
  assert.equal(session.self_id, '8')
  for (const bad of [{ ...request, private: true }, { ...request, text: '鱼'.repeat(41) }, { ...request, room_id: [99] }, { ...request, self_id: [8] }]) await assert.rejects(preflight(bad, preparation()))
  await assert.rejects(preflight(request, preparation({ nav: { mid: 9 } })))
  await assert.rejects(preflight(request, preparation({ room: { room_id: 100 } })))
})

test('单次POST/表单/CSRF与带稳定ID回执核验', async () => {
  const session = await preflight(request, preparation()); const calls = []
  const result = await postOnce(request, session, permit(), { fetch: async (url, init) => {
    calls.push({ url, init }); assert.equal(url, 'https://api.live.bilibili.com/msg/send'); assert.equal(init.method, 'POST'); assert.equal(init.redirect, 'error')
    const form = new URLSearchParams(init.body); assert.equal(form.get('roomid'), '99'); assert.equal(form.get('msg'), request.text); assert.equal(form.get('csrf'), 'fixture-csrf'); assert.equal(form.get('csrf_token'), 'fixture-csrf'); assert.equal(form.get('rnd'), '1793520000')
    for (const [key, value] of Object.entries({ fontsize: '25', color: '16777215', mode: '1', bubble: '0', room_type: '0', jumpfrom: '0', statistics: '{"appId":100,"platform":5}' })) assert.equal(form.get(key), value)
    return response({ code: 0, data: { mode_info: { extra: JSON.stringify({ id_str: 'platform-msg-1' }) } } })
  } })
  assert.equal(calls.length, 1); assert.equal(result.outcome, 'accepted'); assert.equal(result.verified, true); assert.equal(result.msg_id, 'platform-msg-1')
  assert.ok(!JSON.stringify(result).includes('fixture-csrf')); assert.ok(!JSON.stringify(result).includes('fixture-session'))
})

test('code0但无ID不可冒充核验；网络/5xx未知，明确拒绝分类', async () => {
  const session = await preflight(request, preparation())
  const missing = await postOnce(request, session, permit(), { fetch: async () => response({ code: 0, data: {} }) })
  assert.equal(missing.outcome, 'accepted'); assert.equal(missing.verified, false)
  for (const deps of [{ fetch: async () => { throw new Error('credential-private-canary') } }, { fetch: async () => response({ code: 0, data: { id_str: 'not-proof' } }, 500) }]) {
    const result = await postOnce(request, session, permit(), deps); assert.equal(result.outcome, 'unknown'); assert.equal(result.verified, false); assert.ok(!JSON.stringify(result).includes('canary'))
  }
  for (const [code, kind] of [[10031, 'rate_limit'], [12006, 'content'], [12002, 'unavailable'], [-101, 'auth'], [-111, 'auth'], [1003212, 'invalid'], [-400, 'invalid']]) {
    const result = await postOnce(request, session, permit(), { fetch: async () => response({ code, message: 'private-canary' }) })
    assert.equal(result.outcome, 'rejected'); assert.equal(result.error_kind, kind); assert.equal(result.verified, false); assert.ok(!JSON.stringify(result).includes('private-canary'))
  }
})

test('许可失效/未获许可不POST，调用内部不重试', async () => {
  const session = await preflight(request, preparation()); let calls = 0
  const fetch = async () => { calls++; throw new Error('network') }
  for (const p of [null, { ...permit(), expires_at: '2000-01-01T00:00:00Z' }, { ...permit(), request_id: 'other' }]) {
    const result = await postOnce(request, session, p, { fetch }); assert.equal(result.outcome, 'not_posted')
  }
  assert.equal(calls, 0)
  await postOnce(request, session, permit(), { fetch }); assert.equal(calls, 1)
})

test('回读仅强证据，相同正文/时间不证明发送，绝不POST', async () => {
  const session = await preflight(request, preparation())
  const expected = { ...permit(), msg_id: 'known-msg' }
  const verify = async row => verifyOnce(request, session, expected, { fetch: async (url, init) => {
    assert.equal(init.method, 'GET'); assert.ok(url.includes('/gethistory?roomid=99')); return response({ code: 0, data: { room: [row] } })
  } })
  assert.equal((await verify({ uid: 8, text: request.text, id_str: 'known-msg', rnd: '1793520000' })).verified, true)
  for (const row of [{ uid: 9, text: request.text, id_str: 'known-msg' }, { uid: 8, text: '其他正文', id_str: 'known-msg' }, { uid: 8, text: request.text, timeline: '2026-11-01 16:00:00' }, { uid: [8], text: request.text, id_str: 'known-msg' }]) assert.equal((await verify(row)).verified, false)
  const byNonce = await verifyOnce(request, session, { ...expected, msg_id: '' }, { fetch: async () => response({ code: 0, data: { room: [{ uid: 8, text: request.text, id_str: 'nonce-msg', rnd: '1793520000' }] } }) })
  assert.equal(byNonce.verified, true); assert.equal(byNonce.msg_id, 'nonce-msg')
})

test('会话隐藏秘密并绑定完整请求，伪造/交换会话不得投递', async () => {
  const session = await preflight(request, preparation()); let calls = 0
  assert.ok(Object.isFrozen(session)); assert.ok(!JSON.stringify(session).includes('fixture'))
  const fetch = async () => { calls++; return response({ code: 0 }) }
  for (const changed of [{ ...request, text: '换正文' }, { ...request, request_id: 'changed' }, { ...request, room_id: '100' }, { ...request, self_id: '9' }]) {
    assert.equal((await postOnce(changed, session, permit(), { fetch })).outcome, 'not_posted')
  }
  assert.equal((await postOnce(request, { ...session }, permit(), { fetch })).outcome, 'not_posted')
  assert.equal(calls, 0)
})

test('严格标量与Unicode字数，Sender凭据必须齐全且账号预检严格', async () => {
  for (const bad of [true, [8], {}, '08', '0', 0, -8]) await assert.rejects(preflight({ ...request, self_id: bad }, preparation()))
  for (const text of ['', '鱼'.repeat(41), '\ud800']) await assert.rejects(preflight({ ...request, text }, preparation()))
  await preflight({ ...request, text: '🐟'.repeat(40) }, preparation())
  for (const nav of [{ isLogin: 'true' }, { mid: true }, { mid: [8] }]) await assert.rejects(preflight(request, preparation({ nav })))
  for (const cookie of ['SESSDATA=fake', 'SESSDATA=fake; bili_jct=x', 'SESSDATA=fake; bili_jct=x; bili_jct=y; buvid3=b']) {
    const deps = preparation(); deps.cookie = cookie; await assert.rejects(preflight(request, deps)); assert.equal(deps.calls.length, 0)
  }
})

test('取消、坏许可、异常响应及零ID均保守处理', async () => {
  const session = await preflight(request, preparation()); let calls = 0
  const fetch = async () => { calls++; return response({ code: 0 }) }
  for (const p of [{ ...permit(), attempt_id: '' }, { ...permit(), nonce: true }, { ...permit(), nonce: ['1'] }, { ...permit(), nonce: '0' }, { ...permit(), expires_at: null }]) {
    assert.equal((await postOnce(request, session, p, { fetch })).outcome, 'not_posted')
  }
  assert.equal((await postOnce(request, session, permit(), { fetch, signal: AbortSignal.abort() })).outcome, 'not_posted')
  assert.equal(calls, 0)
  for (const body of ['not json', 'x'.repeat(1024 * 1024 + 1)]) {
    assert.equal((await postOnce(request, session, permit(), { fetch: async () => ({ ok: true, status: 200, text: async () => body }) })).outcome, 'unknown')
  }
  for (const id of [0, '0', false, [1], {}]) {
    const result = await postOnce(request, session, permit(), { fetch: async () => response({ code: 0, data: { id_str: id } }) })
    assert.equal(result.outcome, 'accepted'); assert.equal(result.verified, false)
  }
  assert.equal((await postOnce(request, session, permit(), { fetch: async () => response({ code: 99999, message: 'secret' }) })).outcome, 'unknown')
})

test('核验不可用正文替代ID，已知ID不匹配时nonce也不补位', async () => {
  const session = await preflight(request, preparation())
  for (const row of [{ uid: 8, text: request.text, id_str: 'other', rnd: '1793520000' }, { uid: 8, text: request.text, id_str: ['known'] }, { uid: true, text: request.text, id_str: 'known' }]) {
    const result = await verifyOnce(request, session, { ...permit(), msg_id: 'known' }, { fetch: async () => response({ code: 0, data: { room: [row] } }) })
    assert.equal(result.verified, false)
  }
  for (const rnd of [true, ['1793520000'], 0]) {
    const result = await verifyOnce(request, session, { ...permit(), msg_id: '' }, { fetch: async () => response({ code: 0, data: { room: [{ uid: 8, text: request.text, id_str: 'found', rnd }] } }) })
    assert.equal(result.verified, false)
  }
})

test('CLI导入无副作用，默认不能发送，凭据错误不泄漏正文', () => {
  const entry = new URL('./danmaku.mjs', import.meta.url)
  const imported = spawnSync(process.execPath, ['--input-type=module', '-e', `await import(${JSON.stringify(entry.href)})`], { encoding: 'utf8', timeout: 3000 })
  assert.equal(imported.status, 0); assert.equal(imported.stdout, ''); assert.equal(imported.stderr, '')
  const env = { ...process.env, FISH_BILI_COOKIE: '', FISH_BILI_COOKIE_FILE: '' }
  for (const args of [[], ['--action', 'send'], ['--action', 'send', '--go']]) {
    const run = spawnSync(process.execPath, [fileURLToPath(entry), ...args], { env, input: JSON.stringify({ protocol: 1, type: 'prepare', ...request }) + '\n', encoding: 'utf8', timeout: 3000 })
    assert.notEqual(run.status, 0); assert.ok(!run.stdout.includes(request.text)); assert.ok(!run.stdout.includes('fixture')); assert.equal(run.stderr, '')
    const frame = JSON.parse(run.stdout.trim()); assert.equal(frame.protocol, 1); assert.equal(frame.type, 'result'); assert.equal(frame.result.outcome, 'not_posted')
  }
})

test('流响应大小/超时/取消有界，任何不确定结果仅投递一次', async () => {
  const session = await preflight(request, preparation()); let cancelled = false, calls = 0
  const oversize = await postOnce(request, session, permit(), { fetch: async () => ({ status: 200, ok: true, body: new ReadableStream({
    pull(controller) { controller.enqueue(new Uint8Array(1024 * 1024 + 1)) }, cancel() { cancelled = true },
  }) }) })
  assert.equal(oversize.outcome, 'unknown'); assert.equal(cancelled, true)
  const hold = setTimeout(() => {}, 2000)
  try {
    const timed = await postOnce(request, session, permit(), { httpTimeoutMs: 10, fetch: async (_url, init) => { calls++; assert.ok(init.signal); return new Promise(() => {}) } })
    assert.equal(timed.outcome, 'unknown'); assert.equal(calls, 1)
    const controller = new AbortController()
    const pending = postOnce(request, session, permit(), { signal: controller.signal, fetch: async () => { calls++; controller.abort(); throw new Error('credential-private-canary') } })
    assert.equal((await pending).outcome, 'unknown'); assert.equal(calls, 2)
  } finally { clearTimeout(hold) }
})

test('CLI拒绝无界/坏输入、EOF与命令行正文，不接触平台', () => {
  const entry = fileURLToPath(new URL('./danmaku.mjs', import.meta.url))
  const env = { ...process.env, FISH_BILI_COOKIE: '', FISH_BILI_COOKIE_FILE: '' }
  for (const [args, input] of [
    [['--action', 'send', '--go'], 'x'.repeat(16385) + '\n'],
    [['--action', 'send', '--go'], 'not-json\n'],
    [['--action', 'send', '--go'], ''],
    [['--action', 'send', '--go'], '{"protocol":1'],
    [['--action', 'send', '--go', '--text', 'secret'], ''],
  ]) {
    const run = spawnSync(process.execPath, [entry, ...args], { env, input, encoding: 'utf8', timeout: 3000 })
    assert.notEqual(run.status, 0); assert.equal(run.stderr, '')
    const frame = JSON.parse(run.stdout.trim()); assert.equal(frame.type, 'result'); assert.equal(frame.result.outcome, 'not_posted')
  }
})

test('CLI等待stdin时EOF及时退出且不投递', async () => {
  const child = spawn(process.execPath, [fileURLToPath(new URL('./danmaku.mjs', import.meta.url)), '--action', 'send', '--go'], { env: { ...process.env, FISH_BILI_COOKIE: '', FISH_BILI_COOKIE_FILE: '' }, stdio: ['pipe', 'pipe', 'pipe'] })
  let output = '', errors = ''
  child.stdout.on('data', bytes => { output += bytes }); child.stderr.on('data', bytes => { errors += bytes })
  const timer = setTimeout(() => child.stdin.end(), 30)
  const status = await new Promise(resolve => child.on('close', resolve))
  clearTimeout(timer)
  assert.notEqual(status, 0); assert.equal(errors, ''); assert.equal(JSON.parse(output.trim()).result.outcome, 'not_posted')
})

test('CLI合成两阶段握手：ready之前零POST，commit之后只一次POST', async () => {
  const entry = fileURLToPath(new URL('./danmaku.mjs', import.meta.url))
  // 在子进程中直接注入fetch，所有URL均由本机替身处理，不使用HTTP代理/真实平台。
  const script = `
    process.argv = [process.execPath, ${JSON.stringify(entry)}, '--action', 'send', '--go'];
    let posts = 0;
    globalThis.fetch = async (url, init) => {
      if (init.method === 'POST') {
        posts++;
        if (posts !== 1 || url !== 'https://api.live.bilibili.com/msg/send') throw Error('bad-post');
        return new Response(JSON.stringify({code:0,data:{id_str:'cli-message'}}));
      }
      return new Response(JSON.stringify(url.includes('/nav') ? {code:0,data:{isLogin:true,mid:8}} : {code:0,data:{room_id:99}}));
    };
    await import(${JSON.stringify(new URL('./danmaku.mjs', import.meta.url).href)});
    if (posts !== 1) process.exitCode = 9;
  `
  const child = spawn(process.execPath, ['--input-type=module', '-e', script], { env: { ...process.env, FISH_BILI_COOKIE: cookie, FISH_BILI_COOKIE_FILE: '' }, stdio: ['pipe', 'pipe', 'pipe'] })
  const frames = []; let pending = '', errors = ''
  child.stdout.on('data', bytes => {
    pending += bytes
    let newline
    while ((newline = pending.indexOf('\n')) >= 0) {
      const frame = JSON.parse(pending.slice(0, newline)); pending = pending.slice(newline + 1); frames.push(frame)
      if (frame.type === 'ready') child.stdin.write(JSON.stringify({ protocol: 1, type: 'commit', ...permit() }) + '\n')
    }
  })
  child.stderr.on('data', bytes => { errors += bytes })
  child.stdin.write(JSON.stringify({ protocol: 1, type: 'prepare', ...request }) + '\n')
  const timer = setTimeout(() => child.kill(), 3000)
  const status = await new Promise(resolve => child.on('close', resolve)); clearTimeout(timer)
  assert.equal(status, 0); assert.equal(errors, ''); assert.equal(frames.length, 2)
  assert.equal(frames[0].type, 'ready'); assert.equal(frames[1].type, 'result'); assert.equal(frames[1].request_id, request.request_id); assert.equal(frames[1].attempt_id, 'attempt-1')
  assert.equal(frames[1].result.outcome, 'accepted'); assert.equal(frames[1].result.msg_id, 'cli-message'); assert.equal(frames[1].result.verified, true)
  assert.ok(!JSON.stringify(frames).includes('fixture')); assert.ok(!JSON.stringify(frames).includes(request.text))
})

test('CLI核验缺凭据仍为unknown，保留原尝试关联', () => {
  const run = spawnSync(process.execPath, [fileURLToPath(new URL('./danmaku.mjs', import.meta.url)), '--action', 'verify'], {
    env: { ...process.env, FISH_BILI_COOKIE: '', FISH_BILI_COOKIE_FILE: '' },
    input: JSON.stringify({ protocol: 1, type: 'verify', ...request, ...permit(), msg_id: 'previous-id' }) + '\n', encoding: 'utf8', timeout: 3000,
  })
  assert.notEqual(run.status, 0); assert.equal(run.stderr, '')
  const frame = JSON.parse(run.stdout.trim())
  assert.equal(frame.result.outcome, 'unknown'); assert.equal(frame.attempt_id, 'attempt-1'); assert.equal(frame.request_id, request.request_id)
  assert.equal(frame.result.verified, false); assert.ok(!run.stdout.includes(request.text))
})

for (const mode of ['failure', 'cancel']) test(`CLI核验预检${mode}仍为unknown且保留attempt_id`, () => {
  const entry = new URL('./danmaku.mjs', import.meta.url)
  const script = `
    process.argv = [process.execPath, ${JSON.stringify(fileURLToPath(entry))}, '--action', 'verify'];
    let calls = 0;
    globalThis.fetch = async (_url, init) => {
      calls++;
      if (init.method !== 'GET') throw Error('unexpected-post');
      if (${JSON.stringify(mode)} === 'cancel') process.emit('SIGTERM');
      throw Error('secret-credential-canary');
    };
    await import(${JSON.stringify(entry.href)});
    if (calls !== 1) process.exitCode = 9;
  `
  const run = spawnSync(process.execPath, ['--input-type=module', '-e', script], {
    env: { ...process.env, FISH_BILI_COOKIE: cookie, FISH_BILI_COOKIE_FILE: '' },
    input: JSON.stringify({ protocol: 1, type: 'verify', ...request, ...permit(), msg_id: 'previous-id' }) + '\n', encoding: 'utf8', timeout: 3000,
  })
  assert.equal(run.status, 1); assert.equal(run.stderr, '')
  const frame = JSON.parse(run.stdout.trim())
  assert.equal(frame.result.outcome, 'unknown'); assert.equal(frame.attempt_id, 'attempt-1'); assert.equal(frame.request_id, request.request_id)
  assert.equal(frame.result.verified, false); assert.ok(!run.stdout.includes('canary')); assert.ok(!run.stdout.includes('fixture'))
})
