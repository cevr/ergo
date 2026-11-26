# Atlas — Ordered Build Units

Builds cleanly upward from the verified machine models in [models/](models/) (14 models including `cutover` and `restart`), the foundational modules already landed on Ergo `main` (`modules/sqlite`, `modules/orchestrate`, `modules/board`, `std/actor`, `std/machines`, `std/journal`, `std/tasks`, `std/mcp/httpmcp`, `std/llm`, `std/ui`, `hosts/opentui`), and the audited stdlib/runtime prerequisites below.

> **First units immediately after `HARNESS-SPEC r2`:** **`KERNEL-ENGINE-PROBE`** (K1 engine selection) alongside the four audited **Ergo Stdlib/Runtime Prerequisites** (`P1`–`P4`) needed for BEAM/OTP actor durability and live server-to-server handle/FD cutover.

## Dependency Graph

```
HARNESS-SPEC r2 (done: spec + 14 checked Ergo models incl. cutover & restart)
       |
       +---------------------------------------+---------------------------------------+
       v                                       v                                       v
0. KERNEL-ENGINE-PROBE                  P1. std-net-unix-fd                     P3. std-actor-durable-supervision
   (KernelEngine iface;                    (Unix sockets + SCM_RIGHTS              (atomic SQLite/outbox commit hook,
    psychoscript -> Luau ->                 FD passing for listeners/FDs)           dynamic actor supervisor, deadline
    ergoscript spike; 7 checks)                |                                    recovery, bidirectional Link)
       |                                       v                                       |
       |                                P2. std-exec-pty-reattach                      |
       |                                   (PTY alloc/resize, live Detach,             |
       |                                    FD reattach, detached exit collect)        |
       |                                       |                                       |
       |                                P4. std-signal-reload (USR1/USR2 cutover sig)  |
       |                                       |                                       |
       +-------------------+-------------------+---------------------------------------+
                           v
1. atlas-core (Thread/Subagent/Turn/Capabilities/BootReconcile/Cutover deciders)
       |
       v
2. atlas-store (modules/sqlite WAL tx store + cutover/crash ledger)
       |
       v
3. atlas-api (atlas.Api, /mcp tokens, HostCapacity via std/host, WellnessReport)
       |
       +--------------------+--------------------+
       v                    v                    v
4. atlas-pool      5. atlas-kernel-wake   6. atlas-direct-loop
(fleet pace,       (chosen probe engine,   (std/llm loop, tools,
 holds, resets,     cuts, DELIVERS,         guard safety rules,
 exact-open wake)   ask_user_async, queue)  compact, subagents)
       |                    |                    |
       +----------+---------+         +----------+
                  v                   v
           8. atlas-serve-core   7. atlas-provider-direct
           (atlas serve daemon,   (Codex sub, Console, Go,
            live cutover P1-P4,    Anthropic subs; Gemini gated off)
            BootReconcile)
                  |
     +------------+------------+------------+
     v            v            v            v
9. meta-claude 10. meta-codex 11. meta-agy 12. atlas-multi-host
                                             (tailnet sync, board)
                  |
                  v
           13. atlas-tui (std/ui + hosts/opentui; Prime + FX; click parity)
                  |
                  v
           14. atlas-importers-cutover (history import + dotfiles mods retirement)
```

---

## Audited Ergo Stdlib & Runtime Status (Baseline `e760a9bbc`)

Direct source audit of `internal/sketch/std/{actor,machines,journal,net,exec,process,os,signal,tasks}`, `runtime/v1/`, and [`docs/research/actor-model-and-durability.md`](../../research/actor-model-and-durability.md):

### What Already Exists on `main`

- **`std/actor`, `std/machines`, `std/journal`, `std/tasks`**: Bounded mailbox `Actor[S, E, D, F]`, `StartMachine` / `Supervise` / `SuperviseDurable` / `SuperviseDurableWith`, entry-scoped child trees (`NewHost` + `ChildIn`), Erlang-style `Watch` exit monitors, `Registry`, fenced `Journal[S, E]` (`Append` + `Stale` conflict error, `Snapshot`, pure `replay`), and `OneForOne` / `AllForOne` / `RestForOne` supervision strategies.
- **`modules/sqlite`, `modules/orchestrate`, `examples/orchestrate`**: `BEGIN IMMEDIATE` WAL SQLite driver, `schema_version` fence, commitments/verdicts ledger (`ORCH-1`–`ORCH-4`).
- **`modules/board`, `std/llm`, `std/mcp/httpmcp`, `std/ui`, `hosts/opentui`**: Live board over HTTP MCP, streaming LLM gateway (`examples/gent`), terminal UI + focus/mouse primitives.
- **`std/host`** (sibling host unit): Target source for `HostCapacity` metrics (`nproc`, 1-min loadavg, `MemAvailable`, disk free).

