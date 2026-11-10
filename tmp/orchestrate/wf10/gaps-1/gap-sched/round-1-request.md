ready

Protocol: supplied implementor worker protocol (orchestrate).
Spec: /home/exedev/.cache/baton/wf10/gaps-1/gap-sched/spec.md; authority /home/exedev/.cache/baton/wf10/gaps-1/brief.md item 6.
Head: 3f9b620db1006f9e07024d899a83958897c80a72 (implementation and diagnostic allowance; request committed next).
Base: 994773a910d4c31c3eb7493a33bb4cde96641866 (cu8 merge head). Branch: wf10/gap-sched. Workspace: /home/exedev/Developer/personal/.worktrees/ergo-v1b.
Measured `git diff --stat 994773a9..HEAD | tail -1`: 4 files changed, 34 insertions(+), 1 deletion(-), before this request.

## Changes by file

- internal/sketch/lower/module.go: check the initializer/entry consistency invariant only if no lowering refusal already prevents emission. Keep the real entry diagnostic; do not add a false internal error for an entry that cannot be emitted. Successful entries still must call their initializers. No host capability or implicit provider was added.
- internal/sketch/lower/entry_scheduler_test.go: smallest neutral repro, a package list initializer and a Scheduler call in main. Require exactly the existing entry diagnostic at 2:6. Type-check an explicit NewLiveScheduler provider under Ambient with the same initializer in process.
- internal/sketch/lower/testdata/mutants.json: gs-init-refusal restores the old invariant condition. No existing entry-refusal test had a package initializer, so none caught this bug. The diagnostic regression kills it; no test or assertion was removed.
- internal/residue/testdata/row-residue.txt: exact diagnostic sentence allowance, because the sentence is prose rather than obsolete comma-separated capability syntax.
- tmp/orchestrate/wf10/gaps-1/gap-sched/round-1-request.md: committed handoff.
- internal/residue/testdata/wire-residue.txt: narrow allowance for this report's baseline-log path.

## Measured checks and reproduction

Share D: GOMAXPROCS=2, go -p 2, GOMEMLIMIT=6GiB, nice 19, umask 022, private GOCACHE=/tmp/opencode/gaps1/gocache. Checks use the 8G bounded scope required by common.md lines 41–45, whose Departs-from line permits the worker scope. One heavy check at a time; no full/batch gate. No owned process remains after checks.

- Red first at 994773a9: /home/exedev/.cache/baton/wf10/gaps-1/gap-sched/red-1.log, `EXIT 1`. The initializer invariant adds an internal error at 1:1 ahead of the correct entry refusal. A Scheduler call without an initializer already gave the correct diagnostic; no admission rule needed changing.
- Acceptance: `go test -p 2 -count=1 -timeout 15m -run '^TestSchedulerEntryNeedsExplicitProvider$' ./internal/sketch/lower`, `EXIT 0`, in /home/exedev/.cache/baton/wf10/gaps-1/gap-sched/accept-1.log and rerun in the round gate at head. Uses lowerSource and go/types; no generated binary or wall-clock assertion.
- Mutant: `bun /tmp/opencode/gaps1/sched-mutant.ts`, /home/exedev/.cache/baton/wf10/gaps-1/gap-sched/mutants-1.log: `gs-init-refusal: killed by TestSchedulerEntryNeedsExplicitProvider; EXIT 0`. It fails the assertion rather than compilation and restores source in finally.
- Round gate: `zsh /tmp/opencode/gaps1/sched-gate.sh`, /home/exedev/.cache/baton/wf10/gaps-1/gap-sched/gate-1.log, `EXIT 1`. Formatting, lower vet, all lower short tests, exact acceptance and gate cache coverage pass. Only the pre-existing CU7 report residue fails (below).
- cu8 merge acceptance: /home/exedev/.cache/baton/wf10/gaps-1/cu8/merge-accept-1.log, TestPresenceWrappersGenerate, `EXIT 0` at 994773a9.

## Rule and prior-art context

docs/spec/rows.md Capability rows 4 remains unchanged: entry-provided Console, Ambient and go.Foreign do not imply Scheduler. main must explicitly bind it. This is diagnostic recovery, not a new std API or speed lowering. No generated runtime path or hand-Go control changed, and no speed claim is made.

Inferred prior-art comparison for the verifier: Gleam and ReScript check before host emission; Elixir compiles to BEAM and has runtime process capabilities, not Ergo's static needs contract. The relevant comparison is avoiding follow-on invariant diagnostics once compilation has already refused an entry, not copying their capability APIs. No prior-art source claim was measured in this round.

## Known gaps

The round gate is not fully green: TestContextResidue flags the already committed CU7 request at tmp/orchestrate/wf10/cli-units/cu7/round-1-request.md:17. The lane measured that same failure at baseline 50facc08; evidence: /home/exedev/.cache/baton/wf10/gaps-1/gap-wire/base-red-1.log, `EXIT 1`. No unrelated CU7 file or residue rule was changed. All unit acceptance checks are green; no flake or weakened assertion.

No push, main edit, paid call or new ambient admission. Final cleanup uses the previously written /tmp/opencode/gaps1/cleanup.ts, which verifies the owned detached scratch base and removes only /tmp/opencode/gaps1/base and /tmp/opencode/gaps1/gocache. Cleanup evidence will be /home/exedev/.cache/baton/wf10/gaps-1/gap-sched/cleanup-1.log.
