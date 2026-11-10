// Run from the checkout being judged, with ERGO naming its built compiler.
// The caller supplies A1 and the bounded scope from common.md.
import { readFileSync, writeFileSync } from "node:fs"
import { spawnSync } from "node:child_process"
const root = process.cwd()
const ledgers = ["internal/tsproj/testdata/mutants.json", "internal/sketch/lower/testdata/mutants.json", "scripts/examples/testdata/mutants.json"]
const mutants = ledgers.flatMap(path => JSON.parse(readFileSync(`${root}/${path}`, "utf8")).mutants).filter(m => m.name.startsWith("e3-"))
for (const m of mutants) {
  const source = readFileSync(`${root}/${m.file}`, "utf8")
  if (source.split(m.from).length !== 2) throw new Error(`stale mutant ${m.name}`)
}
if (process.argv.includes("--validate")) { console.log(`${mutants.length} exact edits`); process.exit(0) }
if (!process.env.ERGO) throw new Error("ERGO must name this checkout's built compiler")
for (const m of mutants) {
  const path = `${root}/${m.file}`
  const source = readFileSync(path, "utf8")
  try {
    writeFileSync(path, source.replace(m.from, m.to))
    const killer = m.killers[0]
    let name = "go", args
    if (killer.startsWith("tsproj:")) args = ["test", "-p", "2", "-count=1", "-timeout", "10m", "-run", `^${killer.split(":")[1]}$`, "./internal/tsproj"]
    else if (killer.startsWith("lower:")) args = ["test", "-p", "2", "-count=1", "-timeout", "10m", "-run", "^TestStdHTTPLawsRun$", "./internal/sketch/lower"]
    else { name = process.env.ERGO; args = ["test", "examples/jobs/api"] }
    const p = spawnSync(name, args, { cwd: root, env: { ...process.env, ERGO_TS_TEST: "1" }, encoding: "utf8", detached: true })
    try { process.kill(-p.pid, "SIGKILL") } catch (e) { if (e.code !== "ESRCH") throw e }
    const output = (p.stdout ?? "") + (p.stderr ?? "")
    writeFileSync(`${import.meta.dir}/mutant-${m.name}.log`, output + `\nexit ${p.status}\n`)
    if (p.error || p.status === 0 || !/--- FAIL:|FAIL|refuted/i.test(output) || /build failed|syntax error/.test(output)) throw new Error(`${m.name} not assertion-killed: ${p.status}\n${output}`)
    console.log(`${m.name}: killed (exit ${p.status})`)
  } finally { writeFileSync(path, source) }
}
console.log("exit 0")
