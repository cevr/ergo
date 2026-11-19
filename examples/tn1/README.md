# tn1

The command line of TN1, the small integer network behind Ergo's advisory lint rules, on the module `ergo.dev/tn1` (`modules/tn1`). It reads feature records, not source: a host's parser makes the record (Ergo's: `go run ./scripts/tn1record`; a TypeScript host fills the same record from its own tree), so one network and one engine serve both languages.

```sh
tn1 gen control                     # the narration control's weights
tn1 gen control-stop                # the same, function words left out of the comment (`words stop`)
tn1 gen seeded SEED D F K H         # a seeded network of that shape
tn1 id WEIGHTS                      # the model id: SHA-256 of the packed weights
tn1 run WEIGHTS RECORD              # the findings: LINE SCORE PERMILLE
tn1 vectors DIR                     # the conformance vectors' findings
tn1 score WEIGHTS RECORDS EXPECTED [train|holdout]
```

- `vectors` writes what every other engine must reproduce (the hand-written JavaScript engine of oxlint-plugin-effect): the findings of `DIR/nets/*.tn1` on `DIR/records/*.record`. The case `vectors` writes `modules/tn1/testdata/vectors/expected.tsv` again from its inputs, byte for byte.
- `score` holds a network to a labelled suite: recall and precision of at least 0.950 (`train`) or 0.900 (`holdout`), and no false finding on a code line. It exits 1 below the bar. A suite is the law of a network; weights that fail it do not ship.

Package `suite` is the scoring; `cmd/tn1/main.ergo` is the only place that chooses a file system (the host's).

What it shows: closed unions for every kind the record carries (line kinds, node kinds, handler shapes), exhaustive `match` over them, typed failures with their place (`Malformed`, `BadFile`, `BadLabel`), and the law `PermilleRises` beside the table it is about.
