// titles.ts: check tests.txt against the vendored upstream tests. Every
// it.effect call of upstream/story/*.story.test.ts and upstream/scene.test.ts
// has exactly one line "FILE: TITLE -> RESTATEMENT" in tests.txt, in any
// order, and tests.txt has no other line (comments start with #). An
// it.effect.each is one line, with its title as written. It reads the
// titles from the files, so nothing is pinned. Run: bun origin/titles.ts
// (from examples/fk-domains, or with the origin folder as the argument).
// Exit 0 when they agree, 1 with the missing and extra lines otherwise.
// It stands in for FK0's check in scripts/examples until that lands.
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = process.argv[2] ?? dirname(fileURLToPath(import.meta.url));

function endString(s: string, i: number): number {
  const q = s[i];
  for (let j = i + 1; j < s.length; j++) {
    if (s[j] === "\\") { j++; continue; }
    if (s[j] === q) return j;
  }
  return s.length;
}

function skipBalanced(s: string, i: number): number {
  let depth = 0;
  for (let j = i; j < s.length; j++) {
    const c = s[j];
    if (c === "'" || c === '"' || c === "`") { j = endString(s, j); continue; }
    if (c === "(" || c === "[" || c === "{") depth++;
    if (c === ")" || c === "]" || c === "}") {
      depth--;
      if (depth === 0) return j + 1;
    }
  }
  return s.length;
}

// titles gives each it.effect title of the file's source, in order.
export function titles(s: string): string[] {
  const out: string[] = [];
  const re = /\bit\.effect(\.each)?\s*\(/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s))) {
    let i = m.index + m[0].length - 1;
    if (m[1]) {
      i = skipBalanced(s, i);
      while (/\s/.test(s[i])) i++;
    }
    let j = i + 1;
    while (/\s/.test(s[j])) j++;
    out.push(s.slice(j + 1, endString(s, j)).replace(/\s+/g, " "));
  }
  return out;
}

const files = [
  ...readdirSync(join(here, "upstream/story")).filter((f) => f.endsWith(".story.test.ts")).sort().map((f) => `story/${f}`),
  "scene.test.ts",
];
const want = new Set<string>();
for (const f of files) {
  for (const t of titles(readFileSync(join(here, "upstream", f), "utf8"))) want.add(`${f}: ${t}`);
}
const have = new Map<string, number>();
let bad = 0;
for (const line of readFileSync(join(here, "tests.txt"), "utf8").split("\n")) {
  if (line.trim() === "" || line.startsWith("#")) continue;
  const at = line.lastIndexOf(" -> ");
  const key = at < 0 ? line : line.slice(0, at);
  if (at < 0 || line.slice(at + 4).trim() === "") { bad++; console.log(`no restatement: ${line}`); }
  have.set(key, (have.get(key) ?? 0) + 1);
}
for (const k of want) if (!have.has(k)) { bad++; console.log(`missing: ${k}`); }
for (const [k, n] of have) {
  if (!want.has(k)) { bad++; console.log(`extra: ${k}`); }
  if (n > 1) { bad++; console.log(`twice: ${k}`); }
}
console.log(`${want.size} upstream titles, ${bad} problems`);
process.exit(bad ? 1 : 0);
