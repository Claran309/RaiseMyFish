import { DatabaseSync } from 'node:sqlite'
import { createHash } from 'node:crypto'
import { homedir } from 'node:os'
import path from 'node:path'

const db = new DatabaseSync(path.join(homedir(), '.dsh', 'meme-wiki', 'memes.db'))
const rows = db.prepare('SELECT title, usage, summary, tags FROM memes UNION ALL SELECT title, usage, summary, tags FROM custom').all()
for (const r of rows) {
  const id = 'meme-batch-' + createHash('sha1').update(r.title + '|' + (r.usage || '')).digest('hex').slice(0, 12)
  if (id === 'meme-batch-c6ef06d0a6c9') {
    console.log(JSON.stringify({ title: r.title, usage: r.usage, tags: r.tags }))
    const dup = rows.filter(x => x.title === r.title)
    console.log('同 title 条数:', dup.length)
  }
}
