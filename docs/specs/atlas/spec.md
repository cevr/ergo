# Atlas Specification

`atlas` is a multi-machine agent and orchestration harness built in Ergo (`modules/atlas`, CLI/daemon binary via `examples/atlas`, specs in `docs/specs/atlas/`). It unifies external CLI coding agents (`Meta`) and an in-process provider agent loop (`Direct`) under one durable, recursively event-driven thread/turn runtime with integrated quota pooling, a wake-driven persistent code-cell kernel, multi-host coordination, and a clickable Prime + FX terminal interface built on `std/ui` and `hosts/opentui`.

Related specification documents:
- [prior-art.md](prior-art.md) — OpenCode (`prime-look` + mods), Vercel/`fx`, Pi (`pi-mono`/`pi-durable`), Tardigrade, Prime Agent, T3 Code, and Gent (our Effect/TS harness + `examples/gent`)
- [eval-from-sessions.md](eval-from-sessions.md) — empirical evaluation of workbox (3,593 sessions, 212,205 tool parts) and MacBook session studies driving the `atlas` slim-down & `KERNEL-ENGINE-PROBE` 10-task benchmark
- [t3-decisions.md](t3-decisions.md) — verified source decisions D1–D68 from `pingdotgg/t3code`
- [kernel/spec.md](kernel/spec.md) — persistent wake-driven code-cell & `DELIVERS` handle registry design folded into `atlas`
- [machines.md](machines.md) — 14 checked Ergo state machines & mutant witnesses under [models/](models/)
- [units.md](units.md) — audited stdlib prerequisites + ordered build DAG starting with `HARNESS-SPEC r2` -> `KERNEL-ENGINE-PROBE`
- [questions.md](questions.md) — closed decisions (`N1 = atlas`), `KERNEL-ENGINE-PROBE` rules (`K1`), and working defaults

---

## 0. Standing Architectural Invariants

Four hard boundaries govern every subsystem in `atlas`:

1. **Antigravity & Gemini subscription boundary (unchanged):**
   On `Meta`, Antigravity does nothing T3 Code does not already do (D20–D35): official `agy acp` stdio CLI, strict profile isolation (`GEMINI_HOME` + `SSH_CONNECTION`), never a proxy or base-URL spoof, never account rotation inside one session, never endpoint scraping outside official flows. Direct API / Gemini subscription adapters are **off by default and permitted ONLY when officially supported**.
2. **Recursively event-driven (zero spinning, never blocking across the full subagent tree):**
   Neither root orchestrators nor subagents nor nested subagents ever spin-wait or block turns polling on work. Every long operation (`shell`, `job`, `subagent`, `kernel` cut, `pool` hold, `ask_user_async` question, `timer`, `land`) immediately yields a durable handle; the caller parks and wakes cleanly once when the settling event arrives. This invariant holds **recursively across the entire tree** (`root <-> subagent <-> nested subagent`).
3. **Durable across frequent self-update restarts:**
   Because `atlas` upgrades and restarts its own server frequently, all in-flight state—threads, recursive parent-child subagent links, open handles, pending wakes, pool holds & retry openings, kernel cell snapshots/journals, open questions, and queued turns—commits to `modules/sqlite` before side effects execute (`Decide -> Commit -> Act`). On boot reconciliation:
   - External/resumable work (jobs, lands, pool holds, alarms, subagents, questions) reattaches without dropping state or sending a duplicate wake.
   - Volatile in-process work that cannot survive process replacement (`call` handles inside a dead kernel/tool worker) is settled **once** honestly as `lost` ("may have run") with one deterministic notice, never silently disappeared or blindly replayed (inspired by `@earendil-works/pi-durable` and T3 D5).
4. **Honest capabilities & tailnet identity:**
   Missing adapter capabilities stay disabled rather than emulated (D4). Owner hosts on the tailnet authenticate each other via tailnet transport identity (never trusting arbitrary LAN listeners).

---

## 1. Architecture & System Map

