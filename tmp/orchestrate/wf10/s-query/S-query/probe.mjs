// Restate query.ergo's law section as the stdhttp probe's test, and set
// httpQueryStatement to the law's one-line statement; then the ledger row.
import { readFileSync, writeFileSync } from "node:fs"
const root = "/home/exedev/Developer/personal/.worktrees/ergo-l8b"
const src = readFileSync(`${root}/internal/sketch/std/http/query.ergo`, "utf8")
const section = src.slice(src.indexOf("// queryProbe is"))
let probe = section
  .split("\n").filter(l => !l.startsWith("//")).join("\n")
  .replace(/(?<![.\w])Field\b/g, "http.Field")
  .replace("DecodeQuery[queryProbe](ParseQuery(raw))", "http.DecodeQuery[queryProbe](http.ParseQuery(raw))")
  .replace("case QueryRepeated as q:", "case http.QueryRepeated as q:")
  .replace("law QueryDecodesByTheRule(", "test HttpQueryDecodesByTheRule(")
  .replace(/\t\treturn (want == [^\n]+)\n/g, "\t\tassert $1\n\t\treturn\n")
  .replace("\t\treturn false\n\t}\n\twant == \"ok\" && got == record\n}", "\t\tassert false\n\t\treturn\n\t}\n\tassert want == \"ok\" && got == record\n}")
probe = probe.replace(/\n\n+/g, "\n\n").trim() + "\n"
const lawText = section.slice(section.indexOf("law QueryDecodesByTheRule(") + 4)
const statement = lawText.trim().replace(/\s+/g, " ")
const testPath = `${root}/internal/sketch/lower/std_http_test.go`
let test = readFileSync(testPath, "utf8")
const start = test.indexOf("type queryProbe {")
const end = test.indexOf("`\n\nconst httpQueryStatement")
if (start < 0 || end < 0) throw new Error("probe markers")
test = test.slice(0, start) + probe + test.slice(end)
test = test.replace(/const httpQueryStatement = `[^`]*`/, "const httpQueryStatement = `" + statement + "`")
writeFileSync(testPath, test)
console.log(statement.length)
