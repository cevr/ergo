// record.ts: SemverIsNode's recorded answers (TestSemverIsNode). It
// generates versions and npm ranges from node-semver's grammar, with
// near misses (leading zeros, empty and partial parts, stray operators,
// stars, build metadata, white space), and writes node-semver 7.8.5's
// answers, one JSON array per line, to node.jsonl:
//
//   ["valid", text, semver.valid(text) or null]
//   ["range", text, semver.validRange(text) or null]
//   ["satisfies", range, version, semver.satisfies(version, range)]
//   ["compare", a, b, semver.compare(a, b)]
//
// Run it again only to change the cases, from a folder where
// semver@7.8.5 is installed (bun add semver@7.8.5):
//
//   bun record.ts > node.jsonl
//
// The test reads the file; node is never run at test time.
const semver = require("semver");
if (semver.SEMVER_SPEC_VERSION !== "2.0.0" || require("semver/package.json").version !== "7.8.5") throw new Error("want semver 7.8.5");

// A seeded generator (mulberry32), so the file is the same each run.
let seed = 0x5e3e7;
const rnd = () => {
  seed = (seed + 0x6d2b79f5) | 0;
  let t = seed;
  t = Math.imul(t ^ (t >>> 15), t | 1);
  t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};
const int = (n: number) => Math.floor(rnd() * n);
const pick = <T>(xs: T[]): T => xs[int(xs.length)];

const nums = ["0", "0", "1", "1", "2", "3", "10", "12", "9007199254740991", "9007199254740990"];
const badNums = ["01", "00", "", "9007199254740992", "99999999999999999999", "-1", "1a"];
const num = () => (int(14) === 0 ? pick(badNums) : pick(nums));
const word = () => pick(["alpha", "beta", "rc", "x-y", "a1", "1a", "-", "pre-1", "Z"]);
const preId = () => (int(10) === 0 ? pick(["01", "", "0a", "9007199254740991"]) : int(2) ? pick(["0", "1", "2", "10", "11"]) : word());
const ids = (f: () => string) => Array.from({ length: 1 + int(3) }, f).join(".");
const pre = () => (int(3) === 0 ? "-" + ids(preId) : "");
const build = () => (int(6) === 0 ? "+" + ids(() => pick(["b", "001", "sha-1", "exp", ""])) : "");
const prefix = () => pick(["", "", "", "", "v", "=", " ", "vv", "=v"]);

const version = () => {
  const parts = [num(), num(), num()];
  if (int(12) === 0) parts.length = 1 + int(2);
  if (int(16) === 0) parts.push(num());
  return prefix() + parts.join(".") + pre() + build() + pick(["", "", "", "", " ", "x", "-"]).slice(0, int(6) === 0 ? 1 : 0);
};
const xr = () => (int(4) === 0 ? pick(["x", "X", "*"]) : int(40) === 0 ? pick(badNums) : pick(nums.slice(0, 8)));
const partialV = () => {
  const n = 1 + int(3);
  const parts = Array.from({ length: n }, xr);
  return pick(["", "", "", "", "v", "="]) + parts.join(".") + (n === 3 ? pre() : "") + build();
};
const op = () => (int(25) === 0 ? pick(["=<", "=>", "<>"]) : pick(["", "", "=", "<", "<=", ">", ">=", "~", "~>", "^", "^", "~"]));
const comparatorText = () => {
  switch (int(14)) {
    case 0:
      return pick(["*", "x", "", ">=0.0.0", "<0.0.0-0", "junk", "1.2.3*", "*1.2.3", ">=*", "<x", "~*", "^x"]);
    default:
      return op() + (int(8) === 0 ? " " : "") + partialV();
  }
};
const setText = () => {
  if (int(5) === 0) return partialV() + pick([" - ", " - ", "-", "  -  "]) + partialV();
  return Array.from({ length: 1 + int(3) }, comparatorText).join(pick([" ", " ", "  ", "\t"]));
};
const rangeText = () => {
  const sets = Array.from({ length: 1 + (int(3) === 0 ? 1 + int(2) : 0) }, setText);
  return pick(["", "", " "]) + sets.join(pick(["||", " || ", "|| "])) + pick(["", "", " "]);
};

// Versions near a range's own numbers, so satisfies is often true.
const near = (r: string) => {
  const found = r.match(/\d+/g) ?? ["1"];
  const a = Number(pick(found)) || 0;
  const b = int(3) ? int(4) : a;
  const parts = [String(int(2) ? a : a + 1), String(b), String(int(4))];
  return parts.join(".") + (int(3) === 0 ? "-" + pick(["0", "beta", "alpha.1", "rc.2", "1"]) : "");
};

const rows: unknown[][] = [];
for (let i = 0; i < 400; i++) {
  const v = version();
  rows.push(["valid", v, semver.valid(v)]);
}
const valid = rows.map((r) => r[2]).filter((x) => x) as string[];
for (let i = 0; i < 400; i++) {
  const a = pick(valid), b = int(4) === 0 ? a : pick(valid);
  const pr = (s: string) => s.replace(/-.*/, "") + "-" + ids(preId).replace(/^\.|\.$/g, "x");
  const c = int(3) === 0 ? pr(a) : b;
  if (!semver.valid(c)) continue;
  rows.push(["compare", a, c, semver.compare(a, c)]);
}
// Hand ranges whose desugaring turns on a zero part (^0.0.3 is
// <0.0.4-0, ^0.2.3 is <0.3.0-0), with versions at each edge.
const fixed = ["^0.0.3", "^0.0.3-beta", "^0.0", "^0.0.x", "^0.2.3", "^0.2", "^0", "~0.0.3", "~0.2", "~0", "^1.2.3", "~1.2.3", "0.0.3 - 0.0.5", ">0.0", "<=0.0"];
const edges = ["0.0.3", "0.0.4", "0.0.5", "0.0.6", "0.1.0", "0.2.3", "0.2.9", "0.3.0", "0.9.9", "1.0.0", "1.2.3", "1.3.0", "2.0.0", "0.0.3-beta", "0.0.4-0"];
for (const r of fixed) {
  rows.push(["range", r, semver.validRange(r)]);
  for (const v of edges) rows.push(["satisfies", r, v, semver.satisfies(v, r)]);
}
for (let i = 0; i < 700; i++) {
  const r = rangeText();
  let valid: string | null = null;
  try {
    valid = semver.validRange(r);
  } catch {
    valid = null;
  }
  rows.push(["range", r, valid]);
  if (valid === null) continue;
  for (let k = 0; k < 5; k++) {
    const v = near(r);
    rows.push(["satisfies", r, v, semver.satisfies(v, r)]);
  }
}
for (const r of rows) console.log(JSON.stringify(r));
