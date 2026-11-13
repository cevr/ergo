// Measures the original foldkit-domains app (plan.md section 20) in headless
// Chromium through Playwright and the Chrome DevTools Protocol.
//
//   node measure.mjs <variant> <base url> <phase,...>
//
// variant: a name for the output file (native, k1, k10).
// phases:  load, type, scroll, sort, heap (default: all).
// Output:  results-<variant>.json beside this file.
//
// It reads counters, not a stopwatch, where a counter exists: requests,
// bytes, DOM nodes, DOM mutations, heap bytes after a forced collection.
// The CDP durations (ScriptDuration, TaskDuration, LayoutDuration) are
// CPU-side times of the renderer's main thread; the host's load is in the
// output. Playwright comes from the clone's node_modules.
import { createRequire } from 'node:module'
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/tmp/opencode/fk-domains/package.json')
const { chromium } = require('@playwright/test')

const here = dirname(fileURLToPath(import.meta.url))
const variant = process.argv[2] ?? 'native'
const base = process.argv[3] ?? 'http://127.0.0.1:5391'
const phases = (process.argv[4] ?? 'load,type,scroll,sort,heap').split(',')
const viewport = { width: 1280, height: 720 }

const initScript = () => {
  const m = { records: 0, added: 0, removed: 0, attrs: 0, text: 0 }
  window.__m = m
  const count = n => (n.nodeType === 1 ? 1 + n.querySelectorAll('*').length : 1)
  const start = () => {
    new MutationObserver(list => {
      for (const r of list) {
        m.records++
        if (r.type === 'childList') {
          for (const n of r.addedNodes) m.added += count(n)
          for (const n of r.removedNodes) m.removed += count(n)
        } else if (r.type === 'attributes') m.attrs++
        else m.text++
      }
    }).observe(document, { subtree: true, childList: true, attributes: true, characterData: true })
  }
  start()
  let seen = false
  const t = setInterval(() => {
    const marked = document.querySelector('[data-foldkit-app]')
    if (marked) seen = true
    if (seen && !marked) {
      window.__hydratedAt = performance.now()
      clearInterval(t)
    }
  }, 1)
}

const loadavg = () => readFileSync('/proc/loadavg', 'utf8').trim()
const sleep = ms => new Promise(r => setTimeout(r, ms))
const median = xs => {
  const s = [...xs].sort((a, b) => a - b)
  return s.length ? s[Math.floor(s.length / 2)] : null
}
const sum = xs => xs.reduce((a, b) => a + b, 0)

async function open(browser, path, { throttle = 1 } = {}) {
  const context = await browser.newContext({ viewport, deviceScaleFactor: 1 })
  const page = await context.newPage()
  await page.addInitScript(initScript)
  const cdp = await context.newCDPSession(page)
  // threadTicks: the durations are CPU time of the renderer's main thread,
  // not wall time, so a loaded host changes them little.
  await cdp.send('Performance.enable', { timeDomain: 'threadTicks' })
  await cdp.send('HeapProfiler.enable')
  if (throttle !== 1) await cdp.send('Emulation.setCPUThrottlingRate', { rate: throttle })
  const rpc = []
  page.on('request', r => {
    if (new URL(r.url()).pathname.startsWith('/rpc')) {
      const entry = { at: Date.now(), body: r.postData(), state: 'pending', bytes: 0 }
      rpc.push(entry)
      r.__entry = entry
    }
  })
  page.on('requestfinished', async r => {
    if (r.__entry) {
      r.__entry.state = 'done'
      try {
        const s = await r.sizes()
        r.__entry.bytes = s.responseBodySize
        r.__entry.sent = s.requestBodySize
      } catch {}
    }
  })
  page.on('requestfailed', r => {
    if (r.__entry) r.__entry.state = `failed: ${r.failure()?.errorText}`
  })
  const errors = []
  page.on('pageerror', e => errors.push(e.message))
  const metrics = async () => {
    const { metrics } = await cdp.send('Performance.getMetrics')
    const o = {}
    for (const { name, value } of metrics) o[name] = value
    return o
  }
  const muts = () => page.evaluate(() => ({ ...window.__m }))
  const gc = async () => {
    await cdp.send('HeapProfiler.collectGarbage')
    await cdp.send('HeapProfiler.collectGarbage')
  }
  const dom = () =>
    page.evaluate(() => ({
      elements: document.querySelectorAll('*').length,
      rows: document.querySelectorAll('#domain-results .domain-row').length,
      items: document.querySelectorAll('[data-virtual-list-item-key]').length,
      cards: document.querySelectorAll('.domain-card').length,
      height: document.documentElement.scrollHeight,
      scrollY: window.scrollY,
    }))
  if (path !== null) {
    await page.goto(base + path, { waitUntil: 'load' })
    await page.waitForFunction(() => window.__hydratedAt !== undefined, null, { timeout: 60000 })
  }
  return { context, page, cdp, rpc, errors, metrics, muts, gc, dom }
}