### Audited Stdlib / Runtime Gaps Required Before `atlas-serve-core` & Cutover

1. **`P1. std-net-unix-fd` (`std/net` + `runtime/v1/net`):** `internal/sketch/std/net/net.ergo` accepts only `"host:port"` TCP addresses (`ListenFailed` rejects non-`host:port`). Meanwhile `runtime/v1/exec/exec_sandbox_linux.go` already passes FDs across `*net.UnixConn` via `syscall.UnixRights` / `ParseUnixRights`, proving kernel mechanics exist internally. Missing: public `UnixNetwork` (`ListenUnix`, `DialUnix`) and `SCM_RIGHTS` FD transfer (`SendFDs`, `RecvFDs`) needed for passing listeners, MCP sockets, and harness/PTY FDs during live `atlas` cutover.
2. **`P2. std-exec-pty-reattach` (`std/exec` + `std/term` + `runtime/v1/exec`):** `internal/sketch/std/exec/exec.ergo` `Detached` carries only `{ Pid, Start, Name }` with `Alive` and `Kill` (`ToCaller` fails `Spawn` with `"no caller outlives the program to read it"`). Missing: PTY allocation/resize (`std/term` handles current process terminal only), live `Detach` transition from a supervised child, reattaching transferred pipe/PTY FDs across server replacement, and exit-status waiting/collection on reattached PIDs.
3. **`P3. std-actor-durable-supervision` (`std/actor` + `std/journal` + `runtime/v1/actor*`):**
   - **SQLite/outbox atomic step commit:** Bridge `std/actor`'s transition save hook (`Save(prev, next, sig, outbox)`) with `modules/sqlite` (`BEGIN IMMEDIATE`) so machine state, leased incarnation fence, and outbox wakes commit in one transaction rather than JSON/text `journal/file` alone.
   - **Dynamic actor supervision tree (`DynamicSupervisor` / `SuperviseSet` for actors):** `ChildIn` binds child actors to one parent state entry (`Actor.ergo`: `Host` closes on state change, so snapshotting machines with hosts is forbidden). Long-lived session/subagent trees need OTP-style dynamic supervision of sibling actors (`OneForOne`/`RestForOne`/`AllForOne`) plus lightweight bidirectional `Link` alongside `Watch`.
   - **Monotonic deadline preservation on restart:** `std/time` tracks `HasMono` / `TimeHostMono`, whereas `runtime/v1/actor.go` restarts actor timers via relative `timerSpan` without persisting/re-arming target deadlines across `SuperviseDurable` crashes or server upgrades (causing backoff/hold timers to drift or repeat full duration on recovery).
4. **`P4. std-signal-reload` (`std/signal` + `runtime/v1/signal.go`):** `internal/sketch/std/signal/signal.ergo` `Stop` covers only `SIGINT`, `SIGTERM`, `SIGHUP`. Missing: graceful reload signal (`USR1` / `USR2`) to trigger live cutover handover cleanly.

---

## Unit Specifications

### `P1`–`P4` (Prerequisite Ergo Stdlib & Runtime Foundations)
Built first on their own package tests and Ergo provider laws (`std/net`, `std/exec`, `std/actor`, `std/journal`, `std/signal`, `runtime/v1/`). Unblock live server-to-server FD/PTY handover (`atlas-serve-core`) and crash/restart timer + supervision durability.

### 0. `KERNEL-ENGINE-PROBE` (FIRST `atlas` UNIT — Settling `K1`)
- **Goal:** Define `atlas`'s minimal Go/Ergo `KernelEngine` interface (`Eval`, `Restore`, `Snapshot`, `Cut`, `Kill`, `BindTool`, `OnAsyncSettle`) and benchmark **exactly three candidate engines** behind it before writing any production kernel logic.
- **Candidates (exact list and order):**
  1. **`psychoscript`** (`https://github.com/anomalyco/psychoscript`): read first, build bindings from Go; **probe starts with this engine**.
  2. **`Luau`**: embedded Luau runtime behind `KernelEngine`.
  3. **`ergoscript`**: an interpreted Ergo with Ergo's static guarantees — **short design note and smallest-possible spike only** inside this unit.
  *(Bun remains a deferred comparison only on real workload numbers after an embedded engine is running.)*
- **Mandatory 7 measurements (recorded per candidate on the identical cell suite):**
  1. Cold start latency (ms)
  2. Time per cell execution (us/ms across trivial & tool-calling cells)
  3. State persistence kept across successive cells (`restore` + `snapshot` cost)
  4. Async settle waking the session when an external call finishes after cell return/cut
  5. Added binary size overhead (MiB)
  6. Build complexity (`cgo` required vs pure Go; cross-compile impact)
  7. First-try model reliability: run the **10 real-workflow cell tasks** derived from workbox + MacBook OpenCode sessions in [eval-from-sessions.md](eval-from-sessions.md) §4 through a **Hydrogen** worker per candidate language and count first-try passing cells
