// Scratch patch of the clone for the measurement of plan.md section 20:
// makes the candidate catalog N entries long by adding synthetic TLDs after
// the 556 real ones (x00001, x00002, ...). `bun patch-catalog.ts 1002`;
// `bun patch-catalog.ts restore` puts the original file back.
import { copyFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
const file = "/tmp/opencode/fk-domains/src/shared/tld.ts";
const orig = `${file}.orig`;
if (!existsSync(orig)) copyFileSync(file, orig);
const arg = process.argv[2] ?? "restore";
if (arg === "restore") {
  copyFileSync(orig, file);
  console.log("restored");
  process.exit(0);
}
const want = Number(arg);
const text = readFileSync(orig, "utf8");
const have = (text.match(/^\s*'[^']+',$/gm) ?? []).length;
if (!(want > have)) {
  console.log(`have ${have}, want ${arg}: nothing to do`);
  process.exit(1);
}
const extra: string[] = [];
for (let i = 1; i <= want - have; i++) extra.push(`  'x${String(i).padStart(5, "0")}',`);
const cut = text.lastIndexOf("]");
writeFileSync(file, text.slice(0, cut) + extra.join("\n") + "\n" + text.slice(cut));
console.log(`catalog: ${have} real + ${extra.length} synthetic = ${want}`);
