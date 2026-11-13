// Records the original server's wire for the port's WireAgreesWithOrigin
// property (plan.md section 20.4): request and response bodies of /rpc at
// the pin, and the status codes of its refusals.
//   node wire.mjs <base url>
// Output: wire/<n>-<name>.request.json, .response.txt and wire/index.json.
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const base = process.argv[2] ?? 'http://127.0.0.1:5391'
const out = join(here, 'wire')
mkdirSync(out, { recursive: true })

const request = (id, payload) =>
  JSON.stringify({ _tag: 'Request', id, tag: 'SearchDomains', payload, traceId: '00000000000000000000000000000001', spanId: '0000000000000001', sampled: true, headers: [] })

const cases = [
  ['top', { label: 'foldkit', offset: 0, limit: 4 }],
  ['first-page', { label: 'foldkit', offset: 4, limit: 56 }],
  ['page-60', { label: 'foldkit', offset: 60, limit: 60 }],
  ['last-page', { label: 'foldkit', offset: 540, limit: 16 }],
  ['past-end', { label: 'foldkit', offset: 556, limit: 60 }],
  ['whole', { label: 'foldkit', offset: 0, limit: 556 }],
  ['no-range', { label: 'acme' }],
  ['taken', { label: 'google', offset: 0, limit: 4 }],
  ['one-letter', { label: 'f', offset: 0, limit: 4 }],
  ['empty-label', { label: '', offset: 0, limit: 4 }],
  ['upper', { label: 'FoldKit', offset: 0, limit: 4 }],
  ['dotted', { label: 'acme.co', offset: 0, limit: 4 }],
  ['negative', { label: 'acme', offset: -1, limit: 4 }],
  ['zero-limit', { label: 'acme', offset: 0, limit: 0 }],
]

const index = []
let n = 0
const post = async (name, body, headers, path = '/rpc') => {
  n++
  const res = await fetch(base + path, { method: headers.method ?? 'POST', headers: headers.h, body: headers.method === 'GET' ? undefined : body })
  const text = await res.text()
  const stem = `${String(n).padStart(2, '0')}-${name}`
  if (body !== undefined && body.length <= 4096) writeFileSync(join(out, `${stem}.request.json`), body)
  writeFileSync(join(out, `${stem}.response.txt`), text)
  index.push({
    case: stem,
    path,
    method: headers.method ?? 'POST',
    requestBytes: body?.length ?? 0,
    status: res.status,
    contentType: res.headers.get('content-type'),
    cacheControl: res.headers.get('cache-control'),
    responseBytes: text.length,
  })
}

const json = { h: { 'content-type': 'application/json' } }
for (const [name, payload] of cases) await post(name, request(n, payload), json)
// The refusals.
await post('not-json-type', request(100, { label: 'acme' }), { h: { 'content-type': 'text/plain' } })
await post('too-large', request(101, { label: 'a'.repeat(1100) }), json)
await post('malformed', '{"_tag":"Request",', json)
await post('get', undefined, { method: 'GET', h: {} })
await post('exactly-1024', request(102, { label: 'acme' }).padEnd(1024, ' '), json)
await post('exactly-1025', request(103, { label: 'acme' }).padEnd(1025, ' '), json)

writeFileSync(join(out, 'index.json'), JSON.stringify({ source: 'foldkit/foldkit-domains 039c941ad42c9db46d1aa8014c770c9a841c75ad (MIT)', base, cases: index }, null, 1))
for (const c of index) console.log(`${c.case}: ${c.status} ${c.contentType} ${c.cacheControl} ${c.requestBytes}B -> ${c.responseBytes}B`)
