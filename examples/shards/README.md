# shards

A sharded write: a file is staged as parts, written as one or more shares (disks and tapes) at once, and shown on a summary screen. The write is a machine run as an actor. Its `Writing` phase hosts a child, the write runner, and the runner hosts one child per share, so the shares are written at once. Each child's answer climbs back up as a signal of the entry that started it. When a target refuses a share, the write erases every share that was taken before the operator places the shares again. A script on standard input plays the operator.

Tag: `default`. It is an original program on the shape of survey E6 (nested children with compensation). Its goldens were recorded from the program and read by hand.

## Layout

- `ergo.mod`: `module shards`.
- `shards/`: the domain.
  - `targets.ergo`: the contract `Targets` (`Write`, `Erase`) with the laws `WriteTakesTheSize` and `EraseGivesBack`, and the shares.
  - `shards.ergo`: the parent machine `Shards` (`Staging | Writing | RollingBack | Summary`), its step, its summary timer and its work.
  - `runner.ergo`: the child machine `Runner`; its work starts a `Writer` child per share.
  - `writer.ergo`: the grandchild machine `Writer`, with a work that asks the target and a 30 s timer.
  - `laws.ergo`: the rules of the three machines as laws, and `refusing`, a target for the laws.
- `memory/`: `Targets`, an in-memory provider of `shards.Targets`, and properties that run the `Targets` laws against it.
- `console/`: the line protocol: it parses the script, runs `Shards` with `actor.StartMachine`, asks it each signal, waits with `actor.WaitFor` and prints the end with `actor.ExitOf`.
- `cmd/shards/main.ergo`: the binary, the only place that chooses the providers.

## What it shows

- **Children per entry, nested (MU5).** `Writing`'s work calls `actor.Child(send, machines.Start(Runner{}, plan), 1, outcome)`. The runner's work calls `actor.Child` once per share. `outcome` maps the runner's `Outcome` to a write `Signal`, and the `send` parameter makes the checker hold that map's result to the entry's signal type. The children belong to the entry that started them: when it ends they stop, the last started first, before the next entry opens, and an answer that comes after its entry ended is dropped.
- **Several children of one machine.** One `Writer` per share: the children are kept apart by identity, with no names.
- **Work with typed failures.** `Writer`'s work fails `Refused`, which is a signal, so it needs no failure map. `RollingBack`'s work erases the writes that were taken.
- **Timers.** A write gives up after 30 s with no answer (`NoAnswer`). The summary closes after 60 s with no touch; a touch re-enters the summary (`Enter`), so its timer starts again.
- **Order that does not depend on timing.** The writes answer in any order. `OutcomeOf` lists them in share order, and the law `OutcomeIgnoresArrival` holds it, so the output is the same in every run.
- **Rules as guards.** `Place` must cover the staging exactly, with positive sizes; a `cancel` while writes run is refused, because a write may already be taken.

## Laws (machine laws as top-level laws; MU6 is not on this base)

| law | rule | mutant it kills (measured, `mutants.sh`) |
| --- | --- | --- |
| `SharesCoverExactly` | Place starts a write exactly when the shares sum to the total | `>=` for `==` |
| `SummaryOnlyAfterWritten` | the summary opens only after every share was taken, or on a touch | a lost share opens the summary |
| `NoCancelWhileWriting` | a cancel while writes run is refused | cancel during Writing ends the write |
| `TakenWritesAreErased` | after a lost share, every taken write goes to RollingBack | refs dropped |
| `OutcomeIgnoresArrival` | the outcome is the same in any answer order | refs listed in arrival order |
| `RunnerWaitsForAll` | the runner ends only after every share answered | the runner ends at the first answer |
| `WriteAlwaysAnswers` | every signal ends a write with its answer; its timer is 30 s | no answer counts as taken |
| `SummaryTouchRestarts` | a touch re-enters the summary; its timer closes the total | touch is a Stay |
| `Targets.WriteTakesTheSize`, `Targets.EraseGivesBack` | the target contract | run against `refusing` here and restated for `memory.Targets` |

The children's lifetimes (the children stop with their entry, and a late answer is dropped) are the runner's rules, proved by MU5's Quint model and its model-based test. The cases run them.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/shards/cmd/shards < examples/shards/cases/split/stdin
./bin/ergo test examples/shards/shards      # 10 laws
./bin/ergo test examples/shards/memory
go run ./scripts/examples shards
```

Cases: `single` (one disk, a touch, the summary's timer), `split` (a disk and a tape written at once), `refused` (a full disk; an offline tape with a taken disk, erased; then a write), `rules` (refused places and sizes, an unknown share kind, cancel).

## effect-machine side by side

The same write in effect-machine would use `.spawn(State.Writing, ({ self }) => self.spawn("runner", runnerMachine) …)` with a child id per child, the child's output sent to the parent by `sendParent` or a forwarded stream, `.task` for the write and the erase, `.timeout` for the write and the summary, `.reenter` for the touch, and `ActorHost` if a consumer must reach the current runner.

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| a child's output type | the parent's event type is not checked against the child's output map (MU5 request) | `toParent` must give the entry's signal type, through `send` | compiler |
| several children of one machine | distinct string ids in the actor system | values kept by identity; no id | runner (MU5 `byKind` mutant) |
| a late child answer | the program checks a generation by hand (`ActorHost`) | dropped by the entry's epoch | runner (MU5 model and test) |
| stop order | not specified | last started first, before the next entry | runner (MU5 model) |
| re-entry restarts a timer | `.reenter` vs `.on` | `Enter` vs `Stay`, written | compiler (the decision is a value) |
| every state × signal decided | unhandled events dropped | total `match`, `(State, _): Ignore` written | compiler |
| the services a work needs | `R` of the task's Effect | `Work[Signal, Targets \| Scheduler]`: the target is a capability the binary binds once (`cmd/shards`), not a value in every state | compiler (rows) |

Where Ergo could not improve:

- **The driver cannot reach a child.** The console talks to the parent machine only; children are owned by their entry (MQ10: no ambient registry). A screen that shows a write's progress would need the child's address handed out as a signal (the `ActorHost` pattern), which this program does not need.
- Written type arguments and `Ignore` arms, as in pairing.

## Benchmark

em-parity §3 lists "child fan-out" as pending for MU5. This program's split write is that shape (a parent, a child, N grandchildren, one answer each). The command for an end-to-end time of the cases is in the lane's `bench-pending.md` (not run).

## Limits found

- **F13 (closed in becf5fb4)** A machine whose `initial:` is a value (`initial: Open()`) started with no state when another package started it. An imported package's machine initials now run at start, in init order. `Open` still takes the starting parts.
- **F4** (pairing) The `Targets` laws are not run against `memory.Targets`; `memory/tests.ergo` restates them.
- **F7** (pairing) The examples runner does not run the laws of `shards/` (a package with laws and no `test`).
