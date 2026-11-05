import { createRequire } from 'module'
const require = createRequire(import.meta.url)
const { chromium } = require('C:/Users/Claran/.dsh/profiles/desktop/node_modules/playwright')

;(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true })
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  await page.goto('http://127.0.0.1:8090/knowledge', { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  await page.screenshot({ path: 'shots/knowledge.png', fullPage: true })
  console.log('knowledge 截图完成')
  await page.goto('http://127.0.0.1:8090/', { waitUntil: 'networkidle' })
  await page.waitForTimeout(1200)
  await page.screenshot({ path: 'shots/index2.png', fullPage: true })
  console.log('index 截图完成')
  await browser.close()
})().catch((e) => { console.error('ERR', e.message); process.exit(1) })
