ready

Protocol: supplied orchestrate worker/implementor protocol.
Spec: `/home/exedev/.cache/baton/wf10/v1-L8/plan.md`, C1 (262–268); Owns extended by the last entry of `decisions.md`, option 1.
Head: `9d55d06bff9d75971c4d1fd83316715ac6a87b4b` (tested source head; the request-only commit follows it).
Base: `60dba67bd9e732de2b30e1b4a2cbe4fa1d7da5c2`.
Branch/worktree: `wf10/l8-c1`, `/home/exedev/Developer/personal/.worktrees/ergo-l8b`. No rebase or push.
Source diff stat: `15 files changed, 1281 insertions(+), 74 deletions(-)` (`git diff --stat 60dba67b..9d55d06b | tail -1`).

## Measured changes, by file

- `check/lend.go`: release-point relation, root inheritance, task/closure loans, keeper hand-off checks, field order, and callback/field/element transport. An indexed resource borrow cannot escape by implicitly moving its container.
- `check/ownership.go`: loans in the flow lattice, joins and assignments; early-take guards; generic-only restrictions become copy sites; explicit and trailing callbacks use the same escape rule.
- `check/taskcapture.go`: borrowed captures are distinct from owning moves.
- `lower/taskcapture.go`: stable capture bindings; only moves enter child ownership; borrowed-only frames need no owning-handle map.
- `lower/values.go`: borrowed resource captures do not enter value cloning.
- `runtime/v1/moves.go`: reviewed callback-to-keeper and completed-drain summaries.
- `check/lend_test.go`: seeded, replayable checker model over owner/keeper order, nested scopes, incoming borrows, container and iteration roots, fields, and kept callbacks.
- `check/task_capture_test.go`: retains moving-capture controls by placing their keepers before the resources; assertions remain.
- `lower/task_ownership_run_test.go`: keeps the move controls and adds borrowed tasks, Sets, kept functions, aggregate fields, and an incoming-borrow helper declared before any owning frame. The trace oracle checks use/drain before root release and exactly-once release.
- `check/testdata/v1/L8-C1-lends.neg.ergo` and `accept-rules.sha256`: diagnostic positions for early take, returned keeper and unbounded closure capture.
- Both packages' `testdata/mutants.json`: unique guards below.
- `docs/spec/resources.md`, `runtime.md`: scoped implementation status and the capture transport/lifetime contract.

## Measured checks

Round gate: `/home/exedev/.cache/baton/wf10/v1-L8/C1/gate-1.log`, **`GATE_EXIT=0`**, at the source head above. It includes clean gofmt/whitespace, short tests and vet of `internal/sketch/check`, `internal/sketch/lower`, `runtime/v1`, residue, `TestTestCommandsHaveCacheCoverage`, both acceptance tests, and the checker closure sweep. A server restart interrupted only the last sweep; that stage was resumed, with the completed checks left intact. This was not a test flake.

Acceptance command (run uncached at the source head):

```sh
umask 022
systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19 env GOMAXPROCS=2 GOMEMLIMIT=6GiB GOCACHE=/tmp/opencode/c1-gocache go test -p 2 -timeout 20m -count=1 -run '^TestStructuredLendModel$|^TestTaskOwnershipRunModel$' ./internal/sketch/check ./internal/sketch/lower
```

Acceptance exit: **0**, recorded by the two successful package lines in `gate-1.log` (the gate advances with `set -e`). `ERGO_SEED=<seed>` replays either model. Runtime binaries are necessary because go/types cannot execute task admission, restart, stop or release ordering; the existing one-binary helper and kernel memory bound remain.

Red at exact base: `red-current-1.log`, current checker model copied onto an archive of the base: **`BASE_TEST_EXIT=1`**, **`RED_PROOF_EXIT=0`**. Seed 1's safe iteration lend is refused at base. `/tmp/opencode/c1-red.ts` reproduces the proof without editing the worktree.

Mutants: `mutants-2.log`, **`MUTANTS_EXIT=0`**, **`EXIT=0`**. `/tmp/opencode/c1-mutants.ts` substitutes ledger edits in a scratch copy, one check at a time. All twelve C1 mutants and both retained checker capture mutants are killed:

- Model: trailing callback erases loan; index borrow moves container; named callback loses keeper; callable field erases loan; keeper order ignored; incoming return bound lost; field release order ignored; root inheritance lost; take while lent admitted.
- V1 fixture: the diagnostic points at the declaration instead of the early take. The model does not pin this diagnostic position.
- Runtime model: borrowed-only owner-map restoration assumes a map; a lend enters the child's owning list. Existing move-only programs do not exercise these paths.
- Retained controls: moving capture keeps sender ownership; repeated member may take its root captures.

The first mutation pass exposed a surviving source-order guard; the helper was moved before owning functions and the mutant now fails. No assertion was weakened or removed. No test was deleted.

All checks used share C (`GOMAXPROCS=2`, `-p 2`), umask 022, GOMEMLIMIT=6GiB, and the single private cache. Common.md:44 explicitly departs from the supplied no-worker-scope rule, so ad hoc checks used its 8GiB/zero-swap scope. Deletions use the exact-path script `/tmp/opencode/c1-cleanup.ts`; the cache and scratch source copies are removed before READY. Post-check process lists contain no owned child. No paid call or owner store was used.

## Inferred limits and follow-ups

- Unknown escaping callback keepers are refused; general callback-to-keeper inference through arbitrary wrappers is not built here. Direct Task/Set keepers use reviewed runtime facts.
- Aggregate contracts are conservative: later task or callable fields may borrow earlier resource fields, including across a constructor boundary. A signature cannot promise those fields are independent.
- `lower/expr.go` is outside Owns. Its existing predicate can open an extra frame for a borrowed-only callback; removing that overhead is a lowering follow-up, not a correctness waiver. No speed claim is made.
- These are finite seeded checks, not a proof over every schedule. Full/merge/Mac gates belong to the batch. Suggested map update: mark C1's structured-lend checker and transport complete for #197; leave the other L8 units and HITL decisions unchanged.
