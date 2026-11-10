ready

# B5 round 1

Protocol: supplied orchestrate implementor protocol. Spec: `/home/exedev/.cache/baton/wf10/v1-L8/plan.md` B4 Scoped remainder, start message, and `decisions.md` last entry (B5 option 3); B5/spec.md does not exist. Head: e43c8bc56c81aeaf93daae79d0f0b4bf82b78ad1. Branch: wf10/l8-b5.

- Base ae3687af014a6e41c008e1ad65ff5d0d952b6b37; implementation head e43c8bc56c81aeaf93daae79d0f0b4bf82b78ad1. `git diff --stat ae3687af..HEAD | tail -1`: `24 files changed, 205 insertions(+), 74 deletions(-)`.
- `runtime/rt/scope.go`: Scoped takes explicit generic result glue `func(T)`, nil for plain results. A successful body whose scope shutdown fails has its result released once, masked, after scope releases. Result-release defects join the exit. Failed and panicking bodies give no valid result.
- `runtime/rt/*_test.go`: migrate callers and add TestScopedResultReleaseModel, a seeded public-boundary conservation model over success, failure, panic, stop, masking and release defects. It includes an aggregate without Close and checks that no failed candidate is released. No Go build or file reads.
- `runtime/rt/testdata/mutants.json`: skipped result release, failed candidate released, unmasked result release, result-release defect lost. Each mutant is uniquely killed by TestScopedResultReleaseModel; existing scope tests have only plain results. Measured: all four killed (`/home/exedev/.cache/baton/wf10/v1-L8/B5/mutants-1.log`, `exit 0`). Runtime suites are not supported by the compiler-only ledger runner, so `/tmp/opencode/l8b/b5-mutants.ts` applied the ledger edits in a scratch copy.
- `prototypes/actorlib/{actor_test.go,examples/examples_test.go,review3_test.go}`: migrate Scoped calls; all three Gap 47 regressions now use result-aware Scoped boundaries and assert one release, not zero. Raw Run and bare deferred End still have no result glue. No actor reply protocol or resource admission change.
- `prototypes/sketch9/sketch/trace_probe_test.go`: migrate the generated probe's Scoped call.
- `docs/spec/{resources,runtime,README}.md`: Scoped guarantee and caller obligations; v1 std machine actor refuses resource-bearing state, signals and decisions under D-B. Latest/Final alias decisions, so a resource reply protocol needs a later design.

## Run and gate

Acceptance at the implementation head: `GOMAXPROCS=2 go test -p 2 -timeout 20m -count=1 -race -run '^TestScopedResultReleaseModel$' ./runtime/rt`: `ok ergo.local/prototype/runtime/rt 1.033s`, `exit 0`.

Round gate: `zsh /tmp/opencode/l8b/b5-gate.sh`; `/home/exedev/.cache/baton/wf10/v1-L8/B5/gate-1.log`, final `exit 0`. Includes empty gofmt output, runtime/rt and runtime/v1 tests, actorlib and its examples, internal/residue, touched-package vet, TestTestCommandsHaveCacheCoverage, and the nested sketch9 probe with ERGO_REPO_ROOT set to this worktree. The nested package uses `go -C prototypes/sketch9`; an earlier combined command incorrectly named it from the main module and failed setup. No failed assertion was retried or removed.

Red: `/home/exedev/.cache/baton/wf10/v1-L8/B5/red-1.log`, `exit 1`, seed 12: returned aggregate lost when shutdown failed, count zero instead of one. Run after signature/caller migration only, with base Scoped behavior. The skip-release mutant restores that behavior and fails with the same seed.

## Known gaps and protocol

- Inferred: Scoped protects its own boundary, not a raw Run caller that gives away an owned result without a transport. Generated v1 tasks and frames already retain that ownership. No performance claim or unsafe change.
- Common.md:44 explicitly departs from the launch rule: checks used the 8GiB systemd scope, nice 19, umask 022, GOMEMLIMIT=6GiB and private GOCACHE; share C GOMAXPROCS=2, -p 2. Focused gate only; full/merge/Mac belongs to the batch. No paid calls.
- Measured: own checks ended, no own child left in ps; other workers were not signalled. The private cache and mutant scratch are removed at READY by the Written cleanup script. No new file-reading tests; no closure sweep. No test deletion.
- C1 is next, not claimed here. #197's Scoped remainder has runtime evidence; std actor resource admission stays refused. No issue or HITL state changed.

The fuller working request is `/home/exedev/.cache/baton/wf10/v1-L8/B5/round-1-request.md`; this tracked request records the same implementation and verdict.
