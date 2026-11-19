// The chunks law: on seeded TypeScript programs, the top-level statements
// tn1-lint reads (ergo.dev/tn1/ts, Statements) are the statements the
// TypeScript compiler's own parser (typescript-go, through the pinned
// typescript package's API) reports: the same count, and each one from
// its first token (getStart) to its end. A failing seed prints the
// program, both lists and the command that replays it.
//
//   bun ts/agree.ts            seeds 1..SEEDS (default 150)
//   SEED=n bun ts/agree.ts     one seed
import { API } from "typescript/async"
import { mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const bin = process.env.ERGO_BIN
if (!bin) throw new Error("ERGO_BIN names the built tn1-lint")

// splitmix64, so a seed is the same program on every host.
function rng(seed: number) {
  let s = BigInt(seed)
  return (n: number) => {
    s = (s + 0x9e3779b97f4a7c15n) & 0xffffffffffffffffn
    let z = s
    z = ((z ^ (z >> 30n)) * 0xbf58476d1ce4e5b9n) & 0xffffffffffffffffn
    z = ((z ^ (z >> 27n)) * 0x94d049bb133111ebn) & 0xffffffffffffffffn
    z = z ^ (z >> 31n)
    return Number(z % BigInt(n))
  }
}

// Statements, each valid TypeScript on its own; i makes names unique.
const statements: Array<(i: number) => string> = [
  () => `import { Effect, Layer } from "effect"`,
  (i) => `import * as S${i} from "effect/Schema"`,
  () => `import type { Config } from "./config"`,
  (i) => `export const a${i} = 1`,
  (i) => `const s${i} = "a // not a comment /* nor this */"`,
  (i) => `const t${i} = \`x \${\`nested \${1 + 2}\`} } {\n  still the template\``,
  (i) => `const r${i} = /a\\/b[/]c/gi.test("x")`,
  (i) => `const d${i} = a / b / 2`,
  (i) => `const f${i} = (x: number) => x / 2`,
  (i) => `const o${i} = { a: 1, "b}": "}", [k]: 3, ...rest }`,
  (i) => `const e${i} = Effect.succeed(1)\n  .pipe(Effect.map((n) => n + 1))\n  .pipe(Effect.orDie)`,
  (i) => `const c${i} = cond\n  ? yes\n  : no`,
  (i) => `function g${i}(a: string, b = 2): number {\n  if (a) {\n    return 1\n  }\n  return b\n}`,
  (i) => `export default class C${i} extends Base {\n  x = 1;\n  m() { return /}/ }\n}`,
  (i) => `interface I${i} {\n  a: string\n  b?: number\n}`,
  (i) => `type T${i} =\n  | { _tag: "A" }\n  | { _tag: "B" }`,
  (i) => `type U${i}<X> = Array<Array<X>>`,
  (i) => `enum E${i} { A, B = 2 }`,
  (i) => `declare module "m${i}" {\n  export const x: number\n}`,
  (i) => `namespace N${i} { export const y = 1 }`,
  (i) => `@decorate()\nclass D${i} {}`,
  () => `try { run() } catch (e) { throw e } finally { done() }`,
  () => `if (x) {\n  one()\n} else if (y) {\n  two()\n} else {\n  three()\n}`,
  () => `for (const k of xs) { console.log(k) }`,
  () => `do { i++ } while (i < 3)`,
  () => `switch (x) {\n  case 1:\n    break\n  default:\n}`,
  (i) => `let v${i}: Array<Array<string>> = []`,
  (i) => `const gen${i} = async function* () { yield 1 }`,
  (i) => `x${i} = a\n  ? b\n  : c`,
  () => `export { a, b as c }`,
  () => `export * from "./y"`,
  () => `void 0`,
  (i) => `abstract class A${i} { abstract m(): void }`,
  () => `export type { Config as Settings }`,
  (i) => `const big${i} = 10_000n + 0x1Fn + 1e-3 + .5`,
  () => `obj.method()\n  .other()`,
  (i) => `function over${i}(a: string): void;`,
  (i) => `export async function run${i}() {\n  await Promise.resolve()\n}`,
  () => `label: for (;;) { break label }`,
  (i) => `const q${i} = (a) / 2 / (b)`,
  (i) => `const arr${i} = [\n  1,\n  2,\n]`,
  (i) => `let n${i} = 0\nn${i}++`,
]

// Separators between two statements: each holds a line break, so no two
// statements share a line unless a ; ends the first.
const separators = ["\n", ";\n", "\n\n", "\n// a line comment\n", "\n/** a doc comment */\n", " /* inline */\n", "; ", "\n/*\n block\n*/\n"]

function program(seed: number): string {
  const next = rng(seed)
  const count = 1 + next(12)
  let src = next(4) === 0 ? "// leading comment\n" : ""
  for (let i = 0; i < count; i++) {
    src += statements[next(statements.length)](i)
    if (i + 1 < count) {
      let sep = separators[next(separators.length)]
      // "; " keeps the next statement on the line: only after a statement
      // whose text ends the line it is on.
      src += sep
    }
  }
  return src + (next(2) === 0 ? "\n" : "")
}

const api = new API()
const dir = mkdtempSync(join(tmpdir(), "tn1-lint-agree-"))
const one = process.env.SEED
const seeds = one ? [Number(one)] : Array.from({ length: Number(process.env.SEEDS ?? 150) }, (_, k) => k + 1)
let failed = false
for (const seed of seeds) {
  const src = program(seed)
  const file = join(dir, `p${seed}.ts`)
  writeFileSync(file, src)
  const retained = await api.createSourceFile(file, src)
  const sf = retained.sourceFile
  const want = sf.statements.map((s: any) => `${s.getStart(sf)} ${s.end}`)
  retained.dispose?.()
  const run = Bun.spawnSync([bin, "chunks", file])
  if (run.exitCode !== 0) throw new Error(`tn1-lint chunks ${file}: exit ${run.exitCode}: ${run.stderr}`)
  const got = run.stdout.toString().split("\n").filter((l) => l !== "")
  if (got.join("\n") !== want.join("\n")) {
    failed = true
    console.log(`seed ${seed}: tn1-lint and TypeScript disagree\n--- program\n${src}\n--- tn1-lint\n${got.join("\n")}\n--- typescript\n${want.join("\n")}\nreplay: SEED=${seed} bun ts/agree.ts`)
    break
  }
}
await api.close()
if (failed) process.exit(1)
console.log("agree")