const countKeys = ['LayoutCount', 'RecalcStyleCount']
const delta = (a, b, keys) => ({
  ...Object.fromEntries(keys.map(k => [k, +(1000 * (b[k] - a[k])).toFixed(3)])),
  ...Object.fromEntries(countKeys.map(k => [k, (b[k] ?? 0) - (a[k] ?? 0)])),
})
const cpuKeys = ['ScriptDuration', 'TaskDuration', 'LayoutDuration', 'RecalcStyleDuration']
const mdelta = (a, b) => Object.fromEntries(Object.keys(a).map(k => [k, b[k] - a[k]]))

async function settle(page, rpc, quietMs = 400, maxMs = 30000) {
  const end = Date.now() + maxMs
  let last = -1
  let since = Date.now()
  while (Date.now() < end) {
    const pending = rpc.filter(r => r.state === 'pending').length
    const n = rpc.length
    if (pending === 0 && n === last) {
      if (Date.now() - since >= quietMs) return
    } else {
      last = n
      since = Date.now()
    }
    await sleep(25)
  }
}

async function loadPhase(browser, path, throttle) {
  const runs = []
  for (let i = 0; i < 5; i++) {
    const s = await open(browser, null, { throttle })
    await s.page.goto(base + path, { waitUntil: 'load' })
    await s.page.waitForFunction(() => window.__hydratedAt !== undefined, null, { timeout: 120000 })
    const atHydration = await s.metrics()
    await settle(s.page, s.rpc)
    const t = await s.page.evaluate(() => {
      const nav = performance.getEntriesByType('navigation')[0]
      const paint = Object.fromEntries(performance.getEntriesByType('paint').map(p => [p.name, p.startTime]))
      const res = performance.getEntriesByType('resource').map(r => ({
        name: new URL(r.name).pathname,
        type: r.initiatorType,
        transfer: r.transferSize,
        encoded: r.encodedBodySize,
        decoded: r.decodedBodySize,
      }))
      return {
        responseEnd: nav.responseEnd,
        domContentLoaded: nav.domContentLoadedEventEnd,
        load: nav.loadEventEnd,
        html: { transfer: nav.transferSize, encoded: nav.encodedBodySize, decoded: nav.decodedBodySize },
        firstPaint: paint['first-paint'],
        firstContentfulPaint: paint['first-contentful-paint'],
        hydratedAt: window.__hydratedAt,
        res,
      }
    })
    const settled = await s.metrics()
    await s.gc()
    const afterGc = await s.metrics()
    runs.push({
      ...t,
      cpuToHydrationMs: delta({ ScriptDuration: 0, TaskDuration: 0, LayoutDuration: 0, RecalcStyleDuration: 0 }, atHydration, cpuKeys),
      heapAtHydration: atHydration.JSHeapUsedSize,
      heapSettledAfterGc: afterGc.JSHeapUsedSize,
      nodesSettled: settled.Nodes,
      listenersSettled: settled.JSEventListeners,
      dom: await s.dom(),
      rpc: s.rpc.map(r => ({ body: r.body, state: r.state, bytes: r.bytes })),
      errors: s.errors,
    })
    await s.context.close()
  }
  const js = runs[0].res.filter(r => r.name.endsWith('.js'))
  const css = runs[0].res.filter(r => r.name.endsWith('.css'))
  return {
    path,
    throttle,
    median: {
      firstPaint: median(runs.map(r => r.firstPaint)),
      firstContentfulPaint: median(runs.map(r => r.firstContentfulPaint)),
      hydratedAt: median(runs.map(r => r.hydratedAt)),
      scriptToHydrationMs: median(runs.map(r => r.cpuToHydrationMs.ScriptDuration)),
      taskToHydrationMs: median(runs.map(r => r.cpuToHydrationMs.TaskDuration)),
      heapSettledAfterGc: median(runs.map(r => r.heapSettledAfterGc)),
      nodesSettled: median(runs.map(r => r.nodesSettled)),
      elements: median(runs.map(r => r.dom.elements)),
    },
    bytes: {
      html: runs[0].html,
      js: { files: js.length, transfer: sum(js.map(r => r.transfer)), encoded: sum(js.map(r => r.encoded)), decoded: sum(js.map(r => r.decoded)) },
      css: { files: css.length, transfer: sum(css.map(r => r.transfer)), encoded: sum(css.map(r => r.encoded)), decoded: sum(css.map(r => r.decoded)) },
      all: { files: runs[0].res.length, transfer: sum(runs[0].res.map(r => r.transfer)), decoded: sum(runs[0].res.map(r => r.decoded)) },
    },
    runs,
  }
}

