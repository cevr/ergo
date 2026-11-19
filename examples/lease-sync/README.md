# lease-sync

Two users share a pool of named leases (capacity 2) through a sync replica each, and one authority decides. A press shows on the user's own screen at once; the authority runs the same `Decide` again on its own state and may refuse it with a typed reason, which the user's screen then shows. It is the sync engine of the wf10 SYNC plan, stage a, with the authority served over std `rpc` (`lease-server`): the replica's library lives in this program's packages until a second program needs it.

Tag: `default`. It is an original program; its goldens were made by running it and checked line by line against the rules below.

## Rules

- A script line is `a STEP` or `b STEP`, or `state` (the authority's pool). Steps: `take NAME`, `give NAME` (a press), `push` (send the user's pending commands, in order), `pull` (read the log from the user's cursor and apply the frame), `offline`, `online`, `dismiss` (drop the first shown refusal), `dropcache` (forget the confirmed state, as after a schema change).
- After each step the program prints the user's screen: the connection, the pool it shows (`loading` while the replica is below its floor), each pending command, and each refused command with its reason.
- A pool holds at most 2 leases; a name is held once; only its holder gives it back. The refusals are `all 2 leases are held`, `NAME is held by USER` and `NAME is not yours`.
- The screen is the confirmed pool plus a rerun of the pending commands. A pending command whose prediction fails changes nothing and stays pending: the authority decides.
- The authority takes each user's commands in order (a command past the user's next one is refused as out of order) and answers a resend with the first receipt. A refused command is recorded, so the user learns the reason from a later frame, a snapshot frame included.
- While offline, presses queue and `push` and `pull` do nothing; nothing queued is lost.
- After `dropcache` the screen is `loading` until the replica has read the log back to its floor (the point where it last pruned its pending commands).
- A bad line exits 2 with `lease-sync: line N: …`.

## Layout

| folder | what it holds |
| --- | --- |
| `lease/` | The domain, pure: `Pool`, `Cmd`, `Event`, the refusal `Refusal{Reason}`, `Decide` (no needs row) and the total `Evolve`, with the laws `NeverOverCap` and `NamesAreUnique` beside them. |
| `domain/` | The event log's types and the memory log: copies of the module `domain` (wf10 DK2) with its names, fields and laws, until that module lands and the replica moves to a module (wf10 RP2). |
| `replica/` | The client: `Source` (its state needs a codec, since a client holds it: a state with a part that has none, such as a server handle, is refused at the `Source` with a diagnostic naming the part; cmd/ergo's `TestDiagnosticCases`), `Fact`, `Frame` (`Snapshot` carries the caller's refusals), the `Authority` operation type with its laws, `State`, `Signal` and the pure `Step`, with the laws `ViewIsCommittedPlusPending`, `ReadyIsStable` and `RefusalIsTold`. Its loops (`loop.ergo`): `Send`, a round of the push loop (each command retried on `Unavailable` by a `Schedule`, the same envelope every time), `Fetch` and `Follow`, the pull loop, and `ConnOf`, the one rule from a provider's failure to the connection shown. |
| `replica/server/` | The authority: the order rule, the resend rule, `Decide` on its state, the log of facts, delta and snapshot frames; `Await`, the long poll (a pull with nothing new waits for the next kept record or its time limit); `Handle`, its std `rpc` handler, with the authorization seam run before `Push` and `Pull`. Laws `StateIsLogReplay`, `SnapshotIsReplay`, `DeltaIsTheTail`; test `LongPollHolds`. |
| `replica/rpc/` | The `Authority` over std `rpc`: `Push` is one call and `Pull` one call the authority may hold; a failure of the transport is `Unavailable`. `Leases` (`Dial`, or `NewLeases` through `rpc.Loopback`, where the Authority laws run); the property `AgreesWithMemory` (the same answers and frames as the memory provider); `RetryResendsTheSameID`, `RefusedClientNeedsSignIn`, `IdleCostsOneCallPerWait`. |
| `replica/page/` | A paged window over a long list (`Window`): pages read after or before it by cursor (`Extend`, `ExtendBack`, a late page too), `Fail`, `Tail` for the rows that arrive live (held until the window reaches the list's end, never shown inside a part that is not loaded) and `Local` for the outbox's rows; `Rows` is the view's list, each row once by ID. The laws `FailedPageChangesNothing`, `LiveRowStaysAfterLoadedEnd`, `ConfirmedLocalRowIsNotTwice` and the model-based `WindowIsTheListsWindow` (any run of pages, live rows, pending rows, confirmations, failed pages and a lagging read model against the list). |
| `replica/memory/` | `Leases`, the in-process `Authority`; the Authority laws run here. |
| `replica/replicatest/` | The scripted network: commands and frames in flight that a schedule delivers, duplicates, drops and reorders, with links and dropped caches; the model-based laws `ProtocolHolds` (the design model's invariants on the implementation after every op) and `SettledIsServerState` (after any faults, a drain converges on the authority's state). |
| `cmd/lease-sync/` | The binary: it chooses the memory authority and plays the script. |
| `cmd/lease-server/` | The authority over HTTP: `lease-server ADDRESS [CLIENT...]` prints `listening HOST:PORT` and serves Effect RPC at `/rpc`; with client ids, it serves only those. A pull with nothing new is held up to 25s. |

The protocol's design model is `models/replica` (`ergo test models/replica`).
