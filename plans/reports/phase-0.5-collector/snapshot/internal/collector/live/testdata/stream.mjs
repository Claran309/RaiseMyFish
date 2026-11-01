// 合成协议子进程，只给集成测试使用，不连接任何网络或读取本机凭据。
if (process.env.FISH_DB_DSN || process.env.FISH_LLM_KEY || process.env.NODE_OPTIONS) process.exit(20)
const emit = frame => process.stdout.write(JSON.stringify({ protocol: 1, ...frame }) + '\n')
emit({ type: 'ready', room_id: '99', self_id: '8' })
emit({ type: 'event', room_id: '99', msg_id: 'self', from_id: '8', from_name: '鱼', text: '自己', at: '2026-11-01T08:00:00Z' })
for (let i = 0; i < 2; i++) emit({ type: 'event', room_id: '99', msg_id: '9007199254740993123', from_id: '7', from_name: '观众', text: '合成普通消息', at: '2026-11-01T08:00:00Z' })
setInterval(() => emit({ type: 'heartbeat' }), 100)
