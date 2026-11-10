// Run in the checkout being judged: each sq-* mutant of the lower ledger
// is applied, TestStdHTTPLawsRun runs (its own process group, inheriting
// the caller's memory bound), and the source is restored.
import { readFileSync, writeFileSync } from "node:fs"
import { spawnSync } from "node:child_process"
const root = process.cwd()
const prefix = process.env.PREFIX ?? "sq-"
const ledgers = ["internal/sketch/lower/testdata/mutants.json", "runtime/v1/testdata/mutants.json"]
const mutants = ledgers.flatMap(path => JSON.parse(readFileSync(`${root}/${path}`, "utf8")).mutants).filter(m => m.name.startsWith(prefix))
for (const m of mutants) {
  const source = readFileSync(`${root}/${m.file}`, "utf8")
  if (source.split(m.from).length !== 2) throw new Error(`stale mutant ${m.name}`)
}
if (process.argv.includes("--validate")) { console.log(`${mutants.length} exact edits`); process.exit(0) }
let failed = 0
for (const m of mutants) {
  const path = `${root}/${m.file}`, source = readFileSync(path, "utf8")
  try {
    writeFileSync(path, source.replace(m.from, m.to))
    const pkg = m.killers[0].startsWith("runtime/v1:") ? "./runtime/v1" : "./internal/sketch/lower"
    const run = m.killers[0].split(":")[1]
    const p = spawnSync("go", ["test", "-p", "2", "-count=1", "-timeout", "10m", "-run", `^${run}$`, pkg], { cwd: root, encoding: "utf8", detached: true, timeout: 12 * 60_000 })
    if (p.pid > 1) {
      try { process.kill(-p.pid, "SIGKILL") } catch (e) { if (e.code !== "ESRCH") throw e }
    }
    const output = (p.stdout ?? "") + (p.stderr ?? "")
    writeFileSync(`${import.meta.dir}/mutant-${m.name}.log`, output + `\nexit ${p.status}\n`)
    const killed = !p.error && p.status !== 0 && /--- FAIL:|FAIL/.test(output) && !/build failed|syntax error|S10\d/.test(output)
    console.log(`${m.name}: ${killed ? "killed" : "NOT KILLED"} (exit ${p.status})`)
    if (!killed) failed++
  } finally { writeFileSync(path, source) }
}
console.log(`exit ${failed ? 1 : 0}`)
process.exit(failed ? 1 : 0)
