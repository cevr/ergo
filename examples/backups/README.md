# backups

An event-sourced backup service (a backup is planned with its files, stored, verified or aborted), modelled as the alchemy-domain skill describes (an aggregate that decides commands into events, rejections as typed failures, a view, a policy and a saga), in Ergo's own constructs: unions and exhaustive `match`, typed failures, an operation type with laws, contexts and a machine.

```sh
go build -o bin/ergo ./cmd/ergo
printf 'plan a alpha a.txt:2:150\nstore a 300\n' | ./bin/ergo run examples/backups/cmd/backups
./bin/ergo test examples/backups/backup
```

The binary reads a script on standard input, one command per line: `plan BACKUP HOST PATH:COUNT:SIZE ...`, `store BACKUP SIZE`, `verify BACKUP`, `abort BACKUP REASON ...`, `wait MINUTES` and `usage`. Sizes are in hundredths of a GB. It prints each command's events or its rejection, the commands the saga sends, and the usage view at the end. A line that is not a command exits 2.

| package | what it is |
| --- | --- |
| `backup/` | The aggregate. `backup.ergo` (`Command`, `Event`, `State`, the five rejections), `decide.ergo` (`Decide`, a `match` over (state, command); `Evolve`, total; `Replay`), `backups.ergo` (`Backups`, the contract the service needs, with the laws `RejectedCommandEmitsNothing`, `AcceptedCommandIsRecorded` and `ReplayRebuildsState`; `Simulate`, the reference), `draw.ergo` (`Script`, seeded command scripts), `laws.ergo` (`StoreIsTheTotal`, `FreedIsTheStored`), `tests.ergo`. Nothing in it needs a capability. |
| `usage/` | The view: `Project` folds events into `Usage`; the law `SizeIsWhatBackupsHold` holds it to the replayed backups. |
| `keeper/` | The policy and the saga. `saga.ergo` (`Starts`, the policy: a `Planned` event starts a saga; `Keeper`, a machine whose input is the backup planned: a store emits `Verify` and waits for the verify, a deadline ends it with `Abort`); `desk.ergo` (`Submit` and `Advance`, which need only `backup.Backups`); `tests.ergo` (`EveryBackupSettles`). |
| `memory/` | The provider of `backup.Backups`: a stream per backup and a snapshot of its state, so a command is decided without a replay. Its tests run the contract's laws. |
| `cmd/backups/main.ergo` | The script parser (`Parse`, failing `BadLine`), the printing, and the only place that chooses the providers. |

The laws: `RejectedCommandEmitsNothing` and `AcceptedCommandIsRecorded` (a store records what the aggregate decides, and nothing for a refusal), `ReplayRebuildsState` (the snapshot is what the history replays to), `StoreIsTheTotal`, `FreedIsTheStored`, `SizeIsWhatBackupsHold` and `EveryBackupSettles` (after every deadline, no backup is left open or stored). `ergo test` runs the laws over seeded scripts of 40 commands on two backups (and `Backups`' laws against `memory.Log`), and each has a mutant that breaks it (`batch-13/retheme/RT1-A/backups-mutants.json` in the run dir, translated from ex-g's list).

Because the saga verifies a backup as soon as it is stored, the desk never aborts a stored backup; a freed size is reached by deciding against the aggregate directly (the laws' scripts do).

## Limits found

- **A generated slice is short and its ints rarely meet** (G-ex-d-9, still open after T4). The laws draw their own scripts of 40 commands on two backups from a seed.
- Closed in batch 13: G-exup-1. `ergo test` runs `Backups`' three laws in `memory` against `Log`, built by `NewLog`; `memory`'s tests check them again over seeded scripts.
- **A prelude constraint cannot be declared** (G-ex-c-11, partly closed). Batch 12 admits a prelude type's name, but `number` is a predeclared constraint (S108), so the parser's number reader stays `whole`.

Closed in batch 12: G-ex-g-5 (V4: `Keeper`'s input is the `Planning`, and `Starts` takes `machines.Initial(machines.Start(Keeper{}, …))`); G-ex-a-9 and G-ex-b-12 (V5: a store `Emit`s `Verify` and the saga waits in `AwaitingVerify` until the backup is verified or aborted; `Feed` moves with `machines.Next`); G-ex-a-4 (L2: the row `Rejection` names the five rejections, one `case Rejection:` per catch); G-ex-b-3 (`memory.Log` keeps its streams and snapshots in one `sync.Ref`); G-ex-d-5 (W3: event logs compare with `==`); and the property tests that restated the aggregate's and the view's laws (V1 runs them).
