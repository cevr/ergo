// Host adapter cost of the laws prototype (review 1 m5, review 2 m4).
// Calls each export of proto/lib.mjs in a loop and prints raw per-call
// samples.
//   oldest     an export with no adapter, over the carrier: a floor for a
//              cheap export, but a different operation from the two below
//   sortTrio   outward adapter: snapshot of the argument, deep freeze of the result
//   search     inward adapter: snapshot of both arguments and the sortedTrio
//              admission check before the call
// Each of these is the cost per exported call, body included (the sort, the
// search). To isolate the adapter, each evidence export is also measured
// with the same body and no adapter: a copy of lib.mjs whose export line
// names the module-private __ef_function_* binding instead of the adapter
// (`<name>-unadapted`). The difference of medians is the adapter's cost.
// The copy is written beside lib.mjs, so it resolves 'effect' the same way,
// and removed afterwards.
// Usage: node benchmarks/laws/ts/adapter-cost.mjs [iterations] [samples]
import { readFileSync, writeFileSync, rmSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { makeUser, makeTrio, sortTrio, search, oldest } from './proto/lib.mjs';

const iterations = Number(process.argv[2] ?? 200000);
const samples = Number(process.argv[3] ?? 7);
const libPath = fileURLToPath(new URL('./proto/lib.mjs', import.meta.url));
const copyPath = fileURLToPath(new URL('./proto/.lib.unadapted.mjs', import.meta.url));
let text = readFileSync(libPath, 'utf8');
for (const name of ['sortTrio', 'search']) {
  const adapted = `export { __ef_export_${name} as ${name} }`;
  if (!text.includes(adapted)) throw new Error(`adapter-cost: lib.mjs has no adapter export for ${name}`);
  text = text.replace(adapted, `export { __ef_function_${name} as ${name} }`);
}
writeFileSync(copyPath, text);
let raw;
try {
  raw = await import(copyPath);
} finally {
  rmSync(copyPath, { force: true });
}

const ada = makeUser("ada", 36n);
const bob = makeUser("bob", 12n);
const cy = makeUser("cy", 24n);
const trio = makeTrio(ada, bob, cy);
const sorted = sortTrio(trio);

const cases = {
  oldest: () => oldest(trio),
  sortTrio: () => sortTrio(trio),
  'sortTrio-unadapted': () => raw.sortTrio(trio),
  search: () => search(sorted, cy),
  'search-unadapted': () => raw.search(sorted, cy),
};

let sink = 0;
const run = (f) => {
  const start = process.hrtime.bigint();
  for (let i = 0; i < iterations; i++) {
    if (f()) sink++;
  }
  return Number(process.hrtime.bigint() - start) / iterations;
};

const medians = {};
for (const [name, f] of Object.entries(cases)) {
  run(f); // warm-up sample, discarded
  const rawSamples = [];
  for (let s = 0; s < samples; s++) rawSamples.push(run(f));
  const sortedRaw = [...rawSamples].sort((a, b) => a - b);
  const median = sortedRaw[Math.floor(sortedRaw.length / 2)];
  medians[name] = median;
  console.log(`${name} ns/call median=${median.toFixed(1)} raw=${rawSamples.map((x) => x.toFixed(1)).join(",")}`);
}
for (const name of ['sortTrio', 'search']) {
  console.log(`${name} adapter ns/call (median difference) = ${(medians[name] - medians[name + '-unadapted']).toFixed(1)}`);
}
if (sink === 0) throw new Error("adapter-cost: the loop did nothing");
