# tn1-lint

TN1's sidecar for TypeScript projects. It runs beside oxlint and prints its findings in oxlint's JSON form (`--format json`), so whatever reads oxlint's report reads these too:

```sh
oxlint --format json src > oxlint.json
tn1-lint lint --rule narration narration.tn1 src/*.ts > tn1.json
```

```sh
tn1-lint lint --rule NAME WEIGHTS FILE...   # findings, oxlint JSON; always exit 0 (advisory)
tn1-lint no-comments [--fix] FILE...        # ban non-directive comments in TS/TSX (or rewrite with --fix)
tn1-lint record FILE                        # the feature record of FILE (a FILE of - is the standard input)
tn1-lint project FILE...                    # each FILE's record version 3 in the project of FILEs, after "== FILE"
tn1-lint chunks FILE                        # each top-level statement: FROM TO (bytes)
tn1-lint serve                              # one JSON reply line per JSON request line, until EOF
tn1-lint --help                             # the usage; exit 0
```

It reads TypeScript itself, through the TypeScript host of TN1's feature record (`ergo.dev/tn1/ts`): a scanner (comments, strings, templates with nested `${}`, a regular expression told from a division, the braces open at each line), the record's rules stated over its tokens, and the file's top-level statements. The network and the engine are TN1's (`ergo.dev/tn1`), the ones `ergo lint` runs on Ergo source; no Ergo toolchain is needed beside the binary.

A finding's span is the comment on its line; its probability and the model's id (the first 12 hex digits of `tn1 id`) are in its message, and its severity is `advice`.

Cases (the `ts` leg: Bun and the pinned `typescript` package of `tools/ts`):

- `agree` is the law of the chunks: on seeded programs (imports, templates, regular expressions, ASI across `?:` and `.pipe` chains, decorators, labels, `Array<Array<X>>`, blocks followed by `;`), the statements `tn1-lint chunks` reads are the ones the TypeScript compiler's parser reports, from `getStart()` to `end`. A failing seed prints the program, both lists and `SEED=n bun ts/agree.ts`.
- `records` holds the host to the conformance vectors (`modules/tn1/testdata/vectors`): `tn1-lint record` of each `src/*.ts.txt` is its `records/*.record`, byte for byte; and `tn1-lint project` over each `projects/P` is its `records/P-*.record` (record version 3: the file's package, the file itself and its declarations' callers in the project's import graph). oxlint-plugin-effect's ESTree extractor is held to the same records, and its JavaScript engine to `expected.tsv`.
- `lint` is the oxlint report of the narration control on the vectors' TypeScript source.
- `usage` is a bad command line; `help` is `--help`.
- `stdin` is `lint` of the standard input (`-`), the way the effect-oxlint rule `tn1Lint` passes a buffer.
- `serve` is the long-lived mode: each line of its input is a request `{"rule","weights","path","text"}` and each line of its output the reply `{"diagnostics","error"}`, the diagnostics `lint` reports for that text. A weights file is parsed once per run; a request that fails (no such weights file, a line that is not a request) is an `error` reply and the next line is read. The package's JS wrapper keeps one `serve` per lint run (below).
- `wire` holds the package's wire module (`tools/npm/tn1-lint/wire.js`) and `lintSync` to the replies the `serve` and `lint` cases record: each decodes as itself; each with a field taken out or of the wrong kind, or with an error beside diagnostics, is refused; `lintSync` over a serve that writes such a line throws (`ts/fake-serve.sh`).
- `wire-current` checks that `wire.js` and `wire.d.ts` are what `ts/wire.ts` generates from this program's Ergo types `Request`, `Reply` and `Report` (through `ergo tsmap`). Regenerate with `bun ts/wire.ts --write ../../tools/npm/tn1-lint` in this folder (ERGO names the ergo binary).

The npm package `@cvr/tn1-lint` (`tools/npm/tn1-lint`) ships this program's static binary per platform, and `lint.js`, a synchronous `lintSync` for an oxlint rule that holds one `serve` process per lint run through a worker thread and `Atomics.wait` (`node scripts/npm-sidecar-check.js BINARY N` counts its process starts against one `lint` per file, compares the two paths' findings, and checks that no `serve` outlives the run): `sh scripts/npm.sh tn1-lint` builds and dry-packs it, `sh scripts/npm-check.sh tn1-lint DIR` installs the packs with bun and npm and runs `tn1-lint --help`. The `workload` file gives `scripts/opcount` the record of N bytes of TypeScript against the hand Go control in `cmd/ergo/testdata/tn1lint/control`; `cmd/ergo/testdata/tn1lint/floor` is its floor.

What it shows: closed unions for the token kinds and the commands, exhaustive `match` over them, typed failures (`os.Exit`, `tn1.Malformed`) and a record (`Report`) whose field keys are oxlint's names.
