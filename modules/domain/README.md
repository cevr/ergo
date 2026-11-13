# domain

An Ergo module of this repository (`modules/`, MK0): the event log of a domain model and the host that keeps an aggregate on it (DK2; [docs/domain.md](../../docs/domain.md)).

| Package | What |
| --- | --- |
| `domain` | `CommandID`, `Envelope[C]`, `Receipt`, `Recorded[E]`; the port `Log[E]` with its laws; `Ledger`, `Begin`, `Taken`, `Commit`, `Keep`, `StepID`, `Sourced` and `Send`, the host of a stream over a durable actor; `Cursor` with its laws, `Delivery`, `SentID` and `Follow`, a follower of a stream (DK3) |
| `domain/memory` | `Log[E]` and `Cursor` in the process's memory |
| `domain/file` | `Log[E]` in a file, one record appended at a time |

An example takes it with a `require` and a `replace` to this folder in its `go.mod`.

## Laws

`Log`'s laws run against each provider: `AppendThenRead`, `PositionsAreDense`, `ConflictOnStaleExpected`, `ResendGivesFirstReceipt`, `SameIdOtherPayloadIsRefused` (sync-spike L1, L2). `memory`'s `SourcedSurvivesCrashes` is the model-based test of the host (L3): generated commands, resends and crashes at each save, before and after the write, each followed by a restart. After each step, the actor's state is the fold of the log, no answered command is lost, no command is kept twice, and the decider's own count of its decisions is the number of commands the log kept: `decide` ran once for each. `memory`'s `StepIDsReplay`: a host whose signal is no `Envelope` and whose steps no command caused keeps each step once across restarts, each record named by `StepID`. `KeepKeepsTheTimer`: a step committed with `Keep` keeps its entry's timer, and only `Commit` (an `Emit`) starts it again (runtime.md, Machines and actors 9.2). `file`'s `ReopenedLogReads` holds that a log read back from its file has the records written, and that a torn last record is dropped.

## Followers

A policy or a projection follows a stream: `Follow(log, cursor, follower, stream, handle)` delivers each record after the follower's saved cursor to `handle`, in order, and saves the cursor once `handle` returned. A crash between the two delivers the record again, so delivery is at least once; a command that `handle` sends takes the id `SentID(delivery, index)`, made of the follower, the stream, the record's position and the command's index, and the log answers its second send with the first receipt. `Cursor`'s laws: `LastSaveWins`, `CursorsAreApart`. `memory`'s `RedeliveryAddsNothing` is the model-based test: a policy that sends a command per event, crashed at any send or save (before or after the write) and run again, leaves each event's command in the target stream once, in order.

## Decided

- The save gets the step's events through the committed state (`Ledger.Last`), so `decide` runs once per command, and `actor` is unchanged. By: the smallest road (plan DK2: "an additive `actor` start function whose save gets the output, or another"). Reverse by: an `actor` start whose save takes the output.
- `Sourced` is the recover and the save, and the program writes the step and the machine: a durable actor's step must be a function the checker sees (S108), so a generic host that takes `decide` as a value is refused (DK4 waits for STD-NOPRIV NP3).
- `Store` and `Sourced` take the actor's signal as a type parameter, which save does not read, so a desk whose signal is a union of commands and its own work's and timers' ends is hosted as an aggregate is (DK5). A stream may hold many entities: its expected position guards the whole state, which has one writer.
- `Keep` is `Commit` as a `Stay`: the step records its events without leaving the entry, so the state's work and timer go on; the sender reads the receipt from the ledger (`Send`'s fallback), since a `Stay` gives no output. By: runtime.md 9.2, one rule for every machine. Reverse by: a decision that stays and gives an output, if the runtime adds one.
- `StepID` is `#kind@position`: a step that no command caused is named by the position its record takes, so a replay names it the same way and no two records share it.
- A command whose id the stream kept is `Ignore` in the step, answered with the first receipt; the log refuses an id with another payload (`Reused`), so the actor compares no commands.
