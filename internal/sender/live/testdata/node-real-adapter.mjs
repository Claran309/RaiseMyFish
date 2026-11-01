// 运行真正的移植入口，仅将HTTP替换为本机合成函数；不读取真实凭据或连接网络。
import { fileURLToPath } from 'node:url'
const target = new URL('../../../../tools/bili-whale/danmaku.mjs', import.meta.url)
process.argv[1] = fileURLToPath(target)
let posts = 0
globalThis.fetch = async (url, init) => {
  if (url === 'https://api.bilibili.com/x/web-interface/nav') return new Response(JSON.stringify({ code: 0, data: { isLogin: true, mid: 8 } }))
  if (url.startsWith('https://api.live.bilibili.com/room/v1/Room/get_info?room_id=123')) return new Response(JSON.stringify({ code: 0, data: { room_id: 123 } }))
  if (url === 'https://api.live.bilibili.com/msg/send' && init.method === 'POST') {
    if (++posts !== 1) throw new Error('fixture_duplicate_post')
    const form = new URLSearchParams(init.body)
    if (form.get('roomid') !== '123' || form.get('msg') !== '合成发送文本' || form.get('csrf') !== 'syntheticcsrf') throw new Error('fixture_bad_form')
    return new Response(JSON.stringify({ code: 0, data: { mode_info: { extra: JSON.stringify({ id_str: 'real_adapter_receipt' }) } } }))
  }
  throw new Error('fixture_unknown_target')
}
await import(target.href)
if (posts !== 1) process.exitCode = 9
