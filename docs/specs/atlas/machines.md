# Atlas — Checked State Machines and Laws

Every stateful subsystem in `atlas` is specified as an exhaustive finite-state Ergo machine under [models/](models/) (`M1`–`M14`), checked via [models/check.sh](models/check.sh) with `ERGO_FALLBACK=off` (`ergo test`). Every new model (`M9`–`M14`) had all safety invariants verified RED first before implementing the passing design, alongside explicit `Mutant` machines proving each `Breaks` fault is reachable and caught.

**Verified totals across all 14 checked models (`ERGO_FALLBACK=off`, exit 0):** **203** passing laws, witnesses, and mutants (**109** across baseline `M1`–`M8`, **94** across new `M9`–`M14`).

## Summary Index

| ID | Model | Design States / Edges / Longest Shortest | Laws (`always` + `sometimes` + Mutants) | Core Invariants Enforced |
| --- | --- | --- | --- | --- |
| **M1** | [turn](models/turn/turn.ergo) | `71` / `852` / `9` | **15** (5 / 6 / 4) | Turn sent once across crash/lost reply, explicit outcome on end, terminal settlement, no stranded stop |
| **M2** | [session](models/session/session.ergo) | `91` / `1001` / `12` | **14** (6 / 4 / 4) | At most one live process per session, no orphan proc, token dies with session, no stale token, only a turn revives idle tokens |
| **M3** | [limit](models/limit/limit.ergo) | `86` / `946` / `10` | **14** (5 / 5 / 4) | At most one auto-resume per `(thread, run, resetAt)`, never early, never expired, never over pending request |
| **M4** | [request](models/request/request.ergo) | `94` / `1128` / `7` | **13** (4 / 5 / 4) | One answer per approval/question request, delivered once, never to dead session, none left open without session |
| **M5** | [rotation](models/rotation/rotation.ergo) | `261` / `3132` / `14` | **14** (5 / 5 / 4) | One session per thread, never rotate mid-turn, fixed account pinned, never place blindly on exhausted account |
| **M6** | [wake](models/wake/wake.ergo) | `44` / `308` / `7` | **13** (5 / 4 / 4) | Single wake delivery per fire across crash, never early, cancelled never fires, armed timer survives restart |
| **M7** | [boardhost](models/boardhost/boardhost.ergo) | `422` / `5486` / `13` | **14** (5 / 4 / 5) | Single settled `modules/board` writer, one host per epoch once linked, no silent lost post, no duplicate post, follower holds no log |
| **M8** | [bounds](models/bounds/bounds.ergo) | `26` / `1040` / `6` | **12** (4 / 4 / 4) | Bounded delegation depth, bounded fan-out, bounded total run budget, parent control restricted strictly to own subtree |
| **M9** | [direct](models/direct/direct.ergo) | `330` / `4290` / `13` | **14** (5 / 4 / 5) | `Meta` vs `Direct` parity on shared thread model, direct Antigravity/Gemini gated unsupported, no retry after first delta, write approval gate, never emulate missing steer |
| **M10** | [pool](models/pool/pool.ergo) | `388` / `3880` / `14` | **20** (8 / 4 / 8) | **No double-wake at exact opening (`now == retryAt`)**, never early, soonest-resetting under-line preference, unsettled fleet starts counted, refusal names opening, gone child never woken, time-boxed push, single reset claim |
| **M11** | [kernel](models/kernel/kernel.ergo) | `68` / `952` / `7` | **19** (8 / 4 / 7) | Registry handle told once, `Handed` unfinished never double-wakes, no code runs after unfinished await, bound always cuts, `DELIVERS` exclusivity, foreign-owner reload leaves live calls intact, clean restore emits zero lost notice, roots never queue |
| **M12** | [tui](models/tui/tui.ergo) | `1792` / `21504` / `7` | **13** (4 / 5 / 4) | Every focusable element (session/subagent/tool/path/question/link/tab) has click target matching keyboard activation, drag-select suppresses click, disabled ignores both |
| **M13** | [restart](models/restart/restart.ergo) | `76` / `760` / `11` | **14** (5 / 3 / 6) | **Recursively zero-spin subagent tree** (`Root -> Child -> Grandchild`), child waits for descendants, zero silent drops across server restart, zero duplicate wakes across restart, volatile calls settle honestly once `lost` |
| **M14** | [cutover](models/cutover/cutover.ergo) | `30` / `300` / `8` | **14** (5 / 4 / 5) | **Single-owner invariant** across FD/lease transfer, zero silent drops, safe rollback before commit fence, no split-brain after commit fence, at most one wake across live server handover |

