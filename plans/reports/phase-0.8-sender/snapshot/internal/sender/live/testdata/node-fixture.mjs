// 纯合成JSONL协议进程；不导入平台模块、不读取凭据、不发起网络请求。
import readline from 'node:readline'

export async function runFixture(mode) {
  const argv = process.argv.slice(2)
  const send = argv.length === 3 && argv[0] === '--action' && argv[1] === 'send' && argv[2] === '--go'
  const verify = argv.length === 2 && argv[0] === '--action' && argv[1] === 'verify'
  if (!send && !verify) process.exit(2)
  const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity })
  const iterator = lines[Symbol.asyncIterator]()
  const emit = frame => process.stdout.write(`${JSON.stringify({ protocol: 1, ...frame })}\n`)
  try {
    const firstLine = await iterator.next()
    if (firstLine.done) process.exit(3)
    const first = JSON.parse(firstLine.value)
    if (first.protocol !== 1 || first.type !== (send ? 'prepare' : 'verify') || first.private !== false) process.exit(4)
    if (mode === 'preflight-auth') {
      emit({ type: 'result', request_id: first.request_id, attempt_id: verify ? first.attempt_id : '', result: { outcome: verify ? 'unknown' : 'not_posted', msg_id: '', verified: false, error_kind: 'auth', evidence: 'none' } })
      return
    }
    if (mode === 'hang') {
      await new Promise(() => {})
    }
    let attemptID = first.attempt_id
    if (send) {
      emit({ type: 'ready', request_id: first.request_id, room_id: first.room_id, self_id: first.self_id })
      const next = await iterator.next()
      if (next.done) process.exit(5)
      const commit = JSON.parse(next.value)
      if (commit.protocol !== 1 || commit.type !== 'commit' || commit.request_id !== first.request_id || commit.attempt_id !== first.request_id || !/^[1-9][0-9]*$/.test(commit.nonce)) process.exit(6)
      attemptID = commit.attempt_id
    }
    let result
    if (mode === 'unknown') result = { outcome: 'unknown', msg_id: '', verified: false, error_kind: 'network', evidence: 'none' }
    else if (mode === 'rejected' && send) result = { outcome: 'rejected', msg_id: '', verified: false, error_kind: 'rate_limit', evidence: 'none' }
    else if (mode === 'unverified' && send) result = { outcome: 'accepted', msg_id: '', verified: false, error_kind: '', evidence: 'none' }
    else result = { outcome: 'accepted', msg_id: verify && first.msg_id ? first.msg_id : 'fixture_receipt_1', verified: true, error_kind: '', evidence: send ? 'receipt_id' : first.msg_id ? 'history_id' : 'history_nonce' }
    emit({ type: 'result', request_id: first.request_id, attempt_id: attemptID, result })
  } finally {
    lines.close()
    process.stdin.pause()
  }
}