- **Acceptance:** Benchmark report + winning engine recommendation committed under `docs/specs/atlas/` with reproducible probe scripts and raw timings for all three candidates.

### 1. `atlas-core`
- **Owns:** `modules/atlas` pure domain packages (`thread`, `session`, `turn`, `subagent`, `adapter`, `handle`, `reconcile`, `cutover`).
- **Scope:** Shared `Thread`/`Session`/`Turn` state transitions across `Meta` and `Direct`, recursive parent-child subagent invariants (`parentThreadID`, zero child final hand-back while holding open handles), `Adapter.Capabilities` (never emulate), `BootReconcile` & live `Cutover` state machines matching checked Ergo models (`turn`, `session`, `request`, `direct`, `restart`, `cutover`).
- **Waits on:** `HARNESS-SPEC r2` (`KERNEL-ENGINE-PROBE` and `P1`–`P4` run ahead/beside).

### 2. `atlas-store`
- **Owns:** `modules/atlas/sqlite` + `modules/atlas/memory`.
- **Scope:** Event-sourced `modules/sqlite` store implementing `Decide -> Commit -> Act` in single transactions: command idempotency table, threads & branch edges, recursive subagent tree, handles (`DELIVERS`), wakes/holds, `ask_user_async` questions, prompt queue, pool windows/starts/pushes, kernel cell snapshots/journals, fenced server ownership leases, and boot recovery queries (`openOwners`, pending unsettled deliveries). Linearizability & crash-reopen parity laws (`SqliteAgreesWithMemory`).
- **Waits on:** Unit 1.

### 3. `atlas-api`
- **Owns:** `modules/atlas/api`, `examples/atlas` skeleton, per-thread `/mcp` handler.
- **Scope:** `atlas.Api` typed RPC/CLI/MCP surface (`ergo gen`), per-thread SHA-256-hashed bearer `/mcp` tokens (D11–D13), non-blocking `HostCapacity` port wired to `std/host`, and periodic non-spawning `WellnessReport` checks (q210).
- **Waits on:** Unit 2.

### 4. `atlas-pool`
- **Owns:** `modules/atlas/pool`.
- **Scope:** Moves `mods/pool` into `atlas`: account registry, `5h` / `7d` / model-scoped windows, shared fleet pace line (99% weekly target + 5% leeway), soonest-resetting under-line placement preference, fleet unsettled-start accounting (`inflightSpend`), door refusal naming concrete `retryAt`, atomic held-turn single-wake (`wakeDeliveryId(threadID, retryAt)` blocking double-wake at `now == retryAt` and gone-child phantom wakes), one-claim exhaustion resets, and time-boxed owner pushes. Proven against [models/pool/pool.ergo](models/pool/pool.ergo), [limit](models/limit/limit.ergo), and [rotation](models/rotation/rotation.ergo).
- **Waits on:** Unit 3.

### 5. `atlas-kernel-wake`
- **Owns:** `modules/atlas/kernel`, `modules/atlas/handles`, `modules/atlas/questions`, `modules/atlas/queue`.
- **Scope:** Wires the engine selected in `KERNEL-ENGINE-PROBE` behind `KernelEngine` and folds in [kernel/spec.md](kernel/spec.md): `120s` wait / `30s` compute / `10m` asking bounds, `cut.started` / `cut.calls` / `cut.maps`, `Unfinished` -> `Handed` terminal hand-off, `DELIVERS` total single-deliverer registry, `2s` burst coalesced wake letter (`<=2000 chars`), lost-bindings notice only when lost (`<=160 chars`), 32-worker role slot queue (roots never wait), hourly >15% per-root cell flag, async-only `ask_user_async` across every role (no blocking `question`), and persistent prompt FIFO queue. Proven against [models/kernel/kernel.ergo](models/kernel/kernel.ergo) and [wake](models/wake/wake.ergo).
- **Waits on:** Unit 0 (`KERNEL-ENGINE-PROBE`) and Unit 3.

### 6. `atlas-direct-loop`
- **Owns:** `modules/atlas/direct`, `modules/atlas/tools`, `modules/atlas/guard`.
- **Scope:** Native in-process agent loop on `std/llm`: `NoRetryAfterFirstDelta`, parallel read batching, surgical core tools (`read`, `edit`, `shell`, `search`, `handles`, `kernel`, `subagent`, `ask_user_async`, `job`), explicit permission modes (`ask` | `auto_edit` | `yolo`), recursive zero-spin subagent spawning & settlement, branch-preserving compaction, and **ported `mods/guard` shell safety refusals** (`Destructive`, `FullCopy`, server-PID kill guard, `work gc` guard, subagent long `sleep`, Chrome mock-keychain check; **excluding** removed `host-admit`/`light.sh`/`nice`).
- **Waits on:** Unit 3 (and integrates Unit 5 hooks for `kernel` / `ask_user_async`).

