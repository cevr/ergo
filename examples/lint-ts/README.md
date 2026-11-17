# lint-ts

TN1's sidecar for TypeScript projects. It runs beside oxlint and prints its findings in oxlint's JSON form (`--format json`), so whatever reads oxlint's report reads these too:

```sh
oxlint --format json src > oxlint.json
lint-ts lint --rule narration narration.tn1 src/*.ts > tn1.json
```

```sh
lint-ts lint --rule NAME WEIGHTS FILE...   # findings, oxlint JSON; always exit 0 (advisory)
lint-ts record FILE                        # the feature record of FILE
lint-ts chunks FILE                        # each top-level statement: FROM TO (bytes)
```

It reads TypeScript itself, through the TypeScript host of TN1's feature record (`ergo.dev/tn1/ts`): a scanner (comments, strings, templates with nested `${}`, a regular expression told from a division, the braces open at each line), the record's rules stated over its tokens, and the file's top-level statements. The network and the engine are TN1's (`ergo.dev/tn1`), the ones `ergo lint` runs on Ergo source; no Ergo toolchain is needed beside the binary.

A finding's span is the comment on its line; its probability and the model's id (the first 12 hex digits of `tn1 id`) are in its message, and its severity is `advice`.

Cases (the `ts` leg: Bun and the pinned `typescript` package of `tools/ts`):

- `agree` is the law of the chunks: on seeded programs (imports, templates, regular expressions, ASI across `?:` and `.pipe` chains, decorators, labels, `Array<Array<X>>`, blocks followed by `;`), the statements `lint-ts chunks` reads are the ones the TypeScript compiler's parser reports, from `getStart()` to `end`. A failing seed prints the program, both lists and `SEED=n bun ts/agree.ts`.
- `records` holds the host to the conformance vectors (`modules/tn1/testdata/vectors`): `lint-ts record` of each `src/*.ts.txt` is its `records/*.record`, byte for byte. oxlint-plugin-effect's ESTree extractor is held to the same records, and its JavaScript engine to `expected.tsv`.
- `lint` is the oxlint report of the narration control on the vectors' TypeScript source.
- `usage` is a bad command line.

What it shows: closed unions for the token kinds and the commands, exhaustive `match` over them, typed failures (`os.Exit`, `tn1.Malformed`) and a wire record (`Report`) whose field keys are oxlint's.