async function typePhase(browser, text, gapMs) {
  const s = await open(browser, '/')
  await settle(s.page, s.rpc)
  const input = s.page.getByRole('searchbox', { name: 'Search domains' })
  await input.focus()
  const keys = []
  const press = async key => {
    const m0 = await s.metrics()
    const u0 = await s.muts()
    const n0 = s.rpc.length
    await s.page.keyboard.press(key)
    await sleep(gapMs)
    const m1 = await s.metrics()
    const u1 = await s.muts()
    keys.push({ key, cpuMs: delta(m0, m1, cpuKeys), mutations: mdelta(u0, u1), requestsStarted: s.rpc.length - n0 })
  }
  for (const ch of text) await press(ch)
  await settle(s.page, s.rpc)
  const afterTyping = s.rpc.length
  const typed = s.rpc.map(r => ({ body: r.body, state: r.state, bytes: r.bytes }))
  // Cache script: three backspaces and the same three letters again, slower
  // than the debounce, inside the one-minute freshness.
  const before = s.rpc.length
  for (let i = 0; i < 3; i++) {
    await s.page.keyboard.press('Backspace')
    await sleep(400)
  }
  for (const ch of text.slice(-3)) {
    await s.page.keyboard.press(ch)
    await sleep(400)
  }
  await settle(s.page, s.rpc)
  const result = {
    text,
    gapMs,
    keys,
    perKeyMedian: {
      scriptMs: median(keys.map(k => k.cpuMs.ScriptDuration)),
      taskMs: median(keys.map(k => k.cpuMs.TaskDuration)),
      layoutMs: median(keys.map(k => k.cpuMs.LayoutDuration)),
      mutationRecords: median(keys.map(k => k.mutations.records)),
      nodesAdded: median(keys.map(k => k.mutations.added)),
      nodesRemoved: median(keys.map(k => k.mutations.removed)),
      textChanges: median(keys.map(k => k.mutations.text)),
      attrChanges: median(keys.map(k => k.mutations.attrs)),
    },
    requests: {
      total: afterTyping,
      done: typed.filter(r => r.state === 'done').length,
      failedOrAborted: typed.filter(r => r.state.startsWith('failed')).length,
      bodies: typed.map(r => `${r.state} ${r.bytes}B ${r.body}`),
    },
    cacheScript: {
      keys: 6,
      requests: s.rpc.length - before,
      bodies: s.rpc.slice(before).map(r => `${r.state} ${r.body}`),
    },
    dom: await s.dom(),
    errors: s.errors,
  }
  await s.context.close()
  return result
}

// A burst with no measurement between the keys: only the requests are read.
async function burstPhase(browser, text, delayMs) {
  const s = await open(browser, '/')
  await settle(s.page, s.rpc)
  const input = s.page.getByRole('searchbox', { name: 'Search domains' })
  await input.focus()
  const m0 = await s.metrics()
  const u0 = await s.muts()
  const t0 = Date.now()
  await s.page.keyboard.type(text, { delay: delayMs })
  const typedInMs = Date.now() - t0
  await settle(s.page, s.rpc, 600)
  const m1 = await s.metrics()
  const u1 = await s.muts()
  const result = {
    text,
    delayMs,
    typedInMs,
    cpuMs: delta(m0, m1, cpuKeys),
    mutations: mdelta(u0, u1),
    requests: s.rpc.length,
    done: s.rpc.filter(r => r.state === 'done').length,
    aborted: s.rpc.filter(r => r.state.startsWith('failed')).length,
    labels: s.rpc.map(r => `${r.state.slice(0, 6)} ${JSON.stringify(JSON.parse(r.body).payload)}`),
    errors: s.errors,
  }
  await s.context.close()
  return result
}

