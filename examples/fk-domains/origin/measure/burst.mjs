// Why did keyboard.type with a delay make only 2 requests? Three ways to type
// "foldkit", requests counted, nothing measured between the keys.
//   node burst.mjs <base url>
import { createRequire } from 'node:module'
const require = createRequire('/tmp/opencode/fk-domains/package.json')
const { chromium } = require('@playwright/test')
const base = process.argv[2] ?? 'http://127.0.0.1:5391'
const sleep = ms => new Promise(r => setTimeout(r, ms))
const browser = await chromium.launch({ headless: true })
const ways = {
  'press, then 300 ms': async page => {
    for (const ch of 'foldkit') {
      await page.keyboard.press(ch)
      await sleep(300)
    }
  },
  'type, key held 250 ms': async page => {
    await page.keyboard.type('foldkit', { delay: 250 })
  },
  'press, then 60 ms': async page => {
    for (const ch of 'foldkit') {
      await page.keyboard.press(ch)
      await sleep(60)
    }
  },
  'fill each prefix, then 300 ms': async page => {
    const input = page.getByRole('searchbox', { name: 'Search domains' })
    for (let i = 1; i <= 7; i++) {
      await input.fill('foldkit'.slice(0, i))
      await sleep(300)
    }
  },
}
try {
  for (const [name, way] of Object.entries(ways)) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 720 } })
    const page = await context.newPage()
    const rpc = []
    const downs = []
    page.on('request', r => {
      if (new URL(r.url()).pathname.startsWith('/rpc')) {
        const p = JSON.parse(r.postData()).payload
        rpc.push(`${p.label}@${p.offset}+${p.limit}`)
      }
    })
    await page.addInitScript(() => {
      window.__inputs = []
      document.addEventListener('input', () => window.__inputs.push(Math.round(performance.now())), true)
    })
    await page.goto(base + '/', { waitUntil: 'load' })
    await page.waitForFunction(() => document.querySelectorAll('[data-foldkit-app]').length === 0, null, { timeout: 60000 })
    await sleep(500)
    await page.getByRole('searchbox', { name: 'Search domains' }).focus()
    const t0 = Date.now()
    await way(page)
    const took = Date.now() - t0
    await sleep(1500)
    const inputs = await page.evaluate(() => window.__inputs)
    const gaps = inputs.slice(1).map((t, i) => t - inputs[i])
    console.log(`${name}: ${took} ms; input-event gaps ${gaps.join(',')} ms; ${rpc.length} requests: ${rpc.join(' | ')}; url ${page.url()}`)
    await context.close()
  }
} finally {
  await browser.close()
}
