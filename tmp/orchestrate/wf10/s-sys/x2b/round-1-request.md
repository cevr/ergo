ready

Protocol: orchestrate implementor worker protocol carried in the prompt. Spec: `/home/exedev/.cache/baton/wf10/s-sys/brief.md`, item 1 (no separate spec.md).
Head: `b4aaa74fd1c296c99144ffa57e8b4a154f98a3a0`, branch `wf10/x2b`.

## Base and head

Base `f85afd8372bc0d5cc4ce575d2e8b825356f43357` .. head `b4aaa74fd1c296c99144ffa57e8b4a154f98a3a0`.
`git diff --stat f85afd83..HEAD | tail -1`: `1 file changed, 60 insertions(+), 56 deletions(-)` (measured).

## Changes

- `models/children/children.ergo`: safety machines use World directly. Design.Safe is `always safe`, and `safe` combines the three invariant predicates. Four mutant laws are `sometimes unsafeState`. Broken and its transition-time verdict conversion are removed. The five reachability witness machines are unchanged.
- World already held the transition-history bits Late and OrderBad. They remain the ghost state for stale-output and reverse-stop observations; adding duplicate bits would not add information. Current ownership/epoch invariants read the state directly (inferred).
- The mutant's first unsafe state remains terminal, as Broken was. This preserves the first-violation boundary and shortest witness lengths. The state now shows the actual tree at the violation.

## Proof receipts (measured)

Before: `s-sys/x2b-before.log`. After: `s-sys/x2b/gate-1.log`.

| Law | Before | After |
| --- | --- | --- |
| Design.Safe | proved: 289995 states, 8699850 edges, longest shortest run 32 | same |
| LateOutput.Breaks | proved witness, 7 signals | proved witness, 7 signals |
| ParentFirst.Breaks | proved witness, 3 signals | proved witness, 3 signals |
| ByKind.Breaks | proved witness, 6 signals | proved witness, 6 signals |
| EpochWraps.Breaks | proved witness, 13 signals | proved witness, 13 signals |
| Drops.Reached | proved witness, 7 signals | same |
| ReentersAfterStops.Reached | proved witness, 5 signals | same |
| TwoEditors.Reached | proved witness, 2 signals | same |
| DeepChild.Reached | proved witness, 2 signals | same |
| RootEndsAfterStops.Reached | proved witness, 8 signals | same |

The runner reports visited-state counts for a completed safety proof, not for a successful witness search. No witness state count is claimed. All ten old law verdicts are preserved.

## Checks and how to run

`bash /tmp/opencode/s-sys/x2b-gate.sh /home/exedev/.cache/baton/wf10/s-sys/x2b/gate-1.log`

- Gate at head: `EXIT 0`; fmt check, `ergo test models/children`, residue and TestTestCommandsHaveCacheCoverage all exit 0.
- Acceptance at head: `/tmp/opencode/s-sys/ergo test models/children`: `PASS (10 tests)`, `SECTION EXIT 0`.
- Migration guard: `grep -q 'law Safe always safe' models/children/children.ergo` exits 0 at head. The same guard on `git show f85afd83:models/children/children.ergo` exits 1. This is a source-form migration, not a new runtime rule; the base already implements always.
- No new Go tests. The existing model mutant ledger is the four named mutant machines in the model's header. Each is still killed by a shortest unsafe witness; the five non-vacuity witnesses still pass. No duplicate test asserts the same invariant.
- umask 022, nice 19, GOMAXPROCS=1, go -p 2, private GOCACHE, GOMEMLIMIT=6GiB; kernel RLIMIT_DATA 8GiB inherited by child programs. Process checks show none of this lane's check processes remain.

## Prior art

Inferred from the standing sources: Quint/TLC check state predicates and retain history as ghost state where a transition fact is not reconstructible. Ergo now states that same obligation directly with always. Gleam and Elixir lower to BEAM and ReScript to JavaScript; none supplies this graph proof as a language law. Their target lowering paths do not change. MoonBit's ordinary predicates likewise do not enumerate machine state graphs. This unit changes a model only, not compiler or runtime lowering.

## Known gaps / map update

- Full and merge gates and Darwin belong to the batch. No compiler package changed, so no package vet or Go unit suite is required beyond the common residue/cache checks.
- common.md lines 42–44 request worker-created scopes, contrary to the carried developer protocol's explicit prohibition. Used a kernel data limit without a worker-created launcher instead.
- The package keeps one combined safety law to avoid three identical graph enumerations; its three predicates remain named.
- X2's models/children follow-up is satisfied. S-embed and S-atomic remain the next stacked units. The one private GOCACHE is retained until the lane's final READY, as the brief requires.
