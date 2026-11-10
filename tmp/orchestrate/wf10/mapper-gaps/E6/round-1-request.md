ready
Protocol: supplied orchestrate worker protocol (implementor).
Spec: /home/exedev/.cache/baton/wf10/mapper-gaps/E6/spec.md; ../brief-2.md E6.
Head: 05b8dff5 (wf10/e6; the implementation commit; this request is
committed after it as a copy under tmp/orchestrate, no product change).
Base: cf073cde. Diff stat cf073cde..05b8dff5: 7 files changed, 94 insertions(+), 7 deletions(-).

## Changes by file
- internal/tsproj/project.go: std `json.Value` (identified by std package
  identity, not by name) projects to `$Schema.Json` before the Named walk.
  So the mapper no longer walks Value's six variants and stops at Num's
  by-name codec. `Optional[Value]` and `Nullable[Value]` follow the E1 rule
  because the field wrappers are applied before this case. New helper
  `stdJSON`; `jsonArgument` uses it.
- internal/tsproj/accept_test.go: the existing seeded property gains
  `type Snapshot { Body json.Optional[json.Value] }` and
  `type Snapshots { Get() Snapshot }`, and a `Snap` field in Bundle. Every
  sequence has an absent body and each of the six kinds at the top level;
  `jsonValue` draws nested arrays and objects from the seed. The Go codec
  text decodes with the mapped schema, re-encodes to the same parsed JSON
  (Bun, Node, Vite, esbuild loaders), and goes through the RPC echo to the
  Ergo server and back (json and ndjson).
- testdata/runtime.mjs: undefined, a function, a Date, a Map, NaN, an
  array holding undefined and an object holding a symbol are refused at
  decode and at encode.
- testdata/client.ts: tsc accepts `{}`, `{body: null}` and a nested value;
  `@ts-expect-error` holds that tsc refuses a function as the body.
- testdata/mutants.json: e6-value (the special case disabled, which is the
  base behaviour) and e6-json (`Schema.Unknown`). Both assertion-killed by
  TestTsProjectionAgrees. No new test declaration.
- tools/ts/README.md, docs/spec/rpc.md: the json.Value form and its limits.

## Run and evidence
Acceptance (measured, at 05b8dff5): `ERGO_TS_TEST=1 GOMAXPROCS=1
GOMEMLIMIT=6GiB go test -p 2 -count=1 -timeout 10m ./internal/tsproj`,
exit 0 (in gate-1.log). Also green with ERGO_TS_SEED=2, 7 and 13.
Red at base: red-1.log, `exit 1`: tsc and the loaders stop with
`TypeScript wire projection of json.Num: by-name codecs need an explicit
wire projection`.
Gate: /home/exedev/.cache/baton/wf10/mapper-gaps/E6/gate-1.log, last line
`gate exit 0` (/tmp/opencode/mapper-gaps-e6-gate.sh: diff check, gofmt,
vet, tsproj with the TS leg, residue, cache-coverage guard, closure sweep).
Mutants: mutants-1.log, `judge exit 0`.
umask 022, share A1. Common.md lines 42-44 depart from the
worker-created-scope rule; ad hoc checks ran in its 8G kernel scope.
No process of this worktree remained after each check.

## Prior art
- Effect 4.1.2 `Schema.Json` (measured, probe in scratch): admits null,
  booleans, finite numbers, strings, arrays and objects at any depth;
  refuses undefined, functions, Date, Map, NaN, symbols. We use it as is.
- ReScript `JSON.t` is an untagged JSON value in JS; Gleam `json.Json` is
  an opaque builder and decoding goes through `dynamic`; Elixir Jason
  decodes to plain terms (maps lose repeated keys unless `objects:
  :ordered_objects`). All three expose JSON's own shape, not a tagged
  union, which this projection follows (inferred from their docs).

## Known gaps
- A JavaScript peer keeps the parsed value, not what json.Value keeps in
  Go: number spelling and digits beyond float64 precision, repeated keys
  (last wins in JSON.parse), the order of integer-like keys. Documented in
  the README. The generator uses JavaScript number spellings for this
  reason (inferred: the Go harness compares number text).
- A number spelling beyond the float64 range is a Go json.Value but
  JSON.parse gives an infinity, which Schema.Json refuses (inferred from
  the measured refusal of Infinity).
- No performance claim. The fixture is neutral. No flake observed.
