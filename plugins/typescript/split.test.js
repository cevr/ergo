// The Splitter's laws (modules/work/work/language.ergo) on a corpus of
// TypeScript shapes and on seeded edits of it, and the rows of the
// plan's list of what a TypeScript Splitter gets wrong. `work
// plugin-test` runs the same laws on a whole repository; these run with
// no repository and no work binary. A failing seed prints its file, the
// edit and the replay command.

import { expect, test } from "bun:test";
import { serve } from "./main.js";
import { units } from "./split.js";

const corpus = {
  "a.ts": `#!/usr/bin/env bun
// The header.
import { a, b as c } from "./x";
import type { T } from "./t";
import d from "d";

/** f is overloaded. */
export function f(x: string): string;
export function f(x: number): number;
export function f(x: any) {
  return x;
}

export class C<T> extends Base implements I {
  static n = 1; // a note on n
  #p = 2;
  constructor(private q: number) {
    super();
  }
  get v() { return this.#p }
  set v(x) { this.#p = x }
  m(): void;
  m(x?: number) {}
  [Symbol.iterator]() {}
}

interface I { a: string; m(): void; (): void; [k: string]: unknown }
interface I { b: number }

enum E { A, B = 2, "c-d" }
const enum F { X }

declare module "m" {
  export const z: number;
  export function y(): void;
}
namespace N.M { export const k = 1 }
declare global { interface Window { w: 1 } }

const o = { k: 1, "l": 2, ...r, [q]: 3, m() {} } as const;
let [p1, { p2, p3: [p4] }, ...p5] = xs, p6 = 1;
export type U = A | B;
export { a as q, c };
export * from "./all";
export * as ns from "./ns";
type G = typeof import("./g");

f(1);
if (x) { y() }
f(2);
export default { name: "x", run() {} } satisfies Config;
`,
  "b.tsx": `import React from "react";
export const App = () => <div className="x">{1}</div>;
export default function Page() {
  return <App />;
}
`,
  "c.mts": `export const x = 1;\n`,
  "d.cts": `const x = require("x");\nmodule.exports = x;\n`,
  "e.ts": `// only a comment\n`,
  "f.ts": ``,
  "g.ts": `const s = "é ✓ 𝄞"; // wide letters\nexport const t = s;`,
  "h.ts": `const a = <T>b;\nlet x = 1; let y = 2;\n`,
};

function joins(text, us) {
  return us.map((u) => u.text).join("") === text;
}

function keysUnique(us) {
  return new Set(us.map((u) => u.key)).size === us.length;
}

// membersInside: a unit's members join to one run of its text, and their
// keys are unique.
function membersInside(us) {
  return us.every((u) => u.members.length === 0 || (u.text.includes(u.members.map((m) => m.text).join("")) && keysUnique(u.members)));
}

