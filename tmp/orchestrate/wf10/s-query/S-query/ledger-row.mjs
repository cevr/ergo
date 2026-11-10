// Insert the http QueryDecodesByTheRule ledger row after WriteAfterStreamFails.
import { readFileSync, writeFileSync } from "node:fs"
const root = "/home/exedev/Developer/personal/.worktrees/ergo-l8b"
const test = readFileSync(`${root}/internal/sketch/lower/std_http_test.go`, "utf8")
const m = test.match(/const httpQueryStatement = `([^`]*)`/)
if (!m) throw new Error("no statement")
const row = "| `http` | `QueryDecodesByTheRule` | `" + m[1].replaceAll("|", "\\|") + "` | `TestStdHTTPLawsRun` | tested |"
const path = `${root}/docs/spec/stdlib.md`
const lines = readFileSync(path, "utf8").split("\n")
const existing = lines.findIndex(l => l.startsWith("| `http` | `QueryDecodesByTheRule`"))
if (existing >= 0) lines[existing] = row
else {
  const i = lines.findIndex(l => l.startsWith("| `http` | `WriteAfterStreamFails`"))
  lines.splice(i + 1, 0, row)
}
writeFileSync(path, lines.join("\n"))
console.log("ok")