---

## Baseline Subsystems (`M1`–`M8`)

- **M1 (`turn`):** Laws `SentAtMostOnce`, `EndedHasOutcome`, `TerminalSettles`, `NoStrandedStop`, `AcceptedIsSentOrReported`. Mutants `Replay`, `LeaveRequest`, `EarlyCancel`, `ForgetUnsure`.
- **M2 (`session`):** Laws `OneProcess`, `NoOrphan`, `ProcessOnlyWhileAttached`, `TokenDiesWithSession`, `NoStaleToken`, `OnlyATurnRevives`. Mutants `KeepToken`, `LeaveProcess`, `CallRevives`, `SecondProcess`.
- **M3 (`limit`):** Laws `ResumedOncePerRun`, `NeverBeforeReset`, `NeverOnExpired`, `NeverOverARequest`, `LimitedIsResumedOrReported`. Mutants `ResumeTwice`, `ArmExpired`, `ResumeEarly`, `ResumeOverAsk`.
- **M4 (`request`):** Laws `OneAnswer`, `DeliveredAtMostOnce`, `NeverToADeadSession`, `NoOpenRequestWithoutSession`. Mutants `LastAnswerWins`, `DeliverAfterExit`, `Redeliver`, `ExitLeavesOpen`.
- **M5 (`rotation`):** Laws `OneSessionAThread`, `NeverMidTurn`, `FixedAccountNeverMoves`, `NeverPlacedOnLimited`, `TurnHasSession`. Mutants `RotateMidTurn`, `RotateFixed`, `PlaceBlind`, `KeepOldSession`.
- **M6 (`wake`):** Laws `OneMessage`, `NeverEarly`, `CancelledNeverFires`, `FiredHasMessage`, `ArmedSurvivesRestart`. Mutants `NoDedupe`, `FireEarly`, `FireCancelled`, `TimerInMemory`.
- **M7 (`boardhost`):** Laws `OneHostWhenSettled`, `OneHostAnEpochOnceMet`, `NoLostMessage`, `NoDuplicate`, `FollowerHoldsNoLog`. Mutants `AutoPromote`, `DropOnDemote`, `NoDedupe`, `NoTieBreak`, `BlindTakeover`.
- **M8 (`bounds`):** Laws `DepthBounded`, `FanOutBounded`, `BudgetHeld`, `OnlyInOwnSubtree`. Mutants `NoDepth`, `NoFan`, `NoBudget`, `ActOnAny`.

---

## New Round-2 Subsystems (`M9`–`M14`)

### M9 — Two Paths (`Meta` vs `Direct`) & Direct Loop ([direct.ergo](models/direct/direct.ergo))
- **`always` laws:** `NoDirectAgyUnlessSupported`, `NoRetryAfterFirstDelta`, `NoWriteToolWithoutApproval`, `NeverEmulateMissingCapability`, `OneThreadInvariant`.
- **`sometimes` witnesses:** `MetaClaudeCompleted`, `DirectCodexCompleted`, `RetryBeforeDeltaWorked`, `CompactedBetweenTurns`.
- **Mutants:** `UnsupportedDirectAgy`, `RetryAfterDelta`, `UnapprovedWriteTool`, `EmulateSteer`, `SplitThread`.

### M10 — Integrated Fleet Pool & Exact-Opening Single Wake ([pool.ergo](models/pool/pool.ergo))
- **`always` laws:** `OneWakeAtExactOpening`, `NeverWakeBeforeOpening`, `SoonestUnderLinePreferred`, `FleetSpendCountsInflight`, `RefusalNamesFutureOpening`, `GoneChildNotWoken`, `PushRespectsTimeBox`, `OneResetClaimPerPeriod`.
- **`sometimes` witnesses:** `WokeOnceAtOpening`, `RetryReplyAtOpeningStillOneWake` (lost-reply re-scan at exact `now == retryAt` keeping `WakeSent == 1`), `ChildGoneNotifiedOnce`, `PushUsedThenExpired`.
- **Mutants:** `DoubleWakeAtExactOpening`, `WakeBeforeOpening`, `IgnoreSoonestUnderLine`, `IgnoreFleetInflight`, `RefusalWithoutFutureOpening`, `WakeGoneChild`, `ExpiredPushActive`, `DoubleResetClaim`.

