// records.ts holds the TypeScript host to the conformance vectors: for
// each DIR/src/NAME.ts.txt, `lint-ts record` must write DIR/records/NAME.record
// byte for byte. Another TypeScript host (oxlint-plugin-effect's ESTree
// extractor) is held to the same records.
//
//   bun ts/records.ts DIR
import { readdirSync, readFileSync } from "node:fs"
import { join } from "node:path"

const bin = process.env.ERGO_BIN
if (!bin) throw new Error("ERGO_BIN names the built lint-ts")
const dir = process.argv[2]
if (!dir) throw new Error("usage: bun ts/records.ts DIR")
let failed = false
for (const file of readdirSync(join(dir, "src")).sort()) {
  if (!file.endsWith(".ts.txt")) continue
  const name = file.slice(0, -".ts.txt".length) + ".record"
  const run = Bun.spawnSync([bin, "record", join(dir, "src", file)])
  const got = run.stdout.toString()
  const want = readFileSync(join(dir, "records", name), "utf8")
  if (run.exitCode !== 0 || got !== want) {
    failed = true
    const g = got.split("\n")
    const w = want.split("\n")
    const at = g.findIndex((line, i) => line !== w[i])
    console.log(`${name}: differs at record line ${at + 1}\n  lint-ts: ${g[at]}\n  vectors: ${w[at]}`)
  } else {
    console.log(`same ${name}`)
  }
}
if (failed) process.exit(1)
