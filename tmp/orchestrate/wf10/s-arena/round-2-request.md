blocked

Protocol: orchestrate implementor worker protocol carried in the prompt.
Spec: `/home/exedev/.cache/baton/wf10/s-arena/brief.md` (no separate spec.md).
Head: `ffd288eef1bede42566c7bcba1b678c37b3e3c45` (source head; handoff committed on top).
Base: `c96060df`; branch `wf10/s-arena`; worktree `/home/exedev/Developer/personal/.worktrees/ergo-e12`; no rebase.
`git diff --stat c96060df..HEAD | tail -1` at source head: `14 files changed, 244 insertions(+), 47 deletions(-)` (measured).

## Verdict and fix report

This is a partial correctness head, not a request to land the unit. The unresolved F2/F3 performance requirement blocks READY. Decision requested in `/home/exedev/.cache/baton/wf10/s-arena/blocked.md`.

- F1: fixed ffd288ee. Guard: `TestStdGenericLetsCompile`; constant and tuple-initialized std lets read from a generic function. Red against exact c96060df compiler files: missing Go name for `none`. Unique mutant `std-let-tuple-location` is killed; existing arena laws do not cover embedded tuple initialization.
- F2: unresolved, not rejected. Generic result aliasing needs a consistent value-flow rule. No blanket clone removal or arena-name exception was added.
- F3: unresolved, not rejected. Unknown callbacks keep their interruption poll and error ABI. No purity claim based only on an empty row was added.
- F4: fixed ffd288ee. Guard: `TestStdArenaReleasesStorage`; observes actual pointerful storage after Free, not just Stale, and rejects a foreign-store Ref matching a free slot's generation. Red before clearing: `Free retains the removed pointerful value`; red before occupancy check: `a foreign-store Ref exposes cleared host storage`. Unique mutants: `arena-free-retains-pointerful-value`, `arena-free-skips-take`, `arena-free-slot-readable-by-foreign-ref`, `arena-storage-forgets-generic-arity`. Prior laws cannot observe hidden retained storage. The generic-arity mutant is a boundary guard, not a separate test.

## Changes by file

- `internal/sketch/lower/sources.go`, `naming.go`, `module.go`, `decls.go`: embedded std lets use the existing library naming and initialization-order path; tuple initializers use std checker facts without requiring user-file locations.
- `internal/sketch/lower/std_lets_test.go`: in-process F1 regression through lowered Go and go/types.
- `internal/sketch/std/arena/arena.ergo`: real package constants; private generic storage; Free takes and clears the value. Live slots mark their existing free-list link with -2; Has checks occupancy as well as generation. No extra slot field.
- `runtime/v1/data.go`: private-to-Ergo generic storage binding and reviewed pure ArenaTake; no public zero[T]() or unsafe code.
- `runtime/v1/files.go`, `internal/sketch/lower/std.go`, `internal/sketch/std/keys_test.go`: validate generic runtime-record arity and private storage records.
- `internal/sketch/lower/blocking.go`: ArenaTake never waits.
- `internal/sketch/lower/arena_storage_test.go`: storage-clearing regression; builds Go because host storage must be observed after execution, which go/types cannot prove. Checks retained references without field-name coupling or GC timing.
- `internal/sketch/lower/testdata/mutants.json`: six additional arena/std-let guards; existing arena anchors preserved.
- `docs/spec/stdlib.md`: storage invariant, occupancy and cross-store limits.

## Commands and receipts

- Focused gate: `sh /home/exedev/.cache/baton/wf10/s-arena/gate-2.sh`; log `/home/exedev/.cache/baton/wf10/s-arena/gate-2.log`; exit line `---- exit 0`. Includes formatting, build, vet, std/runtime tests, focused lower laws/regressions, residue, cache coverage and lru example. No flake.
- Correctness acceptance at ffd288ee: `sh /home/exedev/.cache/baton/wf10/s-arena/accept-2.sh`; log `/home/exedev/.cache/baton/wf10/s-arena/accept-2.log`; exit line `CORRECTNESS ACCEPT EXIT 0 0`. Runs the F1/F4 guards, arena laws and unchanged lru cases.
- Required speed acceptance: `PERFORMANCE ACCEPT NOT RUN: F2 and F3 remain unresolved`. No inlining/allocation/BCE or speed-parity claim. Quiet-window commands remain in `/home/exedev/.cache/baton/wf10/s-arena/bench-pending.md` with hand Go control `/home/exedev/.cache/baton/wf10/s-arena/bench/handgen.go`.
- Mutants: `/home/exedev/.cache/baton/wf10/s-arena/mutants-2.ts`; full selected run `/home/exedev/.cache/baton/wf10/s-arena/mutants-2.log` kills 19; final storage rerun `/home/exedev/.cache/baton/wf10/s-arena/storage-mutants-2.log` kills four after reflection-guard cleanup; `/home/exedev/.cache/baton/wf10/s-arena/tuple-mutant-2.log` kills the additional tuple mutant. Each ends `MUTANTS EXIT 0`. All 20 selected ledger entries accounted for. The full lower ledger is batch scope; no test was deleted.
- Share A1: umask 022, GOMAXPROCS=1, go test -p 2, nice 19, GOMEMLIMIT=6GiB, one private GOCACHE `/tmp/opencode/sarena-r2/gocache`. Followed the explicit bounded-scope departure in common.md lines 41–45. Process checks after runs found no started processes left. No push or rebase.

## Known gaps and next action

F2/F3 remain present. Recommended next action: a compiler optimization unit defining result-alias summaries and known-callback variants, retaining the old lowering and adding seeded reference properties; merge it forward before collecting the required performance receipts. The private scratch/cache is retained while blocked, not shared or published. Effect intent, Ergo workarounds, generations and prior art remain as recorded in `/home/exedev/.cache/baton/wf10/s-arena/round-1-request.md`; this round adds no new public arena operation.