```
               +------------------------------------------------+
               |   atlas CLI / TUI (std/ui + hosts/opentui)     |
               |   Prime look + FX tree + clickable everything  |
               +-----------------------+------------------------+
                                       | atlas.Api (HTTP/WS/CLI/MCP)
               +-----------------------v------------------------+
               |                atlas serve daemon              |
               |  - Thread / Session / Turn / Subagent Tree     |
               |  - Boot Reconcile & Durable Wake Registry      |
               |  - Integrated Fleet Pool (pace/holds/resets)   |
               |  - KernelEngine + Cut Bounds + Call Handles    |
               |  - Questions (ask_user_async), Queue, Guard    |
               +--------+------------------------------+--------+
                        |                              |
          +-------------v------------+    +------------v-------------+
          |      Meta Adapters       |    |      Direct Engine       |
          | - Claude Code (SDK)      |    | - std/llm providers      |
          | - Codex (app-server)     |    | - Agent loop + compact   |
          | - Antigravity (agy acp)  |    | - Tools: read/edit/      |
          | - Generic ACP / Pane     |    |   shell/search/handles   |
          +--------------------------+    +--------------------------+
```

- **Storage & Journal:** Single transactional ledger in `modules/sqlite` (WAL mode, schema version fence, single-transaction event + projection + outbox commit matching `modules/orchestrate`'s store contract) paired with `std/journal` (`Append` with `Stale` fencing, `Snapshot`, pure `replay`).
- **BEAM/OTP Actor & Machine Runtime (`std/actor`, `std/machines`, `std/tasks`):**
  Every thread, subagent tree, adapter runner, pool coordinator, wake scheduler, and kernel worker runs as typed `std/machines` state machines hosted by `std/actor` (`Actor[S, E, D, F]` bounded mailboxes, `Send`/`Ask`/`Drain`, `Supervise` / `SuperviseDurableWith` using `OneForOne` / `AllForOne` / `RestForOne` supervision policies from `std/tasks`, entry-scoped child trees via `NewHost` + `ChildIn`, and Erlang-style exit monitors `Watch`, plus `Registry`). Architectural invariants follow [`docs/research/actor-model-and-durability.md`](../../research/actor-model-and-durability.md): incarnation fencing at storage boundaries, versioned **process handover** rather than live stack-frame migration, and zero silent drop of volatile process work.
- **Capabilities & API:** Declarative `atlas.Api` compiled with `ergo gen api|cli|mcp` alongside per-thread bearer-scoped `/mcp` endpoints (D11–D13).
- **Cross-crate reuse:** Uses `modules/sqlite`, `modules/orchestrate` (commitments/verdicts), `modules/board` (shared live views), `std/actor`, `std/machines`, `std/journal`, `std/llm`, `std/tasks`, `std/ui`, `hosts/opentui`, and `std/host` capacity signals.

---

## 2. One Thread & Recursive Subagent Model

Every conversation—whether executed by Claude Code, Codex app-server, Antigravity ACP, a generic ACP binary, or `atlas`'s `Direct` engine—projects into one uniform domain model (`turn`, `session`, `request`, `rotation`):

- **Thread:** Durable conversation tree (`ThreadID`, optional `parentThreadID`, optional `linkOrigin`, active leaf, model/provider selection with provenance `origin`, queued user prompts, pending requests, open handles). Lives across process exits and server upgrades.
- **Session:** Ephemeral live attachment between a thread and an adapter runtime (`Meta` child process group or `Direct` runner). One owner instance tag `<serverID>/<epoch>` identifies volatile in-memory calls owned by that server process.
- **Turn:** One step from prompt dispatch to completion, interrupted cancellation, or failure classification (`usage_limit` with `resetAt` | `provider_error` | `transport_error` | `permission_error` | `validation_error` | `unknown`). Bookkeeping settles out of band so late post-turn bookkeeping never keeps the active indicator lit (D6).
- **Recursive Subagent Tree:**
  - Any session (`root`, `planner`, `implementor`, `verifier`, `reviewer`, nested worker) may spawn child subagents subject to role policy (`agents` policy matrix).
  - Neither a parent nor a child blocks synchronously awaiting descendants past short budgets: long subagent work returns a `subagent` handle immediately (`cut` / `tools.start.subagent` / async spawn).
  - **Hand-back law:** A child session with any open handle cannot finalize its hand-back to its parent; settles wake the **child** first, which then produces one final hand-back waking the **parent** (`kernel/spec.md` W8).

---

## 3. Server-Update Cutover, Handle Transfer & Restart Durability

Frequent binary upgrades (`atlas` rebuilding and replacing itself mid-orchestration) operate in two modes—graceful live **cutover** (`models/cutover/cutover.ergo`) and crash/cold **restart** (`models/restart/restart.ergo`)—both modeled on BEAM/OTP ownership/fencing ([`docs/research/actor-model-and-durability.md`](../../research/actor-model-and-durability.md)), Pi's crash-settlement discipline ([prior-art.md](prior-art.md)), and [models/session/session.ergo](models/session/session.ergo) / [models/kernel/kernel.ergo](models/kernel/kernel.ergo):

### 3.1 Live Server-to-Server Cutover & FD Transfer (`models/cutover/cutover.ergo`)

1. **Quiesce & prepare (`PhaseQuiesce`):** `OldServer` receives reload signal (`std/signal`), spawns `NewServer`, stops admitting conflicting local mutations, and persists open handle leases.
2. **Atomic single-owner handle & FD transfer (`PhaseTransfer`):** Open listeners, MCP pipes, and child subprocess stdio/PTY FDs pass across a Unix domain socket via `SCM_RIGHTS` (`std-net-unix-fd`, `std-exec-pty-reattach`), while journaled ownership leases transfer atomically per handle (`OwnerOld -> OwnerNew`, never `OwnerBoth`).
3. **Commit fence (`PhaseCommitted`):** `NewServer` writes `CommitCutover` into `modules/sqlite` / `std/journal`, permanently fencing `OldServer` (any post-commit write from `OldServer` rejects `Stale` -> `NoSplitBrainAfterCommit`).
4. **Safe rollback before commit (`AbortNewBeforeCommit`):** If `NewServer` fails or aborts *before* `CommitCutover`, transferred handles/leases revert cleanly to `OldServer` with zero silent drops (`SafeRollbackBeforeCommit`).
5. **Drain & wake once (`PhaseDone`):** After `CommitCutover`, `OldServer` drains cleanly without orphan leaks; any handle event settling before, during, or after handover delivers **at most one wake** under its immutable delivery ID (`AtMostOneWakeAcrossCutover`).

### 3.2 Crash / Cold Restart Reconciliation (`models/restart/restart.ergo`)

1. **Write-before-act (`Decide -> Commit -> Act`):** Intent, delivery IDs (`claimID`, `wakeDeliveryId`), parent-child edges, active `pool` holds, `kernel` binding diffs, and open questions commit in SQLite *before* spawning workers or dispatching turns.
2. **Idempotent claim & wake keys:** Every wake delivery carries one immutable key created when the wake is armed (e.g. `wake:<handleID>` or `hold:<threadID>:<retryAt>`). If the server crashes between commit, delivery, and acknowledgement, rebooting reconciles once under the same ID—never spawning two turns (`NoDuplicateWakeAcrossRestart`).
3. **Resumable vs. non-resumable work on reboot (`BootReconcile`):**
   - **Survives & reattaches:** `job` (external OS process tracked by PID/start time/reattachable handles), `land` (workspace merge/push phase ledger), `timer` (monotonic/wall deadline), `question` (open `ask_user_async` form), `hold` (pool quota wait at `retryAt`), `subagent` tree edges & un-delivered child hand-backs.
   - **Process-bound volatile work (`call` handles from a retired process owner):** Retired via `Retired { owner: deadOwner }` across `openOwners(state)`. Settled **once** with disposition `lost` ("may have run; never blindly replayed") and reported to the owning session. Live owners on sibling hosts or concurrent instances stay untouched (`OtherOwnerReloadKeepsLiveCalls`).
4. **Never drop live work silently:** On boot, every unsettled thread/handle remains open under a living owner, transfers cleanly to `NewServer`, settles honestly once as `lost`, or delivers its pending completion—never orphaned silently (`ZeroSilentDrops`).

---

## 4. Two Paths to a Model (`Meta` and `Direct`)

Both paths implement one `Adapter` interface exposing a static `Capabilities` record (`models/direct/direct.ergo`):

### 4.1 `Meta` Path (External Harnesses)

Reuses T3 Code's verified adapter discipline ([t3-decisions.md](t3-decisions.md)):
- **Claude Code:** Long-lived `claude-agent-sdk.query` child in its own detached process group; per-account `CLAUDE_CONFIG_DIR` (never mutating `HOME`); `priority: "next"` steering; SDK `canUseTool` permission/ask bridge; HTTP `/mcp` injection; `usage_EXPERIMENTAL...` + stream `rate_limit_event` limit probes; native `fork` and `resume`.
- **Codex:** Shared `codex app-server` JSON-RPC child per `CODEX_HOME` instance; shadow-home symlink overlay with real per-account `auth.json` for conversation continuity across accounts; `turn/steer` with `expectedTurnId`; per-thread `mcp_servers` overrides; `account/rateLimits/read` & updates.
- **Antigravity:** Official `agy acp` stdio CLI (`>= 1.5.0`) under §0 strict limits (`GEMINI_HOME` + `SSH_CONNECTION` so personal OAuth stays file-scoped without OS keychain prompts; native `session/resume` never `session/load`; no spawn during minute health probe because CLI unpacks ~1 GiB; read-only profile SQLite error parsing + HTTP quota probe).
- **Generic ACP / Minimal Pane Fallback:** Strict standard ACP agents; raw terminal pane capture via Rex only when no structured protocol exists.

### 4.2 `Direct` Path (`atlas`'s Built-in Agent Loop)

Runs directly inside `atlas` using `std/llm` streaming clients and `modules/sqlite` thread persistence (adopting Effect/TS Gent's actor-per-session, single-tx store, and cached-prefix lessons via Ergo's `examples/gent/` foundation):
- **Supported providers:**
  - Codex subscription
  - OpenCode Console
  - OpenCode Go
  - Anthropic subscriptions
  - Antigravity / Gemini subscriptions: **ONLY when officially supported** (guarded capability flag off until explicit support lands; never proxy or account rotation).
- **Direct Agent Loop:**
  1. Assemble prompt, `AGENTS.md` / runtime `<runtime_info>`, active branch context, and tool schema.
  2. Stream response from provider (`NoRetryAfterFirstDelta`: once any streamed token/delta reaches the thread, transport retries refuse—partial progress is preserved cleanly instead of duplicated).
  3. Dispatch requested tool calls in parallel batches where pure reads allow, gating side effects through permission policy (`ask` | `auto_edit` | `yolo`) and integrated `guard` shell safety rules.
  4. Route blocking/long work immediately to async handles (`job`, `subagent`, `ask_user_async`, `kernel` cell cut).
  5. Compact context cleanly when token budgets cross configured thresholds without severing `assistant toolUse -> toolResult` branch validity.
  6. Spawn child subagents on the shared recursive thread tree and park cleanly until woken by their settlement.

---

## 5. Integrated Quota Pool (`atlas/pool`, Not a Plugin)

The account/quota pool moves directly from `mods/pool` into `atlas`'s core runtime ([models/pool/pool.ergo](models/pool/pool.ergo), [models/limit/limit.ergo](models/limit/limit.ergo), [models/rotation/rotation.ergo](models/rotation/rotation.ergo)):

- **Accounts & Scoped Windows:** Each configured account tracks independent `5h`, `7d`, and model-scoped `7d_*` windows plus explicit reset timestamps (`resetAt`) and freshness (`measured` vs `stale`/`unknown`).
- **Cross-host Fleet Usage Authority (q208):** Hosts publish account window readings and **unsettled starts** (`inflightSpend`) through the fleet store/peer RPC. Placement reads the sum of observed spend plus unsettled starts across all hosts so two hosts cannot overspend the same quota slack simultaneously (`FleetSpendCountsInflight`).
- **One Shared Fleet Weekly Pace Line:** Target 99% weekly capacity consumed smoothly toward weekly `resetAt` with 5% leeway (`lineOf(resetAt, now)`). Among eligible measured accounts currently under `lineOf`, admission prefers the **soonest-resetting** account (`SoonestUnderLinePreferred`), avoiding stalled idle resets.
- **Door Refusal Naming Opening:** When every eligible account is exhausted, held, or over pace without push clearance, the admission door returns a structured `Hold` naming the earliest concrete future `retryAt` opening timestamp (`RefusalNamesFutureOpening`).
- **Single-Wake Hold Delivery:** A thread (root or subagent) hitting a limit or closed admission door stores an explicit hold keyed by `(threadID, retryAt)` with immutable delivery token `wakeDeliveryId(threadID, retryAt)`. Any clock tick, rescan, or post-restart sweep running at the exact opening (`now == retryAt`) executes an atomic transition so **at most one wake delivers per hold opening** (`OneWakeAtExactOpening`, `NeverWakeBeforeOpening`). If a held child closes or dies before `retryAt`, parent receives one cancellation/gone notice and zero wake goes to the dead child.
- **Resets & Owner-Approved Pushes:** Account exhaustion resets automatically claim at most once per `(account, window, period)` (`OneResetClaimPerPeriod`). Time-boxed owner pushes raise pace ceilings strictly inside declared scope and revert automatically at `endsAt` (`PushRespectsTimeBox`).

---

## 6. Integrated Code-Cell Kernel & Wake Registry (`atlas/kernel`, Not a Plugin)

Incorporates the wake-driven cell kernel specification from [kernel/spec.md](kernel/spec.md) directly into `atlas` ([models/kernel/kernel.ergo](models/kernel/kernel.ergo), [models/wake/wake.ergo](models/wake/wake.ergo)):

- **Engine Interface & Selection (`K1`):** `KernelEngine` defines `{ eval, restore, snapshot, cut, kill, subscribe }` cleanly decoupled from `Handles` and tool policy. First engine is chosen by measured evidence in **`KERNEL-ENGINE-PROBE`** comparing:
  1. `psychoscript` (`anomalyco/psychoscript` — read first, Go bindings; probe starts here),
  2. `Luau`,
  3. `ergoscript` (design note + smallest spike only).
  Bun remains a deferred comparison only if measured data demands it.
- **Bounded Cell Execution:**
  - `waitMs = 120_000` (2 min total tool wait per cell across all tools including queued kernel slot waits), `timeoutMs = 30_000` (30 s compute), `cutGrace = 1_000`, `askMs = 600_000` (`interrupt`-mode open ask pause), `idleMs <= 600_000` (measured).
  - Non-yielding CPU loops past budget terminate (`kill`). Yielding cells past budget end via **cut** (`started` | `waiting` | `asking` | `idle`), persist variable bindings lazily (`cut.calls` ordered call journal, `cut.maps` partitioned indices + remaining item values), retire the transient worker, and return immediately.
- **Order Safety & Explicit Start vs `Unfinished`:**
  - Explicit async calls (`background: true`, `tools.start.*`, unawaited call, `tools.map`) yield handles synchronously so cells proceed.
  - Any ordinary awaited call answering with running work (`Unfinished`) immediately ends the cell at that `await` (`cut.started`) so subsequent lines never read missing results.
  - A detached `call` handle that later answers `Unfinished` transitions to terminal `Handed` and emits **no registry wake letter** (`HandedNeverDoubleWakes`); the spawned entity's own deliverer wakes the session once.
- **`DELIVERS` Total Single-Deliverer Registry:**
  - `call` (cut cell call) and `land` (queued merge/push debt) -> wake registry.
  - `timer` and `job` (including polling `monitor` jobs) -> wakes entry engine.
  - `subagent` -> subagent host/session lifecycle (and pool latch for held starts).
  - `shell` -> host process exit notice.
  - `question` -> `ask_user_async` store.
- **Burst Coalescing & Notices:** Idle settles coalesce over a `2_000 ms` burst window into one short wake letter (`<= 2_000 chars`, no bulk payload in DB; `call` outputs live in capped owner RAM only). Restarts emit a `[new kernel; ...]` notice (`<= 160 chars`) **only** when volatile bindings were actually lost (`NoticeOnlyWhenBindingsLost`).
- **Role Caps & Per-Root Visibility:** Up to `32` live role-agent kernel workers per host (`Slots` queue FIFO behind role agents; roots never queue). Per-root hourly active kernel cell % is tracked and flagged once an hour above `15%`, naming top 3 longest cells.

---

## 7. Questions, Queue & Safety Guards

- **Async-only Questions (`ask_user_async`):** Synchronous blocking `question` tools are removed across every session and every role (q231). Every prompt to the owner carries an explicit working assumption and lets work continue or settle on wake.
- **Persistent Thread Queue:** Queued follow-up inputs and steers survive restarts in SQLite without duplicating delivered turns.
- **Safety Guards (NOT Heavy Queuing):** All shell safety refusals ported from `mods/guard` enforce static & runtime checks inside `atlas`'s tool runner:
  - Refuse `rm` on filesystem roots, home directory, or Git repo roots (`Destructive`, fail-closed when cwd/roots cannot be resolved).
  - Refuse kill-by-pattern (`pkill`, `killall`) targeting `atlas`'s own server PID.
  - Refuse `git worktree add` where copy-on-write workspaces (`work`) apply (`FullCopy`).
  - Refuse `work gc` when an actively creating workspace exists.
  - Refuse subagent foreground `sleep` exceeding idle threshold (`Idle`).
  - Refuse Chrome DevTools/headless launches lacking `--use-mock-keychain --password-store=basic` (`KeychainPrompt`).
  - **Explicitly removed:** `mods/guard`'s heavy command admission queue (`host-admit`, `light.sh`, `nice`) is dropped permanently; host capacity checks read `std/host` metrics directly (`nproc`, 1-min loadavg, `MemAvailable`, disk free).

---

## 8. Prime + FX Clickable TUI (`std/ui` + `hosts/opentui`)

`atlas` ships a thin terminal client over `atlas.Api` built in Ergo using `std/ui` and `hosts/opentui` ([models/tui/tui.ergo](models/tui/tui.ergo)):

- **Visual Design Synthesis:**
  - **From `prime` (`prime-look`):** Dark obsidian surface with `#7c6faf` violet focus accent; live state diamonds (`◆` running, `◈` question/permission wait, `◇` idle); sticky parent -> child breadcrumb bar; single compact dock line (`agents`, `questions`, `jobs`); 6-row auto-scrolling live subagent strip; tabbed composer modes; live status timer (`Executing · Ns`).
  - **From Vercel `fx`:** Heavy left vertical rail (`┃`) framing user messages; crisp solid step indicator (`●`) for assistant turns; clean tree connectors (`├` / `└`) pairing `thinking` nodes and tool invocations directly with their outputs instead of nested boxes.
- **Click-Focus Parity Invariant:**
  Every focusable UI element—session rows, live subagent strip rows, collapsible tool-call tree rows, inline file paths, `ask_user_async` question options, clickable links, and composer tabs—carries a hit target in layout coordinates. Releasing a mouse click (`MouseUp`) without an active text-selection drag dispatches the **exact same action** as activating the focused target via keyboard (`FocusableHasClickTarget`, `ClickMatchesKeyAction`, `DragSelectSuppressesClick`).

---

## 9. Explicit Migration Table: What Moves from OpenCode Mods

| OpenCode Mod / Fork Component | Target in `atlas` / Ergo | Notes |
| --- | --- | --- |
| `mods/pool` | `atlas/pool` | Fleet weekly pace line, multi-host unsettled starts, exact-opening single wake, resets, owner pushes |
| `mods/kernel` + `mods/wake` | `atlas/kernel` + `atlas/handles` | Behind `KernelEngine` (`KERNEL-ENGINE-PROBE`), `waitMs=120s` / `timeoutMs=30s`, `DELIVERS` total table, 2 s burst wake |
| `mods/questions` | `atlas/questions` | Async-only `ask_user_async` across all roles; synchronous `question` tool deleted |
| `mods/queue` | `atlas/queue` | Durable per-thread prompt queue across restarts |
| `mods/commitments` | `modules/orchestrate` | Commitment tracking & unit/receipt verdicts live in `modules/orchestrate`, not in dotfiles or `atlas` core |
| `mods/guard` (safety rules) | `atlas/tools/guard` | `Destructive` `rm`/reset/clean, kill-PID check, `worktree` vs `work` refusal, `work gc` reservation check, subagent long `sleep`, Chrome keychain check |
| `mods/guard` (heavy queue / `host-admit` / `nice`) | **REMOVED** (uses `std/host`) | Replaced by direct `std/host` capacity metrics & process bounds (`GOMAXPROCS`, `GOMEMLIMIT`, `timeout`) |
| `mods/prime` + `prime-look` UI + `fx` tree | `atlas/tui` (`std/ui` + `hosts/opentui`) | Prime diamonds/dock/subagent strip + FX `┃`/`●`/`├`/`└` tree + universal mouse-click parity |
| External orchestrator board views | `modules/board` integration | Uses Ergo's existing `modules/board` over `std/mcp/httpmcp` (`boardhost` machine) |

---

## 10. Multi-Host Tailnet Architecture, Board & Wellness

- **Tailnet Peer Topology:** Each machine runs its own `atlas serve` owning local repos, workspace CoW checkouts, credentials, and SQLite state (D57–D58). Peers on the owner's tailnet verify each other via Tailscale identity and expose full cross-host thread viewing, subagent delegation, and quota pool synchronization. Delegated threads carry immutable `linkOrigin` tags and namespaced idempotency keys (D63–D65).
- **Board Surfaces:** Integrates natively with Ergo's `modules/board` (`models/boardhost/boardhost.ergo`), tracking host liveness, active orchestrators, pending questions, kernel % alerts, and fleet quota slack.
- **Declarative Wellness Checks (q210) & Host Capacity (`std/host`):** Periodic passive/non-spawning wellness probes inspect SQLite consistency, pool freshness, orphaned process groups, stale wake holds, and `std/host` resource headroom (`nproc`, 1-min loadavg, `MemAvailable`, disk free), publishing typed `WellnessReport` statuses instead of crashing silently or queuing externally.
