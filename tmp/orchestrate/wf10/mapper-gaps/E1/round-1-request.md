ready
Protocol: supplied orchestrate worker protocol (implementor).
Spec: /home/exedev/.cache/baton/wf10/mapper-gaps/E1/spec.md and ../brief.md E1.
Head: ffcbbb9c43ff1fa28ed2b59fe8dc51254b9fe09f. Base: c590c1bb.
Diff stat: 7 files changed, 72 insertions(+), 9 deletions(-).

Changes: project.go maps Optional record fields to optionalKey, Nullable to
NullOr, and keeps Optional tagged outside fields. accept_test.go and
testdata/client.ts,runtime.mjs extend the existing seeded codec/schema/RPC
agreement property with Draft, View, nested wrappers, generic fields and slices.
testdata/mutants.json adds e1-optional, e1-nullable, e1-position, all killed by
TestTsProjectionAgrees. tools/ts/README.md and docs/spec/rpc.md document it.

Run: ERGO_TS_TEST=1 GOMAXPROCS=1 GOMEMLIMIT=6GiB go test -p 2 -count=1
-timeout 10m ./internal/tsproj; exit 0 at head (private cache used and removed).
Gate: /home/exedev/.cache/baton/wf10/mapper-gaps/E1/gate-1.log,
`gate exit 0`: focused vet/tests, gofmt, residue, cache-coverage guard and
closure sweep. Red at base: red-1.log exit 1 (TS2741/TS2322 and runtime missing
key); mutants-1.log `judge exit 0` (three assertion kills). umask 022, A1,
8G scope per common.md's explicit departure lines 42-44. No own process remained.
No full gate: batch owner runs it. No new test declaration, no flake.

Known gaps: prior TS limits (recursive/refined/by-name/float/generic contracts,
cross-file watch restart). No performance claim. Prior-art sources and the
full cold-verifier handoff are in the same unit dir's notes.md and request.
