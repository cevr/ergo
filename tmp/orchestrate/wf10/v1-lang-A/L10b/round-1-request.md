ready — L10b round 1

Protocol: supplied orchestrate worker protocol (implementor). Spec: /home/exedev/.cache/baton/wf10/v1-lang-A/L10b/spec.md. Head: 83b50ada46abde8d5065121f025391cbee4cb3f6 (implementation and gate head; the request-only commit follows it).
Base: de51a4d25cb985435a2a544a437593321d7778c0. Branch: wf10/l10b. Worktree: /home/exedev/Developer/personal/.worktrees/ergo-v1a. No rebase or push.
Measured `git diff --stat de51a4d2..83b50ada | tail -1`: `9 files changed, 286 insertions(+), 12 deletions(-)`.

## Changes

- `internal/sketch/lower/tests.go`: integer draws keep the existing random distribution but record the resulting value after narrowing, as sign and magnitude (unsigned integers record only magnitude). Replay shrinks this canonical value, not a small/edge distribution selector. Replay clamps values shifted from a wider draw rather than wrapping them. Applies recursively through the existing generators, including duration.
- `runtime/v1/shrink.go`: binary search toward zero, then adjacent decrements; retain the outer fixpoint so a change to a later component can unlock an earlier component.
- `runtime/v1/shrink_test.go`: enhance the existing seeded property with exact monotone boundaries, a replay-work bound of `2*bit-length+12`, and dependent components that require another pass. No timing assertion.
- `internal/sketch/lower/shrink_test.go`: seeded boundary property (replay flag below) over both signs, all integer widths, duration, strings and nested record/tuple fields. Also the smallest repro for db043a7d: a law under a context reads an empty generated fstest world and writes a file, so reuse fails the next case.
- Both packages' `testdata/mutants.json`: six named mutants and their killers.
- `internal/sketch/lower/testdata/e3/property_fail.ergo`: the existing `n < 5` repro now expects `5`, not the buggy `127`. No test was deleted.
- `docs/testing.md`, `docs/spec/runtime.md`: canonical integer shrinking, component fixpoints, and the finite-budget/global-minimum limits.

Decided by fix-root-causes: canonicalize the generated integer, rather than add redistribution heuristics for each distribution branch. No application lowering or runtime provider contract changes; no speed claim.

## Evidence and unique guards

Measured red at base (new property before implementation): `red-base.log`, `exit 1`; seed 1 shrank `n < 72` to 127 rather than 72. The same failure covered negative, narrow and composite values. A prior scaffolding probe used invalid tuple access; that was fixed before this behavioral red run. The acceptance property now also includes the exact reported `n < 5` repro.

| Mutant | Guard | Measured result |
| --- | --- | --- |
| l10b-shrink-no-adjacent | TestShrinkTapeKeepsTheFailure | exit 1: boundary + 1 |
| l10b-shrink-linear-search | TestShrinkTapeKeepsTheFailure | exit 1: 1000 replays, limit 140 at seed 1 |
| l10b-shrink-no-fixpoint | TestShrinkTapeKeepsTheFailure | exit 1: seed 16 leaves [11, 9], but [10, 9] still fails |
| l10b-law-context-keeps-world | TestLawContextStartsEachCaseFresh | exit 1: context law sees an earlier case's file |
| l10b-integer-branch-tape | TestIntegerShrinksReachBoundary | exit 1: reported n < 5 repro misses 5 |
| l10b-integer-no-narrowing | TestIntegerShrinksReachBoundary | exit 1: int8 boundary missed |

Each mutant log is `<unit dir>/<mutant>.log`; all failed on assertions, not build errors, and each owned process group was empty afterward. Reproduce: `bun /tmp/opencode/l10b/check.ts /tmp/opencode/l10b/mutants.json`.

The two new tests earn unique guards (measured whole-package runs):
- `world-existing-fixed-oracle.log`, `exit 0`: context-keeps-world survives every lower test with only the new fresh-context guard excluded. The initial `world-existing.log` failed only on the old e3 expectation of 127; the corrected oracle run is green. Then the new guard alone kills the mutant.
- `integer-existing.log`, `exit 0`: integer-no-narrowing survives every lower test with only the new boundary property excluded. Then the property alone kills it. The corrected e3 repro also kills integer-branch-tape; that mutant alone is not the reason to retain the broader property.

## Run and gate

All checks used umask 022, share A1 (`GOMAXPROCS=1`, `go -p 2`), `GOMEMLIMIT=6GiB`, and the one private GOCACHE `/tmp/opencode/l10b/gocache`. Each ran in common.md's bounded systemd scope (`MemoryMax=8G`, no swap, nice 19), the explicit Memory-rule departure from the supplied protocol's launcher rule. The private cache is removed before READY. Disk stayed above 10G free.

Round gate: `bun /tmp/opencode/l10b/check.ts /tmp/opencode/l10b/gate.json` at 83b50ada. Log: `/home/exedev/.cache/baton/wf10/v1-lang-A/L10b/gate-1.log`, **`exit 0`**. Changed-file gofmt empty; vet and all tests of runtime/v1 and lower; internal/residue; scripts/gate TestTestCommandsHaveCacheCoverage; then acceptance again. Process group 2906637 empty. Full gate, full e2e and examples are batch-owned under the worker protocol; none was run here.

Acceptance commands at the implementation head (gate log lines 12–14), both **`exit 0`**:
```
go test -p 2 -timeout 10m -count=1 -run '^TestShrinkTapeKeepsTheFailure$' ./runtime/v1
go test -p 2 -timeout 30m -count=1 -run '^(TestIntegerShrinksReachBoundary|TestLawContextStartsEachCaseFresh)$' ./internal/sketch/lower
```
For a boundary seed: `go test -p 2 -timeout 30m -run '^TestIntegerShrinksReachBoundary$' ./internal/sketch/lower -args -integer-shrink.seed=1`. The failure report includes the full Ergo source, expected boundaries, output and replay command. Tape-property replay: `-shrink.seed=16` on runtime/v1's property.

## Known limits and follow-ups

- A case still has a 500-replay budget; exhaustion can stop short of a fixpoint. Arbitrary predicates have no global-minimum guarantee. This unit does not change those limits.
- L10's remaining suggestion to show a receipt in an example is outside this unit.
- Map update: the two specified L10 follow-ups are closed by the boundary property and the unique context-world guard. No new ticket is needed.
