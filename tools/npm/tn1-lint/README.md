# @cevr/tn1-lint

`tn1-lint`: an advisory linter for TypeScript that runs beside oxlint. Each rule is decided by a small all-integer (ternary) network over a feature record of the source, so a finding is a function of the source bytes and the weights alone: the same on every host, local and free. Findings are printed in oxlint's JSON form (`oxlint --format json`) and never change the exit code.

```sh
npm install -D @cevr/tn1-lint      # or: bun add -d @cevr/tn1-lint
oxlint && tn1-lint lint --rule narration narration.tn1 src/*.ts > tn1.json
tn1-lint record src/a.ts           # the feature record a network reads
tn1-lint chunks src/a.ts           # each top-level statement: FROM TO (bytes)
tn1-lint --help
```

A rule's weights are a `.tn1` file named on the command line; this package ships none yet. A network ships only once it passes its suite and a hold-out. The source, the record's format and the engine are in [examples/tn1-lint](https://github.com/cevr/ergo/tree/main/examples/tn1-lint) and [modules/tn1](https://github.com/cevr/ergo/tree/main/modules/tn1).

The package holds no program of its own. Its optional dependency for the platform (`@cevr/tn1-lint-linux-x64`, `@cevr/tn1-lint-linux-arm64` or `@cevr/tn1-lint-darwin-arm64`) holds the static binary, and the install puts it at `bin/tn1-lint`, so `tn1-lint` starts no Node process.
