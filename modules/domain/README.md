# domain

An Ergo module of this repository (`modules/`, MK0): the event log of a domain model and the host that keeps an aggregate on it (DK2; [docs/domain.md](../../docs/domain.md)).

| Package | What |
| --- | --- |
| `domain` | `CommandID`, `Envelope[C]`, `Receipt`, `Recorded[E]`; the port `Log[E]` with its laws; `Ledger`, `Begin`, `Taken`, `Commit`, `Sourced` and `Send`, the host of an aggregate over a durable actor |
| `domain/memory` | `Log[E]` in the process's memory |
| `domain/file` | `Log[E]` in a file, one record appended at a time |

An example takes it with a `require` and a `replace` to this folder in its `go.mod`.

## Laws

`Log`'s laws run against each provider: `AppendThenRead`, `PositionsAreDense`, `ConflictOnStaleExpected`, `ResendGivesFirstReceipt`, `SameIdOtherPayloadIsRefused` (sync-spike L1, L2). `memory`'s `SourcedSurvivesCrashes` is the model-based test of the host (L3): generated commands, resends and crashes at each save, before and after the write, each followed by a restart. After each step, the actor's state is the fold of the log, no answered command is lost, no command is kept twice, and the decider's own count of its decisions is the number of commands the log kept: `decide` ran once for each. `file`'s `ReopenedLogReads` holds that a log read back from its file has the records written, and that a torn last record is dropped.

## Decided

- The save gets the step's events through the committed state (`Ledger.Last`), so `decide` runs once per command, and `actor` is unchanged. By: the smallest road (plan DK2: "an additive `actor` start function whose save gets the output, or another"). Reverse by: an `actor` start whose save takes the output.
- `Sourced` is the recover and the save, and the program writes the step and the machine: a durable actor's step must be a function the checker sees (S108), so a generic host that takes `decide` as a value is refused (DK4 waits for STD-NOPRIV NP3).
- A command whose id the stream kept is `Ignore` in the step, answered with the first receipt; the log refuses an id with another payload (`Reused`), so the actor compares no commands.
