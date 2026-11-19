# @cvr/tn1-lint

`tn1-lint`: an advisory linter for TypeScript that runs beside oxlint. Each rule is decided by a small all-integer (ternary) network over a feature record of the source, so a finding is a function of the source bytes and the weights alone: the same on every host, local and free. Findings are printed in oxlint's JSON form (`oxlint --format json`) and never change the exit code.

```sh
npm install -D @cvr/tn1-lint      # or: bun add -d @cvr/tn1-lint
oxlint && tn1-lint lint --rule narration narration.tn1 src/*.ts > tn1.json
tn1-lint record src/a.ts           # the feature record a network reads
tn1-lint chunks src/a.ts           # each top-level statement: FROM TO (bytes)
tn1-lint --help
```

From an oxlint rule, which cannot wait on a promise, call it synchronously; one `tn1-lint serve` process answers every call of the lint run (started on the first call by a worker thread, ended when the run ends):

```js
const { lintSync } = require("@cvr/tn1-lint");
lintSync({ rule: "narration", weights: "narration.tn1", path: "src/a.ts", text }); // diagnostics, oxlint JSON
```

The request, the reply and the report are tn1-lint's own Ergo types (`Request`, `Reply`, `Report`), generated into `@cvr/tn1-lint/wire` (`encodeRequest`, `decodeReply`, `decodeReport`, with their `.d.ts`; no dependencies). A reply or report that is not that JSON throws `UnreadableReply`.

A failed call throws an `Error` with tn1-lint's reason. When the `serve` process cannot start (a fork that fails with `ENOMEM` in a process holding much memory, a binary that cannot run, or one that exits three times), that call and every later one throw at once, with `error.code` set to `"TN1_SIDECAR_DOWN"`: no file of the run can be linted, so a caller fails the run once rather than reporting it per file.

A rule's weights are a `.tn1` file named on the command line; this package ships none yet. A network ships only once it passes its suite and a hold-out. The source, the record's format and the engine are in [examples/tn1-lint](https://github.com/cevr/ergo/tree/main/examples/tn1-lint) and [modules/tn1](https://github.com/cevr/ergo/tree/main/modules/tn1).

The package holds no program of its own. Its optional dependency for the platform (`@cvr/tn1-lint-linux-x64`, `@cvr/tn1-lint-linux-arm64` or `@cvr/tn1-lint-darwin-arm64`) holds the static binary, and the install puts it at `bin/tn1-lint`, so `tn1-lint` starts no Node process.
