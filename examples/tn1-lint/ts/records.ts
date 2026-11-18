// records.ts holds the TypeScript host to the conformance vectors: for
// each DIR/src/NAME.ts.txt, `tn1-lint record` must write DIR/records/NAME.record
// byte for byte. Another TypeScript host (oxlint-plugin-effect's ESTree
// extractor) is held to the same records.
//
//   bun ts/records.ts DIR
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs"
import { join } from "node:path"

const bin = process.env.ERGO_BIN
if (!bin) throw new Error("ERGO_BIN names the built tn1-lint")
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
    console.log(`${name}: differs at record line ${at + 1}\n  tn1-lint: ${g[at]}\n  vectors: ${w[at]}`)
  } else {
    console.log(`same ${name}`)
  }
}
// Each DIR/projects/P is a project: `tn1-lint project` over its .ts files,
// named by their paths in P, must write each DIR/records/P-PATH.record
// (record version 3; PATH's slashes as dashes, its .ts cut).
const projects = join(dir, "projects")
for (const project of existsSync(projects) ? readdirSync(projects).sort() : []) {
  const root = join(projects, project)
  const files: string[] = []
  const walk = (sub: string) => {
    for (const e of readdirSync(join(root, sub)).sort()) {
      const p = sub ? `${sub}/${e}` : e
      if (statSync(join(root, p)).isDirectory()) walk(p)
      else if (p.endsWith(".ts")) files.push(p)
    }
  }
  walk("")
  const run = Bun.spawnSync([bin, "project", ...files], { cwd: root })
  const parts = run.stdout.toString().split(/^== /m).filter((x) => x !== "")
  if (run.exitCode !== 0 || parts.length !== files.length) {
    failed = true
    console.log(`${project}: tn1-lint project exited ${run.exitCode} with ${parts.length} records for ${files.length} files`)
    continue
  }
  for (const part of parts) {
    const nl = part.indexOf("\n")
    const name = `${project}-${part.slice(0, nl).replace(/\.ts$/, "").replaceAll("/", "-")}.record`
    const got = part.slice(nl + 1)
    const want = readFileSync(join(dir, "records", name), "utf8")
    if (got !== want) {
      failed = true
      const g = got.split("\n")
      const w = want.split("\n")
      const at = g.findIndex((line, i) => line !== w[i])
      console.log(`${name}: differs at record line ${at + 1}\n  tn1-lint: ${g[at]}\n  vectors: ${w[at]}`)
    } else {
      console.log(`same ${name}`)
    }
  }
}
if (failed) process.exit(1)
