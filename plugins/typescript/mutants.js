#!/usr/bin/env bun
// The plugin's mutant run: each row of mutants.json made in a copy of
// this folder (its node_modules linked), whose `bun test` must fail. The
// copy is a fresh temporary folder, removed when the run ends.
//
//   bun mutants.js [NAME...]

import { cpSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const here = import.meta.dir;
const ledger = JSON.parse(readFileSync(join(here, "mutants.json"), "utf8"));
const only = process.argv.slice(2);
const files = ["package.json", "split.js", "main.js", "split.test.js"];
const scratch = mkdtempSync(join(tmpdir(), "work-typescript-mutants-"));
if (!scratch.startsWith(tmpdir() + "/work-typescript-mutants-")) throw new Error(`not a scratch folder: ${scratch}`);
let survived = 0;
try {
  for (const m of ledger.mutants) {
    if (only.length > 0 && !only.includes(m.name)) continue;
    const dir = join(scratch, m.name);
    cpSync(here, dir, { recursive: true, filter: (src) => src === here || files.some((f) => src === join(here, f)) });
    symlinkSync(join(here, "node_modules"), join(dir, "node_modules"));
    const path = join(dir, m.file);
    const text = readFileSync(path, "utf8");
    if (text.split(m.from).length !== 2) {
      console.log(`stale ${m.name}: its from is not once in ${m.file}`);
      survived++;
      continue;
    }
    writeFileSync(path, text.replace(m.from, m.to));
    const r = Bun.spawnSync(["bun", "test"], { cwd: dir, stdout: "pipe", stderr: "pipe" });
    if (r.exitCode === 0) {
      console.log(`survived ${m.name}`);
      survived++;
    } else {
      console.log(`killed ${m.name}`);
    }
  }
} finally {
  rmSync(scratch, { recursive: true, force: true });
}
console.log(`mutants: ${survived} not killed`);
process.exit(survived === 0 ? 0 : 1);