### 7. `atlas-provider-direct`
- **Owns:** `modules/atlas/providers/direct`.
- **Scope:** Direct subscription/API clients for **Codex subscription**, **OpenCode Console**, **OpenCode Go**, and **Anthropic subscriptions**. Antigravity / Gemini direct stays **explicitly gated false unless officially supported** and strictly honors §0 (never proxy, never account rotation).
- **Waits on:** Units 4 and 6.

### 8. `atlas-serve-core`
- **Owns:** `modules/atlas/serve`, `examples/atlas` runtime binary.
- **Scope:** BEAM/OTP-modeled daemon supervision over `std/actor` (`P3`) and `std/tasks`, live server-to-server **cutover & FD/PTY transfer** (`P1`, `P2`, `P4`, [models/cutover/cutover.ergo](models/cutover/cutover.ergo)), cold crash `BootReconcile` ([models/restart/restart.ergo](models/restart/restart.ergo)), pool-hold resume scheduling, Rex pane helper integration, and clean shutdown.
- **Waits on:** Units `P1`–`P4`, 4, 5, and 6.

### 9. `atlas-meta-claude`
- **Owns:** `modules/atlas/providers/claude`.
- **Scope:** `claude-agent-sdk.query` managed process group, `CLAUDE_CONFIG_DIR` isolation, `priority: "next"` steer, `canUseTool` permission/ask bridge, HTTP `/mcp` injection, native `fork` & `resume`, usage probes, plus reattachable subprocess supervision across server cutovers (`P2`).
- **Waits on:** Unit 8.

### 10. `atlas-meta-codex`
- **Owns:** `modules/atlas/providers/codex`.
- **Scope:** Shared `codex app-server` JSON-RPC instance per home, `CODEX_HOME` shadow overlay for continuation keys, `turn/start` & `turn/steer`, per-thread `mcp_servers` overrides, `account/rateLimits/read` & updates (D47–D56), plus reattachable stdio FD transfer across cutovers (`P1`, `P2`).
- **Waits on:** Unit 8.

### 11. `atlas-meta-agy-acp`
- **Owns:** `modules/atlas/providers/antigravity`, `modules/atlas/providers/acp`.
- **Scope:** Antigravity `agy acp` stdio CLI (`>=1.5.0`) strictly bounded to T3 Code D20–D35 (`GEMINI_HOME`, `SSH_CONNECTION`, read-only SQLite usage errors, HTTP quota summary only where T3 already does it, no spawn health check), strict generic ACP adapter, and minimal pane fallback.
- **Waits on:** Unit 8.

### 12. `atlas-multi-host`
- **Owns:** `modules/atlas/peer`, `modules/atlas/board`.
- **Scope:** Tailnet host identity verification, cross-host quota pool reading & unsettled-start synchronization (q208), read-only remote transcript streaming & `linkOrigin` fencing with namespaced idempotency keys (D57–D65), `modules/board` synchronization ([models/boardhost/boardhost.ergo](models/boardhost/boardhost.ergo)), and multi-host `std/host` wellness aggregation.
- **Waits on:** Units 4 and 8.

### 13. `atlas-tui`
- **Owns:** `modules/atlas/tui`.
- **Scope:** Ergo terminal client on `std/ui` and `hosts/opentui` rendering Prime visual language (`#7c6faf`, `◆ ◇ ◈`, breadcrumb, dock, 6-row scrolling subagent strip, composer tabs, `Executing · Ns`) + `fx` transcript hierarchy (`┃` user rail, `●`/`├`/`└` call and `thinking` tree) with strict mouse-click parity (`MouseUp` without drag-select matches keyboard focus target activation across rows, file paths, tool calls, sessions, options, and links) verified by [models/tui/tui.ergo](models/tui/tui.ergo).
- **Waits on:** Units 8 and 12.

### 14. `atlas-importers-cutover`
- **Owns:** `modules/atlas/import`, migration verification.
- **Scope:** Idempotent `atlas import` commands for existing Claude, Codex, and OpenCode session histories; documented cutover retiring dotfiles `mods/pool`, `mods/kernel`, `mods/wake`, `mods/questions`, `mods/queue`, and `mods/guard` (plus `mods/prime` UI overrides) in favor of `atlas`, while keeping commitments in `modules/orchestrate` and boards in `modules/board`.
- **Waits on:** Units 9, 10, 11, 12, and 13.
