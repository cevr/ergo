#!/usr/bin/env node
// spike-where benchmark program generator.
//
//   node gen.mjs --style a|b --sites N --types K [--layout distinct|shared]
//
// Prints an Ergo program with K record types and N constrained call sites
// of a generic three-element sort. Style b is where-clause dispatch
// (`where T.compare: fn(T, T) -> i64`, receiver methods resolved by the
// checker); style a passes the ordering explicitly as a function value, which
// is Effect's Order<T> (a function) and the one-method dictionary of option
// (a). Both lower to the same Go and JavaScript, so the comparison isolates
// the checker's resolution work. `--layout distinct` gives every record its
// own field name, hence its own Go GC shape (the SH1 fixture's pattern);
// `--layout shared` gives every record the same fields.
const args = Object.fromEntries(
  process.argv.slice(2).reduce((pairs, arg, i, all) => (arg.startsWith("--") ? [...pairs, [arg.slice(2), all[i + 1]]] : pairs), []),
);
const style = args.style;
const sites = Number(args.sites);
const types = Number(args.types);
const layout = args.layout ?? "distinct";
if (!["a", "b"].includes(style) || !(sites > 0) || !(types > 0) || !["distinct", "shared"].includes(layout)) {
  console.error("usage: gen.mjs --style a|b --sites N --types K [--layout distinct|shared]");
  process.exit(2);
}
const field = (k) => (layout === "distinct" ? `f${k}` : "n");
const out = [];
out.push("import I64 \"ergo/i64\"", "");
out.push("record Three<T: type> {", "    a: T;", "    b: T;", "    c: T", "}");
if (style === "b") {
  out.push(
    "fn sort2<T: type>(x: T, y: T) -> Three<T> where T.compare: fn(T, T) -> i64 {",
    "    if T.compare(y, x) < 0 {",
    "        Three { a: y; b: x; c: x }",
    "    } else {",
    "        Three { a: x; b: y; c: y }",
    "    }",
    "}",
    "fn sort3<T: type>(xs: Three<T>) -> Three<T> where T.compare: fn(T, T) -> i64 {",
    "    let ab = sort2(xs.a, xs.b)",
    "    let bc = sort2(ab.b, xs.c)",
    "    let lo = sort2(ab.a, bc.a)",
    "    Three { a: lo.a; b: lo.b; c: bc.b }",
    "}",
  );
} else {
  out.push(
    "fn sort2<T: type>(x: T, y: T, compare: fn(T, T) -> i64) -> Three<T> {",
    "    if compare(y, x) < 0 {",
    "        Three { a: y; b: x; c: x }",
    "    } else {",
    "        Three { a: x; b: y; c: y }",
    "    }",
    "}",
    "fn sort3<T: type>(xs: Three<T>, compare: fn(T, T) -> i64) -> Three<T> {",
    "    let ab = sort2(xs.a, xs.b, compare)",
    "    let bc = sort2(ab.b, xs.c, compare)",
    "    let lo = sort2(ab.a, bc.a, compare)",
    "    Three { a: lo.a; b: lo.b; c: bc.b }",
    "}",
  );
}
for (let k = 1; k <= types; k++) {
  const f = field(k);
  out.push(`record R${k} {`, "    name: string;", `    ${f}: i64`, "}");
  if (style === "b") out.push(`fn (x: R${k}) compare(o: R${k}) -> i64 {`, `    x.${f} - o.${f}`, "}");
  else out.push(`fn R${k}_compare(x: R${k}, o: R${k}) -> i64 {`, `    x.${f} - o.${f}`, "}");
}
const chunk = 50;
const chunks = Math.ceil(sites / chunk);
let site = 0;
for (let c = 0; c < chunks; c++) {
  out.push(`fn chunk${c}() -> i64 {`);
  const terms = [];
  for (let i = 0; i < chunk && site < sites; i++, site++) {
    const k = (site % types) + 1;
    const f = field(k);
    const rec = (v) => `R${k} { name: "r"; ${f}: ${v} }`;
    const call = style === "b" ? `sort3(Three { a: ${rec(3)}; b: ${rec(1)}; c: ${rec(2)} })` : `sort3(Three { a: ${rec(3)}; b: ${rec(1)}; c: ${rec(2)} }, R${k}_compare)`;
    out.push(`    let s${i} = ${call}`);
    terms.push(`s${i}.a.${f}`);
  }
  out.push(`    ${terms.join(" + ")}`, "}");
}
const calls = Array.from({ length: chunks }, (_, c) => `chunk${c}()`);
out.push("effect fn main() -> string {", `    I64.format(${calls.join(" + ")})`, "}");
console.log(out.join("\n"));