// rng is a small seeded generator (mulberry32).
function rng(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// inside is the unit that holds [at, at+cut) strictly inside it, or -1.
function inside(us, at, cut) {
  let start = 0;
  for (let i = 0; i < us.length; i++) {
    const end = start + us[i].text.length;
    if (at > start && at + cut < end) return i;
    start = end;
  }
  return -1;
}

test("UnitsAreTheFile, KeysAreUnique and the members on the corpus", () => {
  for (const [file, text] of Object.entries(corpus)) {
    const us = units(file, text);
    expect(us.unreadable, file).toBeUndefined();
    expect(joins(text, us), file).toBe(true);
    expect(keysUnique(us), file).toBe(true);
    expect(membersInside(us), file).toBe(true);
  }
});

// EditStaysInItsUnit on seeded edits: a space added after a space, a
// line added in a body, a word changed; each inside one unit. An edit
// that leaves the file with another count of units (or that does not
// parse) is not judged, as in the law.
test("EditStaysInItsUnit on seeded edits", () => {
  const seeds = Number(process.env.SEEDS ?? 300);
  const first = Number(process.env.SEED ?? 1);
  for (let seed = first; seed < first + seeds; seed++) {
    const r = rng(seed);
    const files = Object.keys(corpus).filter((f) => corpus[f].length > 0);
    const file = files[Math.floor(r() * files.length)];
    const text = corpus[file];
    const before = units(file, text);
    const spaces = [...text.matchAll(/ /g)].map((m) => m.index + 1);
    const kind = Math.floor(r() * 3);
    const at = spaces[Math.floor(r() * spaces.length)];
    const insert = kind === 0 ? " " : kind === 1 ? "\n  /* note */\n " : "z";
    const i = inside(before, at, 0);
    if (i < 0) continue;
    const edited = text.slice(0, at) + insert + text.slice(at);
    const after = units(file, edited);
    if (after.unreadable !== undefined || after.length !== before.length) continue;
    // The one move split.js allows: a rename of a name declared twice
    // re-keys its later declarations ("I #2" becomes "I").
    const renamed = (u) => before[i].key !== after[i].key && u.key.startsWith(before[i].key + " #");
    // And a line break before a statement's trailing comment moves the
    // comment to the next unit, whose key stays.
    const broken = (k) => insert.includes("\n") && k === i + 1 && after[k].key === before[k].key;
    const moved = before.findIndex((u, k) => k !== i && (u.key !== after[k].key || u.text !== after[k].text) && !renamed(u) && !broken(k));
    if (moved >= 0) {
      throw new Error(`seed ${seed}: ${file}: ${JSON.stringify(insert)} at ${at} in ${before[i].key} moved ${before[moved].key} -> ${after[moved].key}\nreplay: SEED=${seed} SEEDS=1 bun test split.test.js`);
    }
  }
});

// keys is the keys of the declared units (a statement with no name is the
// seeded property's: its key must not move when the unit before it is
// renamed).
const keys = (file, text) => units(file, text).map((u) => u.key).filter((k) => !k.startsWith("code"));

// An overload set is one unit: two would share one key (or be keyed by
// their place).
test("an overload set is one unit", () => {
  const us = units("o.ts", "export function f(x: string): string;\nexport function f(x: any) { return x }\nfunction g(): void;\nfunction g() {}\n");
  expect(us.map((u) => u.key)).toEqual(["f", "g"]);
  expect(us[0].text).toBe("export function f(x: string): string;\nexport function f(x: any) { return x }\n");
});

// Keys are names, not places: a unit added before another keeps the
// other's key.
test("a key is the declared names, never the offset", () => {
  expect(keys("k.ts", "export const a = 1, b = 2;\nf();\nclass C {}\nexport default 1;\ng();\n")).toEqual(["a,b", "C", "default"]);
  expect(keys("k.ts", "type Z = 1;\nexport const a = 1, b = 2;\nf();\nclass C {}\nexport default 1;\ng();\n")).toEqual(["Z", "a,b", "C", "default"]);
});

// The comment above a statement is its unit's; the comment after a
// statement on its last line stays with that statement.
test("a leading comment belongs to its unit", () => {
  const us = units("c.ts", "const a = 1; // a's\n\n// b's\n/* also b's */\nconst b = 2; /* b's,\n over two lines */\nconst c = 3;\n");
  expect(us.map((u) => u.text)).toEqual(["const a = 1; // a's\n", "\n// b's\n/* also b's */\nconst b = 2; /* b's,\n over two lines */\n", "const c = 3;\n"]);
});

// The dialect is the extension's: JSX reads in .tsx, an angle cast in .ts.
test("a .tsx file is parsed as TSX, a .ts file as TypeScript", () => {
  expect(units("x.tsx", "const x = <div>{1}</div>;\n").unreadable).toBeUndefined();
  expect(units("x.ts", "const x = <T>y;\n").unreadable).toBeUndefined();
  expect(units("x.ts", "const x = <div>{1}</div>;\n").unreadable).toBeDefined();
});

// A declare module block is one unit, its statements its members.
test("a declare module block is one unit", () => {
  const us = units("m.d.ts", 'declare module "m" {\n  export const z: number;\n\n  export function y(): void;\n}\nexport {};\n');
  expect(us.map((u) => u.key)).toEqual(["module m", "export from "]);
  expect(us[0].members.map((m) => m.key)).toEqual(["z", "y"]);
});

// The members of a class, an interface, an enum, an object and an import.
test("members by name", () => {
  const m = (text) => units("m.ts", text)[0].members.map((x) => x.key);
  expect(m("class C { static a = 1; get b() { return 1 } set b(x) {} m(): void; m() {} }")).toEqual(["static a", "get b", "set b", "m"]);
  expect(m("interface I { a: string; b(): void }")).toEqual(["a", "b"]);
  expect(m("enum E { A, B = 2 }")).toEqual(["A", "B"]);
  expect(m("const o = { k: 1, m() {} } as const;")).toEqual(["k", "m"]);
  expect(m('import { a, b as c } from "x";')).toEqual(["a", "b as c"]);
});

test("the protocol: units, Unreadable, NotProvided", () => {
  const lines = serve(
    [
      JSON.stringify({ _tag: "UnitsOf", file: "a.ts", text: "const a = 1;\n" }),
      JSON.stringify({ _tag: "UnitsOf", file: "b.ts", text: "const = ;\n" }),
      JSON.stringify({ _tag: "SitesOf", snap: { root: "", layer: [] }, of: "a" }),
    ].join("\n") + "\n",
  )
    .trim()
    .split("\n")
    .map((l) => JSON.parse(l));
  expect(lines[0]).toEqual({ _tag: "UnitsAre", units: [{ key: "a", kind: "variable", text: "const a = 1;\n", exported: false, members: [] }] });
  expect(lines[1]._tag).toBe("Unreadable");
  expect(lines[2]).toEqual({ _tag: "NotProvided", op: "Sites" });
});
