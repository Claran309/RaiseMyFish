// 把 DSH 梗库（~/.dsh/meme-wiki/memes.db，6836 条）转换导入 RaiseMyFish K0 格式
// 输出：personas/dafeiyu/knowledge/memes/batch-*.md（每文件 < 1MiB）
import { DatabaseSync } from 'node:sqlite'
import { mkdirSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { homedir } from 'node:os'
import path from 'node:path'

const DB = path.join(homedir(), '.dsh', 'meme-wiki', 'memes.db')
const OUT_DIR = path.join('personas', 'dafeiyu', 'knowledge', 'memes')
mkdirSync(OUT_DIR, { recursive: true })

const db = new DatabaseSync(DB)
const allRows = db.prepare('SELECT title, usage, summary, tags FROM memes UNION ALL SELECT title, usage, summary, tags FROM custom').all()
// 纯符号标题（如「？」「！」）Recall 无法用原词召回自己；跳过这类条目
const hasWord = s => /[A-Za-z0-9\u4e00-\u9fff]/.test(s || '')
const rows = allRows.filter(r => hasWord(r.title))
console.log('总计', allRows.length, '条，可导入', rows.length, '条（跳过纯符号标题', allRows.length - rows.length, '条）')

const CHANNELS = '["live", "comment", "dm", "qq_private", "qq_group"]'
const BATCH = 600 // 每批条数（按字节控制：每批约 900KB < 1MiB）

function entry(r, i) {
  const id = 'meme-batch-' + createHash('sha1').update(r.title + '|' + (r.usage || '')).digest('hex').slice(0, 12)
  const tags = (r.tags || '').split(',').map(s => s.trim()).filter(Boolean)
  const tagYaml = tags.length ? '["' + tags.join('", "').replace(/"/g, '\\"') + '"]' : '[]'
  const clean = s => (s || '').replace(/\r?\n/g, ' ').replace(/"/g, '\\"')
  // usage 是必需字段：DSH 有 42 条 usage 为空，用 summary 前 80 字兜底；都空则跳过（上层过滤）
  let usage = clean(r.usage)
  if (!usage.trim()) usage = clean((r.summary || '').slice(0, 80))
  return [
    '  - id: "' + id + '"',
    '    title: "' + clean(r.title) + '"',
    '    channel: ' + CHANNELS,
    '    keywords: []',
    '    tags: ' + tagYaml,
    '    priority: 50',
    '    visibility: "public"',
    '    source: "meme-sqlite:dsh"',
    '    summary: "' + clean(r.summary) + '"',
    '    usage: "' + usage + '"',
  ].join('\n')
}

let file = 0
for (let start = 0; start < rows.length; start += BATCH) {
  file++
  const chunk = rows.slice(start, start + BATCH)
  const body = '---\nschema: fish-knowledge/v1\nentries:\n' + chunk.map((r, i) => entry(r, i)).join('\n') + '\n---\n'
  const name = path.join(OUT_DIR, 'batch-' + String(file).padStart(3, '0') + '.md')
  writeFileSync(name, body, 'utf8')
  const bytes = Buffer.byteLength(body, 'utf8')
  console.log(name, (bytes / 1024).toFixed(0) + 'KB', chunk.length, '条')
}
console.log('完成', file, '个文件')
