# X5 sched-seam, round 1 request: ready

Protocol: supplied orchestrate worker protocol (implementor). Spec:
`/home/exedev/.cache/baton/wf10/explorer/plan.md` §5 X5, and `brief-X5.md`.
Head: be66bcd57c5aa8a221d87fb15caa1b6e8f494dcc on `wf10/x5`.
Worktree: `/home/exedev/Developer/personal/.worktrees/ergo-l8b`.

## Commits and changes

Base `00c4325c84d155ce9942f915542c9f5448643748`; head above.
`git diff --stat 00c4325c..be66bcd5 | tail -1`:
`33 files changed, 1900 insertions(+), 14 deletions(-)`.

- `81f43730`: `runtime/rt/sched.go`, `sched_live.go`, `sched_explore.go`:
  controlled scheduler under `ergoexplore`, one baton, Strategy/Choice/Result,
  stable spawn paths, deadlock/escape reporting and schedule records. Hooks in
  `rt/{handle,clock,idle,tasks,timeout}.go` and
  `v1/{queue,mailbox,sync,actor,actor_host,set}.go` connect waits and shared
  operations. `runtime/embed.go` includes both forms; `v1/exit.go` and
  `explore_on/off.go` connect the tagged entry.
- `70d82f3b`: `cmd/ergo/{main,commands,test}.go`: `ergo test --explore`.
  `internal/sketch/build/action.go`: Tags is a Go action input, not an emission
  input. Existing `inputs_test.go` and CLI `test_command_test.go` guard action
  separation and unchanged emitted-module bytes. `docs/spec/runtime.md` and
  `docs/tooling.md` document the supported seam and limits.
- `f0e5c3a4`: `models/baton/baton.ergo`, per owner q78: three-task design, four
  invariants, three safety mutants, deadlock/end/clock witnesses. Design.Safe
  proved over 142 states; all six reachability laws pass. Deleted the new
  Quint model, runner and log via `/tmp/opencode/x5/delete-quint.ts`, an exact-
  path script. `runtime/rt/MEASURED.md` gets the erasure note, not RECEIPT.md
  (RT2 renamed it on main). `scripts/gate/steps.go`: tagged rt test and model,
  full tier; `scripts/authored_examples.txt`: model formatting input.
- `be66bcd5`: weight the tagged gate step from runtime/rt's recorded costs.

Supporting files outside the literal Owns list embed, connect or guard this
seam. No compiler lowering changed. No rebase, main merge or push.

## Checks and mutants (measured)

- Round gate: `/home/exedev/.cache/baton/wf10/explorer/x5/gate-1.log`:
  `EXIT=0`, `SHELL_EXIT=0`.
- Acceptance at head:
  `go test -p 2 -timeout 10m -count=1 -tags ergoexplore -race -run TestSchedulerFollowsModel ./runtime/rt`:
  exit 0. 300 generated programs against a reference model: enabled tasks,
  operations, counters, latches, stop/completion, clock, deadlock and replay
  trace. `ERGO_SCHED_SEED=N` replays a failure; it prints source instructions
  and both results. New unique mutants: new-task-runs-unpicked, lost wake,
  two batons, stop without point, early clock, unstable spawn path, false pass
  on deadlock. No prior test drove a controlled scheduler.
- New `runtime/TestSchedHooksCostNothingLive` checks embedded Go in process:
  empty hooks, false constant and plain arguments or guarded closures. Unique
  mutants: live-hook-body, hook-argument-calls, hook-closure-unguarded. Existing
  TestCacheInputs kills tags-not-linked. Ledger:
  `runtime/rt/testdata/mutants.json`; `/tmp/opencode/x5/mutants-head.log`:
  eleven of eleven killed by assertions, `EXIT=0`, no build-error/timeout kills.
- Tests of runtime, rt, v1 and build, focused CLI tests, vet of those plus
  cmd/ergo and scripts/gate, tagged rt/v1 vet, residue, cache coverage and closure
  sweeps (runtime, cmd/ergo; jobs=1): all exit 0. Changed Go files gofmt-l empty;
  git diff-check empty. No test was deleted.
- Whole scripts/gate first run found only the missing step weight. At fixed
  head the server restarted mid-rerun: completed tests were not repeated.
  `/tmp/opencode/x5/gate-package-head.log` plus `gate-package-resumed.log` cover
  every declared test; continuation `EXIT=0`, `SHELL_EXIT=0`. Slowest head test:
  TestMergeTierSandboxRegression, 268.41s (measurement, not an assertion).
- Fast `./scripts/gate.sh` under wf10/gate.lock, ERGO_GATE_CPUS=2:
  `x5/fast-gate-1.log`, `EXIT=1`, solely examples(default)/chat build. Every
  other step passed. Two passed input sweeps were not cached after input
  changes. The orchestrator explicitly assigned the base chat error elsewhere.

All checks: umask 022, share C (GOMAXPROCS=2, -p 2), nice 19, GOMEMLIMIT=6GiB,
one private GOCACHE. Ad hoc checks use common.md lines 41–45's owner-approved
Departs-from exception (8 GiB systemd scope); gates use their own bounds.
Process listings found no owned leftovers.

## Red at base and examples

`/tmp/opencode/x5/red-at-base.log`, compiler built at 00c4325c:
`--explore` unknown (exit 2); unchanged acceptance source transplanted into base
does not build (Block/Unblock/Result/Point/Choice/OpKind undefined, exit 1).

Every example package with tests ran plain and with --explore. Logs:
`/tmp/opencode/x5/examples-explore.log` and `remainder.log`. All pass except
chat. The **same chat error occurs at 00c4325c**, exit 1:
`examples/chat/server/server.ergo:97:43: cannot use slot_1 (variable of interface type Conn) as *v1.Owned value in argument to ergo.Hand[struct{}]`;
line 97:32: `(slot_1).Release undefined (type Conn has no field or method Release)`.
No X5 edit touches chat; per orchestrator, not fixed here.

## Run, limits and map update

```
go test -p 2 -timeout 10m -tags ergoexplore -run TestSchedulerFollowsModel ./runtime/rt
go build -p 2 -o /tmp/opencode/x5/ergo ./cmd/ergo
/tmp/opencode/x5/ergo test models/baton
/tmp/opencode/x5/ergo test --explore examples/downloads/fetch
```

Measured: go1.27.0 can inline every live hook, and inlines 22 hook call sites.
No timing claim: quiet-window base/head commands are in x5/bench-pending.md.
Module equality is checked through the CLI fixture and generic action property;
no separate byte pass over every conformance lowering was run. The tag never
enters the emitter. X5 supplies one cooperative schedule, not X6's modes.
An abnormal run leaves its tasks parked until process exit, as documented;
X6 must resolve repeated-run ownership before looping over schedules. No
general foreign-goroutine sandbox is claimed; tests cannot reach host rows.

Prior art (inferred from plan sources): Loom/shuttle switch at their sync
types; Concuerror instruments the BEAM, also covering Elixir/Gleam output;
Coyote rewrites Tasks. Ergo swaps runtime hooks, not source types or lowered
Go. ReScript uses JS's event loop; MoonBit task groups compare lifetimes,
not schedule search. Go erases live hooks. No upstream speed is an Ergo claim.
X5's seam and model are complete; modes and repeated-run ownership remain
X6. No HITL ticket was closed.
