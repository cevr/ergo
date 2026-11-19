# ergo.dev/tn1

TN1, input version 2: a small ternary network over a host-neutral feature record, as an installable module (not std). Every value is an integer, so a finding has the same bits on every host and in every engine.

- `ParseRecord(text) Features` reads a record (`record.ergo` states the form): per line its kind (`Blank`, `Code`, `CommentOnly`, `DocComment`, `Directive`), its syntactic depth, the node kinds that start on it, two 64-bit word sketches and the comment's text; per declaration-sized node its span, name words, counts, handler shapes and a SimHash of its shape. Ergo's parser fills it (`internal/sketch/lint/features`, `go run ./scripts/tn1record`); a TypeScript host fills the same fields from ESTree.
- `ParseNet(text) Net` reads a weights file (`net.ergo` states the form); `Packed` and `ID` are its canonical bytes and their SHA-256, the model's name.
- `Run(net, record) []Finding` is the forward pass (`run.ergo` states it): line, score and probability in thousandths.
- `NarrationControl()`, `NarrationControlStop()` and `Seeded(seed, D, F, K, H)` write weights files from their arguments alone. A weights file may read `words stop` after its header: the comment's word planes then leave out `StopWords` (function words such as "the", "now", "each"), read from the line's comment text, so prose narration is measured by its content words. `NarrationControlStop` is the control with it (`testdata/vectors/nets/control-stop.tn1`).
- `Expected(records, nets)` writes the conformance vectors' findings. `testdata/vectors` holds the vectors: sources (`src/`), their records (`records/`, made by `scripts/tn1record`), networks (`nets/`, made by `tn1 gen`) and `expected.tsv`. This engine is the reference; another engine (the hand-written JavaScript one of oxlint-plugin-effect) must reproduce every line of `expected.tsv`. No engine is generated from this source: Go is Ergo's only target.

Tests (`ergo test .`): `VectorsAreReproduced` holds the engine to the vectors; the law `PermilleRises` holds the probability table. The record's own guarantees (it survives a reformat, its vectors' records are what the parser makes) are tested on the Go side, in `internal/sketch/lint/features`.

Install: `require ergo.dev/tn1 v…` in the program's `go.mod` with `replace ergo.dev/tn1 => ../../modules/tn1`, as `examples/tn1` does. It uses no Go.
