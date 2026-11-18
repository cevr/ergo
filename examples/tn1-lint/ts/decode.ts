// decode.ts holds @cvr/tn1-lint's wire module and lintSync to the replies
// tn1-lint really writes. The `wire` case's setup.sh copies the package
// (pkg/: lint.js, worker.js, wire.js) and the serve and lint cases'
// expected output, which the built binary is held to, into the case.
//
//   1. Each recorded serve reply and lint report decodes as itself.
//   2. Each with one field taken out, a field of the wrong kind, an error
//      beside diagnostics, or diagnostics beside an error, is refused.
//   3. lintSync, over a serve that writes such a line (ts/fake-serve.sh),
//      throws its reason; a well-formed reply still returns its
//      diagnostics after one that was not.
// It prints one line per fact; a failure says FAIL and exits 1.
import { chmodSync, readFileSync, writeFileSync } from "node:fs"
import { createRequire } from "node:module"
import { resolve } from "node:path"

const require = createRequire(import.meta.url)
const wire = require(resolve("pkg/wire.js"))
let failed = false
const fail = (message: string) => {
  failed = true
  console.log(`FAIL ${message}`)
}

const replies = readFileSync("serve.out", "utf8").split("\n").filter((line) => line !== "")
const report = readFileSync("lint.out", "utf8")

/** Each value of `value` with one object field (at any depth) taken out, with its path. */
function withoutEach(value: any, at = ""): Array<[string, any]> {
  if (Array.isArray(value)) return value.flatMap((v, i) => withoutEach(v, `${at}[${i}]`).map(([p, w]): [string, any] => [p, value.map((x, j) => (j === i ? w : x))]))
  if (typeof value !== "object" || value === null) return []
  return Object.keys(value).flatMap((k) => {
    const { [k]: _, ...rest } = value
    const deeper = withoutEach(value[k], `${at}.${k}`).map(([p, w]): [string, any] => [p, { ...value, [k]: w }])
    return [[`${at}.${k}`, rest] as [string, any], ...deeper]
  })
}

/** Each value of `value` with one leaf (at any depth) of the wrong kind: a string for a number, a number for a string. */
function wrongEach(value: any, at = ""): Array<[string, any]> {
  if (Array.isArray(value)) return value.flatMap((v, i) => wrongEach(v, `${at}[${i}]`).map(([p, w]): [string, any] => [p, value.map((x, j) => (j === i ? w : x))]))
  if (typeof value === "string") return [[at, 1]]
  if (typeof value === "number") return [[at, String(value)]]
  if (typeof value !== "object" || value === null) return []
  return Object.keys(value).flatMap((k) => wrongEach(value[k], `${at}.${k}`).map(([p, w]): [string, any] => [p, { ...value, [k]: w }]))
}

const refused = (decode: (text: string) => unknown, text: string) => {
  try {
    decode(text)
    return false
  } catch (error) {
    return error instanceof wire.UnreadableReply
  }
}

// 1 and 2: the replies serve writes. Mutants: the reply not checked (a
// missing field read as an empty list), the error-and-diagnostics check
// dropped, decodeReport not checked.
let wrong = 0
for (const line of replies) {
  const reply = JSON.parse(line)
  if (JSON.stringify(wire.decodeReply(line)) !== JSON.stringify(reply)) fail(`decodeReply changed ${line.slice(0, 80)}`)
  for (const [path, broken] of [...withoutEach(reply), ...wrongEach(reply)]) {
    wrong++
    if (!refused(wire.decodeReply, JSON.stringify(broken))) fail(`decodeReply read a reply with ${path} taken out or of the wrong kind`)
  }
  const both = reply.error === ""
    ? { ...reply, error: "tn1-lint: cannot read x.tn1" }
    : { ...reply, diagnostics: replies.map((l) => JSON.parse(l).diagnostics).find((d) => d.length > 0) }
  if (both.diagnostics.length > 0 && !refused(wire.decodeReply, JSON.stringify(both))) fail("decodeReply read a reply with an error and diagnostics")
}
if (!refused(wire.decodeReply, "not json")) fail("decodeReply read a line that is not JSON")
console.log(`serve: ${replies.length} replies decode; ${wrong} with a field taken out or of the wrong kind, and each with an error beside diagnostics, are refused`)

const parsed = JSON.parse(report)
if (JSON.stringify(wire.decodeReport(report)) !== JSON.stringify(parsed)) fail("decodeReport changed the report")
let wrongReports = 0
for (const [path, broken] of [...withoutEach(parsed), ...wrongEach(parsed)]) {
  wrongReports++
  if (!refused(wire.decodeReport, JSON.stringify(broken))) fail(`decodeReport read a report with ${path} taken out or of the wrong kind`)
}
console.log(`lint: the report decodes; ${wrongReports} with a field taken out or of the wrong kind are refused`)

// 3: lintSync over a serve that writes what the file says. Mutant: the
// worker passing a reply on unchecked (lintSync returns undefined for a
// reply with no diagnostics).
const replyFile = resolve("fake-reply")
chmodSync("ts/fake-serve.sh", 0o755)
process.env.TN1_LINT_BINARY = resolve("ts/fake-serve.sh")
process.env.FAKE_REPLY = replyFile
const { lintSync } = require(resolve("pkg/lint.js"))
const request = { rule: "narration", weights: "w.tn1", path: "a.ts", text: "x" }
const good = replies.find((line) => JSON.parse(line).diagnostics.length > 0)!
for (const [what, line] of [
  ["no diagnostics", '{"error":""}'],
  ["an error and diagnostics", JSON.stringify({ ...JSON.parse(good), error: "tn1-lint: cannot read w.tn1" })],
  ["a span with no offset", good.replace('"offset":', '"at":')],
] as const) {
  writeFileSync(replyFile, `${line}\n`)
  let threw = ""
  try {
    lintSync(request)
  } catch (error) {
    threw = String(error)
  }
  if (threw === "") fail(`lintSync read a reply with ${what} as a pass`)
  else console.log(`lintSync throws on a reply with ${what}`)
}
writeFileSync(replyFile, `${good}\n`)
const after = lintSync(request)
if (JSON.stringify(after) !== JSON.stringify(JSON.parse(good).diagnostics)) fail("lintSync did not return a good reply's diagnostics after a bad one")
else console.log("lintSync returns a good reply's diagnostics after a bad one")
if (failed) process.exit(1)
