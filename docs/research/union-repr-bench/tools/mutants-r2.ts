// Mutants of rule.go (round 2). Each must turn a rule test red.
// Usage (GOCACHE set to a private dir): bun tools/mutants-r2.ts <a scratch copy of the module>
// It rewrites rule.go in that copy per mutant and restores it at the end.
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const dir = process.argv[2];
const file = join(dir, "rule.go");
const orig = readFileSync(file, "utf8");

const mutants: [string, string, string][] = [
  ["r1 no recursion step", "case !o.checksLast && rec:", "case false && rec:"],
  ["r2 no error step", "case !o.checksLast && err:", "case false && err:"],
  ["r3 no bound on C", "ruleOptions{boundC: true, held: true}", "ruleOptions{held: true}"],
  ["r4 no hold step", "ruleOptions{boundC: true, held: true}", "ruleOptions{boundC: true}"],
  ["r5 no trailing zero-size pad", "fields[n-1].size == 0 && off > 0", "false"],
  ["r6 no field alignment", "off = (off + f.align - 1) / f.align * f.align", "off = off + 0"],
  ["r7 slots never shared", "if !used[i] && typeKey(t) == typeKey(f.Type) {", "if !used[i] && typeKey(t) == typeKey(f.Type) && false {"],
  ["r8 recursion ignores slices", "case KSlice:\n\t\t\treturn reach(*t.Elem)", "case KSlice:\n\t\t\treturn false"],
  ["r9 hold ignores records", "case KRecord:\n\t\t\tif seen[t.Name] {", "case 99:\n\t\t\tif seen[t.Name] {"],
  ["r10 hold by any form", "if r.out[h.Name].Form != FormA {", "if false {"],
  ["r11 option shape any two variants", "len(u.Variants) == 2 && (len(u.Variants[0].Fields) == 0) != (len(u.Variants[1].Fields) == 0)", "len(u.Variants) == 2"],
  ["r12 five fields allowed", "len(fields) <= ssaFields", "len(fields) <= ssaFields+1"],
  ["r13 40-byte bound", "ssaBytes  = 32", "ssaBytes  = 40"],
  ["r14 no layout cycle guard", "if r.visiting[t.Name] {", "if false {"],
  ["r15 one pass only", "for changed := true; changed; {", "for pass, changed := 0, true; changed && pass < 1; pass++ {"],
];

const env = { ...process.env };
let survived = 0;
for (const [name, from, to] of mutants) {
  if (!orig.includes(from)) {
    console.log(`${name}: PATTERN MISSING`);
    survived++;
    continue;
  }
  writeFileSync(file, orig.replace(from, to));
  const p = Bun.spawnSync(
    [
      "timeout", "--kill-after=10s", "300",
      "go", "test", "-p", "2", "-timeout", "280s", "-count=1", "-run", "Rule|Round1", ".",
    ],
    { cwd: dir, env: { ...env, GOMEMLIMIT: "6GiB", GOMAXPROCS: "2" } },
  );
  const out = p.stdout.toString() + p.stderr.toString();
  const failed = out.split("\n").filter((l) => l.startsWith("--- FAIL")).map((l) => l.split(" ")[2]);
  const red = p.exitCode !== 0;
  if (!red) survived++;
  const why = failed.length ? failed.join(", ") : out.split("\n").find((l) => /panic|fatal|overflow|\.go:\d+/.test(l)) ?? "";
  console.log(`${name}: ${red ? "RED" : "GREEN (survived)"} exit ${p.exitCode} ${why}`);
}
writeFileSync(file, orig);
console.log(`survived: ${survived}`);