async function scrollPhase(browser, { sortFirst = false } = {}) {
  const s = await open(browser, '/?q=foldkit')
  await settle(s.page, s.rpc, 600)
  const start = { dom: await s.dom(), rpc: s.rpc.length }
  let sort = null
  if (sortFirst) {
    await s.gc()
    const m0 = await s.metrics()
    const u0 = await s.muts()
    const n0 = s.rpc.length
    const trigger = s.page.getByRole('button', { name: 'Sort results', exact: false })
    await trigger.click()
    await s.page.getByRole('listbox', { name: 'Sort results', exact: false }).getByRole('option', { name: 'Price: Low to High', exact: true }).click()
    await settle(s.page, s.rpc, 800, 120000)
    const m1 = await s.metrics()
    const u1 = await s.muts()
    await s.gc()
    const m2 = await s.metrics()
    sort = {
      cpuMs: delta(m0, m1, cpuKeys),
      mutations: mdelta(u0, u1),
      requests: s.rpc.slice(n0).map(r => `${r.state} ${r.bytes}B ${r.body}`),
      heapBefore: m0.JSHeapUsedSize,
      heapAfterGc: m2.JSHeapUsedSize,
      dom: await s.dom(),
    }
  }
  const steps = []
  const stepPx = Math.round(viewport.height * 0.8)
  const maxSteps = Number(process.env.MAX_STEPS ?? 450)
  console.error(`scroll (sortFirst ${sortFirst}): start ${JSON.stringify(start.dom)}; sort ${sort ? JSON.stringify(sort.cpuMs) : '-'}`)
  let maxRows = 0
  let maxItems = 0
  let maxElements = 0
  for (let i = 0; i < maxSteps; i++) {
    const m0 = await s.metrics()
    const u0 = await s.muts()
    const moved = await s.page.evaluate(px => {
      const y = window.scrollY
      window.scrollBy({ top: px, behavior: 'instant' })
      return new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => r(window.scrollY - y))))
    }, stepPx)
    await sleep(30)
    const m1 = await s.metrics()
    const u1 = await s.muts()
    const d = await s.dom()
    maxRows = Math.max(maxRows, d.rows)
    maxItems = Math.max(maxItems, d.items)
    maxElements = Math.max(maxElements, d.elements)
    steps.push({ cpuMs: delta(m0, m1, cpuKeys), mutations: mdelta(u0, u1), rows: d.rows, items: d.items, elements: d.elements })
    if (i % 25 === 0) console.error(`  step ${i} y ${d.scrollY} script ${steps[i].cpuMs.ScriptDuration} ms rpc ${s.rpc.length} ${new Date().toISOString()}`)
    if (moved < 1) {
      // The bottom, or a page that is still loading: wait once, then stop if it does not grow.
      await settle(s.page, s.rpc, 300, 10000)
      const again = await s.page.evaluate(px => {
        const y = window.scrollY
        window.scrollBy({ top: px, behavior: 'instant' })
        return window.scrollY - y
      }, stepPx)
      if (again < 1) break
    }
  }
  await settle(s.page, s.rpc, 600)
  const frameCost = k => steps.map(x => x.cpuMs[k])
  const p = (xs, q) => {
    const a = [...xs].sort((x, y) => x - y)
    return a.length ? a[Math.min(a.length - 1, Math.floor(q * a.length))] : null
  }
  const result = {
    sortFirst,
    start,
    sort,
    stepPx,
    steps: steps.length,
    reachedBottom: steps.length < maxSteps,
    end: { dom: await s.dom(), rpc: s.rpc.length },
    requestsDuringScroll: s.rpc.length - start.rpc - (sort ? sort.requests.length : 0),
    responseBytes: sum(s.rpc.map(r => r.bytes)),
    maxRows,
    maxItems,
    maxElements,
    perStep: {
      scriptMs: { median: median(frameCost('ScriptDuration')), p95: p(frameCost('ScriptDuration'), 0.95), max: Math.max(...frameCost('ScriptDuration')) },
      taskMs: { median: median(frameCost('TaskDuration')), p95: p(frameCost('TaskDuration'), 0.95), max: Math.max(...frameCost('TaskDuration')) },
      layoutMs: { median: median(frameCost('LayoutDuration')), p95: p(frameCost('LayoutDuration'), 0.95) },
      nodesAdded: { median: median(steps.map(x => x.mutations.added)), p95: p(steps.map(x => x.mutations.added), 0.95) },
      nodesRemoved: { median: median(steps.map(x => x.mutations.removed)), p95: p(steps.map(x => x.mutations.removed), 0.95) },
      mutationRecords: { median: median(steps.map(x => x.mutations.records)), p95: p(steps.map(x => x.mutations.records), 0.95) },
    },
    rpcBodies: s.rpc.slice(0, 14).map(r => `${r.state} ${r.bytes}B ${r.body}`),
    errors: s.errors,
  }
  await s.gc()
  result.heapAfterScrollGc = (await s.metrics()).JSHeapUsedSize
  await s.context.close()
  return result
}

