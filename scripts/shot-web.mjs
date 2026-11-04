// 审查 Web 面板前端：截图三个页面 + 测试按钮（搜索/删除/筛选）
import { createRequire } from 'module'
const require = createRequire(import.meta.url)
const { chromium } = require('C:/Users/Claran/.dsh/profiles/desktop/node_modules/playwright')

;(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true })
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })

  // 1. 概览页
  await page.goto('http://127.0.0.1:8090/', { waitUntil: 'networkidle' })
  await page.waitForTimeout(800)
  await page.screenshot({ path: 'shots/index.png', fullPage: true })
  console.log('index 截图完成')

  // 2. 记忆页
  await page.goto('http://127.0.0.1:8090/memory', { waitUntil: 'networkidle' })
  await page.waitForTimeout(800)
  await page.screenshot({ path: 'shots/memory.png', fullPage: true })
  console.log('memory 截图完成')

  // 3. 活动页
  await page.goto('http://127.0.0.1:8090/activity', { waitUntil: 'networkidle' })
  await page.waitForTimeout(800)
  await page.screenshot({ path: 'shots/activity.png', fullPage: true })
  console.log('activity 截图完成')

  // 4. 按钮测试：记忆搜索
  await page.goto('http://127.0.0.1:8090/memory', { waitUntil: 'networkidle' })
  const searchInput = page.locator('input[type=text], input[placeholder*="搜索"]').first()
  if (await searchInput.count()) {
    await searchInput.fill('主人')
    await page.waitForTimeout(1200)
    await page.screenshot({ path: 'shots/memory-search.png', fullPage: true })
    console.log('搜索按钮测试完成')
  } else {
    console.log('未找到搜索框')
  }

  await browser.close()
})().catch((e) => {
  console.error('ERR', e.message)
  process.exit(1)
})
