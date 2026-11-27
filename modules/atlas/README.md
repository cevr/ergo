# atlas

Pure domain module (`modules/atlas`) defining the core data types, capability contracts, transition functions, finite-state machines, and verified laws for `atlas` across `Meta` and `Direct` agent modes.

| Package | What |
| --- | --- |
| `adapter` | Driver kinds (`Meta` / `Direct`), static `Capabilities`, `Adapter` contract (never emulates unsupported ops, `Supervised` fallback, Antigravity direct gate, continuation keys) |
| `handle` | `DELIVERS` single-deliverer registry, handle dispositions (`Handed`, `Lost`, etc.), wake delivery IDs, bounded lost-binding notices and wake letters |
| `turn` | Shared `TurnLifecycle`, failure classification (`FailureUsageLimit` with `ResetAtMs`), `NoRetryAfterFirstDelta`, one-shot approval/question `RequestLifecycle`, and single-armed `LimitRecoveryLifecycle` |
| `session` | Live `SessionLifecycle`, at-most-one process per session, `/mcp` token expiry & turn-only revival, and `RotationLifecycle` (`RotationFixed` vs `RotationPooled`) |
| `thread` | Durable `Thread` record, `MetaPath` / `DirectPath` parity transitions, prompt queue, out-of-band bookkeeping settle, and `LinkOrigin` fencing |
| `subagent` | Recursive parent-child `Tree`, zero-spin `NodeParked` tree, subtree control bounds, and refusal of child final hand-back while holding open handles or descendants |
| `reconcile` | Pure `BootReconcile` over persisted handles, wakes, turns, requests, and subagent tree: live-owner isolation, once-only volatile `DispLost` settlement, zero silent drops |
| `cutover` | Live server-to-server `CutoverLifecycle` (`CutoverQuiesce` -> `CutoverTransfer` -> `CutoverCommitted` -> `CutoverDone`), single-owner lease invariant, pre-commit rollback, post-commit fencing |
| `memory` | Pure `Store` operation contract & laws, `Decide`/`Fold`/`Scope` state machine, and mutex/channel-backed `MemStore` covering commands, threads/edges, subagent trees, handles/wakes/holds, `ask_user_async`, prompt queues, pool windows/recovery, kernel snapshots/cuts, fenced leases, and `BootReconcile` |
| `sqlite` | Durable WAL SQLite `Store` and `WALStore` implementing `memory.Store` with single-transaction `Decide -> Commit -> Act`, schema version fencing, `BootReconcile` recovery, and `SqliteAgreesWithMemory` + crash-reopen laws |

## Verification

```sh
CGO_ENABLED=0 GOMAXPROCS=2 GOMEMLIMIT=4GiB GOTOOLCHAIN=local go build -p 2 -trimpath -buildvcs=false -o /tmp/ergo ./cmd/ergo
cd modules/atlas
GOMAXPROCS=2 GOMEMLIMIT=4GiB GOTOOLCHAIN=local ERGO_FALLBACK=off /tmp/ergo fmt --check ./...
GOMAXPROCS=2 GOMEMLIMIT=4GiB GOTOOLCHAIN=local ERGO_FALLBACK=off /tmp/ergo lint ./...
GOMAXPROCS=2 GOMEMLIMIT=4GiB GOTOOLCHAIN=local ERGO_FALLBACK=off /tmp/ergo lint --preset strict ./...
GOMAXPROCS=2 GOMEMLIMIT=4GiB GOTOOLCHAIN=local ERGO_FALLBACK=off /tmp/ergo test -j 2 --fresh ./...
```
