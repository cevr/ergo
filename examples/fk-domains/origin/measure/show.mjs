// Prints a short reading of results-<variant>.json (measure.mjs).
//   node show.mjs <variant>
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
const here = dirname(fileURLToPath(import.meta.url))
const r = JSON.parse(readFileSync(join(here, `results-${process.argv[2] ?? 'native'}.json`), 'utf8'))
const p = r.phases
console.log(`# ${r.variant}  ${r.browser}  load ${r.loadavgStart} -> ${r.loadavgEnd}`)
for (const k of ['loadLanding', 'loadSearch', 'loadSearchCpu4x']) {
  if (!p[k]) continue
  console.log(`## ${k} (${p[k].path}, cpu x${p[k].throttle})`)
  console.log(' median', JSON.stringify(p[k].median))
  console.log(' bytes', JSON.stringify(p[k].bytes))
  console.log(' dom', JSON.stringify(p[k].runs[0].dom), 'rpc', JSON.stringify(p[k].runs[0].rpc.map(x => `${x.state} ${x.bytes}B ${x.body}`)))
  console.log(' runs hydratedAt', p[k].runs.map(x => Math.round(x.hydratedAt)).join(' '), 'fcp', p[k].runs.map(x => Math.round(x.firstContentfulPaint)).join(' '), 'errors', p[k].runs.flatMap(x => x.errors).length)
}
for (const k of ['typeFast', 'typeSlow']) {
  if (!p[k]) continue
  console.log(`## ${k} ("${p[k].text}", ${p[k].gapMs} ms between keys)`)
  console.log(' perKeyMedian', JSON.stringify(p[k].perKeyMedian))
  for (const x of p[k].keys) console.log(`  ${x.key} script ${x.cpuMs.ScriptDuration} task ${x.cpuMs.TaskDuration} layout ${x.cpuMs.LayoutDuration} muts ${JSON.stringify(x.mutations)} req ${x.requestsStarted}`)
  console.log(' requests', p[k].requests.total, 'done', p[k].requests.done, 'failed', p[k].requests.failedOrAborted)
  for (const b of p[k].requests.bodies) console.log('  ', b.slice(0, 200))
  console.log(' cacheScript', JSON.stringify(p[k].cacheScript))
  console.log(' dom', JSON.stringify(p[k].dom), 'errors', p[k].errors.length)
}
for (const k of ['burst30', 'burst80', 'burst250']) {
  if (!p[k]) continue
  const b = p[k]
  console.log(`## ${k}: "${b.text}" typed in ${b.typedInMs} ms; requests ${b.requests}, done ${b.done}, aborted ${b.aborted}; cpu ${JSON.stringify(b.cpuMs)}; muts ${JSON.stringify(b.mutations)}`)
  console.log('  ', b.labels.join(' | ').slice(0, 900))
}
for (const k of ['scroll', 'sortThenScroll']) {
  if (!p[k]) continue
  const s = p[k]
  console.log(`## ${k}`)
  console.log(' start', JSON.stringify(s.start), 'end', JSON.stringify(s.end))
  if (s.sort) console.log(' sort', JSON.stringify(s.sort).slice(0, 900))
  console.log(` steps ${s.steps} of ${s.stepPx}px, bottom ${s.reachedBottom}, requests in scroll ${s.requestsDuringScroll}, response bytes ${s.responseBytes}`)
  console.log(` max rows ${s.maxRows}, max items ${s.maxItems}, max elements ${s.maxElements}, heap after gc ${s.heapAfterScrollGc}`)
  console.log(' perStep', JSON.stringify(s.perStep))
  for (const b of s.rpcBodies) console.log('  ', b.slice(0, 200))
  console.log(' errors', s.errors.length)
}
if (p.heap) {
  console.log('## heap')
  for (const x of p.heap.points) console.log('  ', JSON.stringify(x))
  console.log(' cpuMsPerQuery', JSON.stringify(p.heap.cpuMsPerQuery), 'requestsPerQuery', p.heap.requestsPerQuery, 'errors', p.heap.errors.length)
}
