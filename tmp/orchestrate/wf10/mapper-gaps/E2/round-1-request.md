ready
Protocol: supplied orchestrate worker protocol (implementor).
Spec: /home/exedev/.cache/baton/wf10/mapper-gaps/E2/spec.md; ../brief.md E2.
Head: 946e8f606e9a8efca969eae6a46451bf347e869e.
Base: 0003eb62cc42109ad07acfc28d5ccac71a98955d.
Diff stat: 9 files changed, 52 insertions(+), 18 deletions(-).

Changes: the checker exports its existing codec admission as WireType; tsproj/project.go
selects exported standalone JSON records/unions. accept_test.go and the TS/JS
fixtures import and decode Point/Status from two files without an operation type.
project_test.go removes obsolete unused-float omission; machines remain guarded.
testdata/mutants.json adds e2-roots/e2-admission and updates r3b-reach.
tools/ts/README.md and docs/spec/rpc.md state the rule and existing TS limits.

Run: ERGO_TS_TEST=1 GOMAXPROCS=1 go test -p 2 -count=1 -timeout 10m
./internal/tsproj; exit 0 at head. Base red: TS2305 missing Point/Status,
red-1.log exit 1. Gate: /home/exedev/.cache/baton/wf10/mapper-gaps/E2/gate-1.log,
`gate exit 0` (focused tests/vet, checker -short tests, formatting, residue,
cache guard, closure sweep). mutants-1.log: complete mapper ledger assertion-killed,
`judge exit 0`. No test declaration/binary added. No own process remained.
umask 022, A1, 8G scope per common.md departure lines 42-44; private cache
removed at READY. No full gate or flake. Existing unsupported TS shapes remain
diagnostics, cross-file watch needs restart. No performance claim.
Full cold-verifier handoff and prior-art sources: same unit dir's request/notes.
