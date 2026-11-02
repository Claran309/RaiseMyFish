import { DatabaseSync } from 'node:sqlite'
import { homedir } from 'node:os'
import path from 'node:path'

const DB = path.join(homedir(), '.dsh', 'meme-wiki', 'memes.db')
const db = new DatabaseSync(DB)
const rows = db.prepare('SELECT title, usage, summary, tags FROM memes UNION ALL SELECT title, usage, summary, tags FROM custom').all()
let emptyTitle = 0, emptyUsage = 0, emptyTags = 0
const samples = []
for (const r of rows) {
  if (!r.title || !String(r.title).trim()) emptyTitle++
  if (!r.usage || !String(r.usage).trim()) { emptyUsage++; if (samples.length < 5) samples.push({ title: r.title, usage: r.usage, summary: r.summary, tags: r.tags }) }
  if (!r.tags || !String(r.tags).trim()) emptyTags++
}
console.log('emptyTitle', emptyTitle, 'emptyUsage', emptyUsage, 'emptyTags', emptyTags)
console.log(JSON.stringify(samples, null, 1))
