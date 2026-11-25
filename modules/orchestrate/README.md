# orchestrate

An Ergo module (`modules/`) implementing the pure, zero-I/O event-sourced domain model, contracts, in-memory reference store, and transactional SQLite store for the cross-harness `orchestrate` CLI and MCP server.

| Package | What |
| --- | --- |
| `orchestrate` | Pure domain types, formal lifecycle machines (`UnitLifecycle`, `QuestionLifecycle`), `Command`/`Envelope`/`Event`, `Decide`/`Apply`/`Fold`, pure state queries, `Store` transactional contract + laws, `Api` contract + `Service` adapter across all 5 v1 tool groups (units, receipts, commitments, queue, questions) |
| `orchestrate/memory` | In-memory reference provider of `orchestrate.Store` feeding `Store` and `Api` contract laws plus seeded model-based sequence verification |
| `orchestrate/sqlite` | Single-transaction SQLite provider of `orchestrate.Store` over `modules/sqlite` (`BEGIN IMMEDIATE` WAL, `schema_version` fence, `events` + `commands` + folded projection tables updated atomically in one `sql.Tx`), verified by `Store` contract laws (`:memory:` and file-backed WAL), memory/SQLite agreement across reopen, mid-transaction rollback atomicity, and concurrent multi-writer WAL tests |

## Laws

- `ReplayMatchesIncremental`: `Fold(allEvents) == incrementalState` over generated command sequences.
- `IdempotentCommandID`: replaying an accepted `CommandID` with the same payload yields no new events and identical state; replaying with a different payload fails `CommandReused`.
- `UnitLifecycle` and `QuestionLifecycle`: formal finite-state machines exhaustively checked by `ergo test`.
- Domain invariants: `UnitLifecycleOrder`, `ReceiptRecordedReadable`, `CommitmentLandedMonotone`, `QueueUpsertRemoveRoundTrip`, `QuestionAnsweredOnce`.
- `Store` contract laws: `StoreReplayMatchesSnapshot`, `StoreIdempotentExecute`, `StoreRefusedLeavesStateUnchanged`, `StoreQueriesMatchSnapshot` — run identically against `memory.Store`, `sqlite.Store` (`:memory:`), and `sqlite.WALStore` (file-backed WAL).
- SQLite persistence checks (`PersistenceChecks`): `SqliteAgreesWithMemory` (valid, invalid, duplicate, and `CommandReused` sequences across close/reopen), `SchemaVersionFenceRefusesFutureVersion`, `RollbackAtomicityPreservesConsistency`, and `ConcurrentMultiWriterWAL`.
