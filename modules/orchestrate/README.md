# orchestrate

An Ergo module (`modules/`) implementing the pure, zero-I/O event-sourced domain model, contracts, and reference in-memory store for the cross-harness `orchestrate` CLI and MCP server.

| Package | What |
| --- | --- |
| `orchestrate` | Pure domain types, formal lifecycle machines (`UnitLifecycle`, `QuestionLifecycle`), `Command`/`Envelope`/`Event`, `Decide`/`Apply`/`Fold`, pure state queries, `Store` transactional contract + laws, `Api` contract + `Service` adapter across all 5 v1 tool groups (units, receipts, commitments, queue, questions) |
| `orchestrate/memory` | In-memory reference provider of `orchestrate.Store` feeding `Store` and `Api` contract laws plus seeded model-based sequence verification |

## Laws

- `ReplayMatchesIncremental`: `Fold(allEvents) == incrementalState` over generated command sequences.
- `IdempotentCommandID`: replaying an accepted `CommandID` with the same payload yields no new events and identical state; replaying with a different payload fails `CommandReused`.
- `UnitLifecycle` and `QuestionLifecycle`: formal finite-state machines exhaustively checked by `ergo test`.
- Domain invariants: `UnitLifecycleOrder`, `ReceiptRecordedReadable`, `CommitmentLandedMonotone`, `QueueUpsertRemoveRoundTrip`, `QuestionAnsweredOnce`.
- `Store` contract laws: `StoreReplayMatchesSnapshot`, `StoreIdempotentExecute`, `StoreRefusedLeavesStateUnchanged`, `StoreQueriesMatchSnapshot` (shared across `memory` and SQLite implementations).