async function heapPhase(browser, queries, every) {
  const s = await open(browser, '/')
  await settle(s.page, s.rpc)
  const input = s.page.getByRole('searchbox', { name: 'Search domains' })
  const points = []
  const point = async i => {
    await s.gc()
    const m = await s.metrics()
    points.push({ queries: i, heap: m.JSHeapUsedSize, heapTotal: m.JSHeapTotalSize, nodes: m.Nodes, listeners: m.JSEventListeners, rpc: s.rpc.length, elements: (await s.dom()).elements })
  }
  await point(0)
  const c0 = await s.metrics()
  for (let i = 1; i <= queries; i++) {
    await input.fill(`session${i}`)
    // Longer than the 150 ms debounce, so each query makes its requests.
    await settle(s.page, s.rpc, 350, 15000)
    if (i % every === 0) await point(i)
  }
  const c1 = await s.metrics()
  const result = {
    queries,
    points,
    cpuMsPerQuery: Object.fromEntries(Object.entries(delta(c0, c1, cpuKeys)).map(([k, v]) => [k, +(v / queries).toFixed(3)])),
    requestsPerQuery: +((s.rpc.length - points[0].rpc) / queries).toFixed(2),
    errors: s.errors,
  }
  await s.context.close()
  return result
}

const out = { variant, base, viewport, startedAt: new Date().toISOString(), loadavgStart: loadavg(), phases: {} }
const browser = await chromium.launch({ headless: true, args: ['--enable-precise-memory-info', '--js-flags=--expose-gc'] })
out.browser = browser.version()
try {
  if (phases.includes('load')) {
    out.phases.loadLanding = await loadPhase(browser, '/', 1)
    out.phases.loadSearch = await loadPhase(browser, '/?q=foldkit', 1)
    out.phases.loadSearchCpu4x = await loadPhase(browser, '/?q=foldkit', 4)
  }
  if (phases.includes('type')) {
    out.phases.typeFast = await typePhase(browser, 'foldkit', 40)
    out.phases.typeSlow = await typePhase(browser, 'foldkit', 400)
    out.phases.burst30 = await burstPhase(browser, 'foldkit', 30)
    out.phases.burst80 = await burstPhase(browser, 'foldkit', 80)
    out.phases.burst250 = await burstPhase(browser, 'foldkit', 250)
  }
  const save = () => writeFileSync(join(here, `results-${variant}.json`), JSON.stringify(out, null, 1))
  if (phases.includes('scroll')) out.phases.scroll = await scrollPhase(browser)
  save()
  if (phases.includes('sort')) out.phases.sortThenScroll = await scrollPhase(browser, { sortFirst: true })
  save()
  if (phases.includes('heap')) out.phases.heap = await heapPhase(browser, 400, 50)
} finally {
  await browser.close()
}
out.loadavgEnd = loadavg()
out.endedAt = new Date().toISOString()
const file = join(here, `results-${variant}.json`)
writeFileSync(file, JSON.stringify(out, null, 1))
console.log(`wrote ${file}`)
