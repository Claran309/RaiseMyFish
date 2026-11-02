// 基于 dsh-bili-whale@0.4.5 的协议思路移植，许可证与出处见本目录。
import { inflateSync, brotliDecompressSync } from 'node:zlib'
import { createHash } from 'node:crypto'

export const MAX_BYTES = 1024 * 1024
const MIXIN_TAB = [46,47,18,2,53,8,23,32,15,50,10,31,58,3,45,35,27,43,5,49,33,9,42,19,29,28,14,39,12,38,41,13,37,48,7,16,24,55,40,61,26,17,0,1,60,51,30,4,22,25,54,21,56,59,6,63,57,62,11,36,20,34,44,52]

// 错误只含固定代码，不拼接平台报文或底层异常。
export function failure(code) { return Object.assign(new Error(code), { code }) }

export function decimalID(value) {
  if (typeof value !== 'string' && typeof value !== 'number') return null
  if (typeof value === 'number' && !Number.isSafeInteger(value)) return null
  const text = String(value ?? '')
  return /^[1-9][0-9]{0,127}$/.test(text) ? text : null
}

export function parseWireJSON(text) {
  try {
    if (typeof text !== 'string' || Buffer.byteLength(text) > MAX_BYTES) throw failure('protocol')
    return JSON.parse(text, (_key, value, context) => {
      if (typeof value === 'number' && Number.isInteger(value) && !Number.isSafeInteger(value)) {
        if (!/^-?[0-9]+$/.test(context.source)) throw failure('protocol')
        return context.source
      }
      return value
    })
  } catch { throw failure('protocol') }
}

export function packet(op, body = '', version = 1) {
  const bytes = typeof body === 'string' ? Buffer.from(body) : Buffer.from(body)
  if (bytes.length + 16 > MAX_BYTES) throw failure('protocol')
  const result = Buffer.alloc(16 + bytes.length)
  result.writeUInt32BE(result.length, 0)
  result.writeUInt16BE(16, 4)
  result.writeUInt16BE(version, 6)
  result.writeUInt32BE(op, 8)
  result.writeUInt32BE(1, 12)
  bytes.copy(result, 16)
  return result
}

export class PacketDecoder {
  #pending = Buffer.alloc(0)

