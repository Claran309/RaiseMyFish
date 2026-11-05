import { createRequire } from 'module'
const require = createRequire(import.meta.url)
const { chromium } = require('C:/Users/Claran/.dsh/profiles/desktop/node_modules/playwright')

;(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true })
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  const pages = [
    ['live', '/live'], ['message', '/message'], ['config', '/config'],
    ['logs', '/logs'], ['activity', '/activity'],
  ]
  for (const [name, path] of pages) {
    await page.goto('http://127.0.0.1:8090' + path, { waitUntil: 'networkidle', timeout: 30000 })
    await page.waitForTimeout(1200)
    await page.screenshot({ path: 'shots/' + name + '.png', fullPage: true })
    console.log(name + ' 截图完成')
  }
  await browser.close()
})().catch((e) => { console.error('ERR', e.message); process.exit(1) })