### M11 — Persistent Kernel & `DELIVERS` Wake Registry ([kernel.ergo](models/kernel/kernel.ergo))
- **`always` laws:** `HandleSettledToldOnce`, `HandedNeverDoubleWakes`, `NoCodeAfterUnfinished`, `CutBeforeNextWaitPastBound`, `DeliversExclusiveByKind`, `OtherOwnerReloadKeepsLiveCalls`, `CleanRestoreEmitsNoLostNotice`, `RootNeverQueuesSlots`.
- **`sometimes` witnesses:** `DetachedCallToldOnce`, `HandedOffToldOnceByOwner`, `ReloadBLeavesALive`, `LostBindingsToldOnce`.
- **Mutants:** `DoubleWakeUnfinished`, `RunCodeAfterUnfinished`, `SkipCutAtBound`, `RegistryStealsShell`, `ForeignOwnerReloadRetires`, `CleanRestoreEmitsNotice`, `QueueRootCell`.

### M12 — Clickable Prime + FX UI & Focus Parity ([tui.ergo](models/tui/tui.ergo))
- **`always` laws:** `EveryFocusableHasClickTarget`, `ClickMatchesKeyAction`, `DragSelectSuppressesClick`, `DisabledIgnoresClickAndKey`.
- **`sometimes` witnesses:** `MouseOpenedFilePath`, `MouseAnsweredQuestion`, `MouseOpenedLink`, `MouseSelectedSubagent`, `KeyAndClickMatched`.
- **Mutants:** `UnclickableElement`, `ClickDiffersFromKey`, `ClickDuringDragSelect`, `ActivateDisabledTarget`.

### M13 — Recursive Event-Driven Subagent Tree & Server Restart Durability ([restart.ergo](models/restart/restart.ergo))
- **`always` laws:** `ZeroSpinParkedTree`, `ChildWaitsForDescendants`, `NoSilentDropAcrossRestart`, `NoDuplicateWakeAcrossRestart`, `VolatileSettledOnceAsLost`.
- **`sometimes` witnesses:** `NestedTreeSurvivedRestart`, `VolatileLossReportedOnceAfterRestart`, `DurableJobCompletedAcrossRestart`.
- **Mutants:** `BlockingWaitInChild`, `PrematureChildHandBack`, `DropWorkOnRestart`, `DoubleWakeOnRestart`, `SilentVolatileLoss`, `ReplayVolatileOnRestart`.

### M14 — Live Server-to-Server Cutover & Handle/FD Transfer ([cutover.ergo](models/cutover/cutover.ergo))
- **`always` laws:** `SingleOwnerInvariant`, `ZeroSilentDrops`, `SafeRollbackBeforeCommit`, `NoSplitBrainAfterCommit`, `AtMostOneWakeAcrossCutover`.
- **`sometimes` witnesses:** `CleanCutoverCompleted`, `SafeRollbackRestoredOld`, `PostCommitCrashRecoveredNew`, `EventSettledAcrossCutoverOnce`.
- **Mutants:** `DualOwnerTransfer`, `BadRollbackDrop`, `PostCommitSplitBrain`, `PrematureOldExit`, `DoubleWakeCutover`.

---

## Remaining Integration Seams Verified in Code Units

State machines `M1`–`M14` model protocol/concurrency safety exhaustively over finite domains. The following lower-level or wire-level behaviors are verified via dedicated integration/contract tests in their respective implementation units ([units.md](units.md)):
1. **`SCM_RIGHTS` FD & PTY transfer across OS sockets** (`P1`, `P2`, `atlas-serve-core`): verified by real OS process-pair handover tests over `UnixNetwork`.
2. **Tailnet peer forwarding & `T3-Mode-Limit` intersection** (`atlas-multi-host`, D61–D65): non-transitive links, `linkOrigin` fencing, idempotency hashing.
3. **Shared `codex app-server` thread multiplexing & Codex timeout recycling** (`atlas-meta-codex`, D47): single `initialize`, request deadline retirement.
4. **`KernelEngine` real VM boundary tests** (`KERNEL-ENGINE-PROBE`, `atlas-kernel-wake`): 7 benchmarks + 10-task Hydrogen worker correctness suite.