  push(input) {
    const bytes = Buffer.from(input)
    if (this.#pending.length + bytes.length > MAX_BYTES) throw failure('protocol')
    const joined = Buffer.concat([this.#pending, bytes])
    const frames = [], budget = { packets: 0, expanded: 0 }
    const consumed = this.#decode(joined, 0, frames, budget, true)
    this.#pending = Buffer.from(joined.subarray(consumed))
    return frames
  }

  #decode(bytes, depth, frames, budget, partial) {
    if (depth > 4) throw failure('protocol')
    let offset = 0
    while (offset + 16 <= bytes.length) {
      const length = bytes.readUInt32BE(offset), header = bytes.readUInt16BE(offset + 4)
      const version = bytes.readUInt16BE(offset + 6), op = bytes.readUInt32BE(offset + 8)
      if (length < 16 || length > MAX_BYTES || header !== 16 || header > length) throw failure('protocol')
      if (offset + length > bytes.length) break
      if (++budget.packets > 256) throw failure('protocol')
      const body = bytes.subarray(offset + header, offset + length)
      if (version === 2 || version === 3) {
        if (depth >= 4) throw failure('protocol')
        let expanded
        try { expanded = (version === 2 ? inflateSync : brotliDecompressSync)(body, { maxOutputLength: MAX_BYTES - budget.expanded }) }
        catch { throw failure('protocol') }
        budget.expanded += expanded.length
        if (budget.expanded > MAX_BYTES) throw failure('protocol')
        this.#decode(expanded, depth + 1, frames, budget, false)
      } else {
        if (version !== 0 && version !== 1) throw failure('protocol')
        if (op === 5 || op === 8) frames.push({ op, data: parseWireJSON(body.toString('utf8')) })
        else if (op === 3) {
          if (body.length !== 4) throw failure('protocol')
          frames.push({ op, popularity: body.readUInt32BE(0) })
        } else frames.push({ op })
      }
      offset += length
    }
    if (!partial && offset !== bytes.length) throw failure('protocol')
    return offset
  }
}

function stableID(value) {
  if (typeof value !== 'string' && typeof value !== 'number') return null
  if (typeof value === 'number' && !Number.isSafeInteger(value)) return null
  return /^[A-Za-z0-9_-]{1,128}$/.test(String(value ?? '')) ? String(value) : null
}

// B站回复/@仍是DANMU_MSG，正文未必带「@名字」；回复目标在 extra 的结构化字段里。
// 只提取可信的目标，异常值按「没有回复目标」处理，不影响弹幕本身入库。
function replyTarget(extra) {
  if (!extra || typeof extra !== 'object') return {}
  const target = {}
  const id = decimalID(extra.reply_mid)
  if (id) target.reply_to_id = id
  let name = extra.reply_uname
  if (typeof name !== 'string' || name.trim() === '') name = extra.reply_uinfo?.base?.name
  if (typeof name !== 'string') return target
  name = name.trim()
  if (name === '' || [...name].length > 128 || /[\u0000-\u001f\u007f]/.test(name) || !name.isWellFormed()) return target
  target.reply_to_name = name
  return target
}

export function normalizeMessage(message, room, selfId) {
  if (!/^DANMU_MSG(?::|$)/.test(String(message?.cmd ?? ''))) return null
  const info = message.info
  const uid = decimalID(info?.[2]?.[0])
  if (!uid || !decimalID(room) || !decimalID(selfId)) throw failure('invalid_message')
  if (uid === selfId) return null
  let extra = info?.[0]?.[15]?.extra
  if (typeof extra === 'string') extra = parseWireJSON(extra)
  const candidates = [message.id_str, message.dmid, message.data?.id_str, message.data?.dmid, extra?.id_str, extra?.dmid, info?.[0]?.[15]?.id_str, info?.[0]?.[15]?.dmid]
  const id = candidates.map(stableID).find(Boolean)
  if (!id) throw failure('missing_message_id')
  const rawTime = info?.[0]?.[4]
  if (typeof rawTime !== 'string' && typeof rawTime !== 'number') throw failure('missing_timestamp')
  if (typeof rawTime === 'number' && !Number.isSafeInteger(rawTime)) throw failure('missing_timestamp')
  if (!/^[0-9]{10,13}$/.test(String(rawTime ?? ''))) throw failure('missing_timestamp')
  const timestamp = Number(rawTime) * (String(rawTime).length <= 10 ? 1000 : 1)
  const date = new Date(timestamp)
  if (!Number.isSafeInteger(timestamp) || !Number.isFinite(date.getTime())) throw failure('missing_timestamp')
  if (typeof info?.[1] !== 'string' || typeof info?.[2]?.[1] !== 'string') throw failure('invalid_message')
  return { room_id: room, msg_id: id, from_id: uid, from_name: info[2][1], text: info[1], at: date.toISOString(), ...replyTarget(extra) }
}

export function wbiQuery(room, raw, seconds = Math.round(Date.now() / 1000)) {
  if (!decimalID(room) || typeof raw !== 'string' || !Number.isSafeInteger(seconds)) throw failure('protocol')
  const mixin = MIXIN_TAB.map(index => raw[index] ?? '').join('').slice(0, 32)
  const fields = { id: room, type: 0, wts: seconds }
  const query = Object.keys(fields).sort().map(key => `${encodeURIComponent(key)}=${encodeURIComponent(String(fields[key]).replace(/[!'()*]/g, ''))}`).join('&')
  return `${query}&w_rid=${createHash('md5').update(query + mixin).digest('hex')}`
}
