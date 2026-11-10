ready
Protocol: supplied orchestrate worker protocol (implementor).
Spec: /home/exedev/.cache/baton/wf10/mapper-gaps/E7/spec.md; ../brief-2.md E7.
Head: 1bc1d071 (wf10/e7: implementation 6f6537ca, then a merge of E6's
request rewording 2256a7fc; this request is committed after it as a copy
under tmp/orchestrate, no product change).
Base: 2256a7fc (wf10/e6 head). Diff stat 2256a7fc..1bc1d071: 8 files changed, 91 insertions(+), 15 deletions(-).

## Changes by file
- internal/tsproj/project.go: float64 projects to `$Schema.Number`.
  Measured in a scratch probe on the pinned Effect 4.1.2: the JSON codec
  of `Schema.Number` writes a finite number as a number and NaN/Infinity/
  -Infinity as the strings "NaN", "Infinity", "-Infinity", reads them
  back, and refuses other strings and non-finite numbers. That is std
  json's rule (runtime/v1/json.go JSONAppendFloat/readFloat). No new
  schema code. float32 stays a diagnostic, with a new reason (see gaps).
- internal/tsproj/accept_test.go: the seeded contract gains
  `type Reading { Value float64 }`, `type Readings { Read() Reading }`,
  and Bundle fields `Reading Reading` (a seeded finite float64 per value)
  and `Samples []float64` (zero, 0.5, -2.25, 3.0, 1e21, 1e-7, -1.5e300,
  5e-324, NaN, +Inf, -Inf in every sequence). These go through the codec
  round trip under four loaders and the RPC echo (json, ndjson).
  New: a list of float texts (finite spellings including `1E5`, `1e308`,
  six seeded floats in e/E/f/g form; the three strings; `"nan"`,
  `"inf"`, `"1.5"`, `""`, `1e400`, `-1e400`, null, true, [1]). The Ergo
  program prints std json's verdict for each (`floats` mode); the TS
  client prints the mapped schema's verdict last; the harness compares
  them line by line, in all eight loader/serialization runs.
- testdata/runtime.mjs: the verdict loop. testdata/client.ts: tsc accepts
  `{value: 1.5}`; `@ts-expect-error` holds that `{value: "1.5"}` is refused.
- internal/tsproj/project_test.go: TestProjectionRefusesUnsupported kept
  its job (kill r3-refusal) with float32 as the unsupported shape; float64
  is now supported, so the old input no longer refused. It pins the new
  float32 text. r3-refusal is killed (mutants-1.log).
- testdata/mutants.json: e7-finite (Schema.Finite), e7-int (Schema.Int),
  e7-json (Schema.Json). All assertion-killed by TestTsProjectionAgrees.
- tools/ts/README.md, docs/spec/rpc.md: the float64 form and limits.

## Run and evidence
Acceptance (measured, at 1bc1d071): `ERGO_TS_TEST=1 GOMAXPROCS=1
GOMEMLIMIT=6GiB go test -p 2 -count=1 -timeout 10m ./internal/tsproj`,
exit 0 (in gate-1.log).
Red at base: red-1.log, `exit 1`: with the base project.go, tsc and the
loaders stop with the float64 diagnostic quoted in the brief.
Gate: /home/exedev/.cache/baton/wf10/mapper-gaps/E7/gate-1.log, last line
`gate exit 0` (diff check from 5f0fe8d7, gofmt, vet, tsproj with the TS
leg, residue, cache-coverage guard, closure sweep). This gate's residue
run also covers E6's reworded request at 2256a7fc (the first E7 gate run
found its old text; product code did not change for that failure).
Mutants: mutants-1.log, `judge exit 0` (e7-finite, e7-int, e7-json,
r3-refusal).
umask 022, share A1. Common.md lines 42-44 depart from the
worker-created-scope rule; ad hoc checks ran in its 8G kernel scope.
No process of this worktree remained after each check.

## Prior art
- Effect Schema: `Number` (non-finite as strings in its JSON codec) and
  `Finite` (refuses them); we take `Number` because std json writes the
  strings (measured in the probe).
- ReScript: `JSON.Encode.float` gives a JS number; JSON.stringify writes
  NaN/Infinity as null, so non-finite values are lost (inferred from the
  JS rule). Gleam `json.float` writes the number on the JS target the same
  way. Elixir Jason has no non-finite floats (BEAM floats are finite) and
  refuses them on encode. Ergo keeps protobuf's string form instead, and
  the mapped schema reads it.

## Known gaps
- float32 is a projection diagnostic: Go decodes into float64 and rounds
  or overflows to an infinity, which a JavaScript number cannot show, so
  a TS peer would hold a different value (inferred from the lowered decode).
  Decided by simpler-reversible: a later unit can admit it.
- A JavaScript -0 is written as `0` by JSON.stringify, while std json
  writes `-0`; the property does not draw -0 (inferred).
- No performance claim. The fixture is neutral. No flake observed.
