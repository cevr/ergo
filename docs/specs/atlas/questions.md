# Atlas — Questions, Closed Decisions, and Prototype Probes

Everything needed to start building `atlas` is either decided by the owner or isolated into a single measured prototype (`KERNEL-ENGINE-PROBE`). No build unit blocks on an open guess.

## Closed Naming Decision (N1)

- **N1 (Name) — CLOSED:** **`atlas`** (plain name, no puns).
  - Binary & CLI: `atlas` (`atlas serve`, `atlas run`, `atlas status`, …).
  - Ergo module & API: `modules/atlas`, `atlas.Api`, runnable binary `examples/atlas` (matching repo `modules/` + `examples/` layout), specs under `docs/specs/atlas/`.
  - Why: Owner rejected pun names (`"Something less like of a pun"`) and selected `atlas`.

## Prototype Settlement for Kernel Engine (K1)

- **K1 (First kernel engine) — PROBE BEFORE COMMIT:** Instead of guessing the first embedded runtime for `atlas`'s persistent wake-driven cell kernel, build **`KernelEngine`** as one Go/Ergo interface first and benchmark **exactly three candidate engines** in unit **`KERNEL-ENGINE-PROBE`** (the first unit after `HARNESS-SPEC r2` in [units.md](units.md)):
  1. **`psychoscript`** (`https://github.com/anomalyco/psychoscript`): read first, reach from Go via bindings; **start here**.
  2. **`Luau`**: embedded Luau VM behind `KernelEngine`.
  3. **`ergoscript`**: an interpreted Ergo with Ergo's guarantees — **only a design note and the smallest-possible spike** inside `KERNEL-ENGINE-PROBE`.
- **Mandatory 7 measurements** collected identically on every candidate across the same benchmark cell set:
  1. Cold start latency
  2. Time per cell execution
  3. State persistence kept across cells
  4. Async settle waking the session when a call finishes later
  5. Added binary size
  6. Build complexity (`cgo` required or pure Go)
  7. Model first-try cell reliability (the same 10 tasks run through one Hydrogen worker per language; pass count recorded)
- **Bun** stays the **later comparison** only: if a measured workspace later shows a Bun kernel materially outperforms the chosen embedded engine on a workload that matters, Bun runs behind the same `KernelEngine` interface on data.

## Resolved Owner Decisions (Round 1, Round 2, and Kernel)

| ID | Topic | Decision | Where specified |
| --- | --- | --- | --- |
| **Q-Antigravity** | Antigravity & Gemini subscriptions | On `Meta`, nothing T3 Code does not already do (D20–D35): ACP stdio CLI, never a proxy, never account rotation. Antigravity / Gemini subscriptions via `Direct` **only when officially supported** (gate off by default) | [spec.md](spec.md) §0, §4 |
| **Q-Direct** | Direct provider path | Support Direct adapters alongside Meta for Codex subscription, OpenCode Console, OpenCode Go, Anthropic subscriptions, with `atlas`'s own agent loop (`read`, `edit`, `shell`, `search`, permissions, compaction, subagents) and one shared Thread/Turn model | [spec.md](spec.md) §4, [models/direct/direct.ergo](models/direct/direct.ergo) |
| **Q-BEAM-Cutover** | BEAM/OTP actor durability & live cutover | Ground `atlas` in `std/actor`, `std/machines`, `std/journal`, and `std/tasks`; build audited stdlib prerequisites first (`std-net-unix-fd`, `std-exec-pty-reattach`, `std-actor-durable-supervision`, `std-signal-reload`) for live FD cutover & zero-drop restarts | [spec.md](spec.md) §1, §3, [units.md](units.md), [models/cutover/cutover.ergo](models/cutover/cutover.ergo), [models/restart/restart.ergo](models/restart/restart.ergo) |
| **Q-Pool** | Fleet quota pool | Built into `atlas`, not a plugin: accounts, `5h`/`7d`/model windows, one shared fleet pace line across hosts, soonest-resetting under-line preference, door refusal with `retryAt`, held-turn single-wake at `retryAt`, measured resets, owner-approved time-boxed pushes | [spec.md](spec.md) §5, [models/pool/pool.ergo](models/pool/pool.ergo) |
| **Q-Kernel** | Persistent cell kernel & wakes | Built into `atlas` behind `KernelEngine`: 2 min wait / 30 s compute / 10 min asking cuts, `cut.calls` & `cut.maps`, `DELIVERS` total single-deliverer registry, 2 s burst wake letter, `ask_user_async` as the only question form everywhere | [spec.md](spec.md) §6, [kernel/spec.md](kernel/spec.md), [models/kernel/kernel.ergo](models/kernel/kernel.ergo) |
| **Q-TUI** | Interface & mouse parity | Prime-inspired + FX-inspired TUI on `std/ui` + `hosts/opentui`: every focusable element (row, file path, tool call, session, question option, link, tab) is clickable and dispatches the same action as keyboard activation | [spec.md](spec.md) §8, [models/tui/tui.ergo](models/tui/tui.ergo) |
| **Q-Mods** | OpenCode mods migration | Retire dotfiles mods cleanly into `atlas` (`pool`, `kernel`, `wake`, async `questions`, `queue`, `guard` safety refusals), `modules/orchestrate` (`commitments`), and `std/host` (host capacity); reject heavy queue / `host-admit` / `nice` | [spec.md](spec.md) §9 |
| **q208** | Cross-host account limits | Track and pace fleet account usage across hosts (`atlas` peers share readings and unsettled starts); do not maintain isolated per-host blind spots | [spec.md](spec.md) §5, §10, [models/pool/pool.ergo](models/pool/pool.ergo) |
| **q209** | Idle thread retention | Automatic `usage_limit` resume once per `(thread, run, resetAt)` when limit clears and auto-resume is on; never spam loops | [spec.md](spec.md) §5, [models/limit/limit.ergo](models/limit/limit.ergo) |
| **q210** | Host wellness checks | Periodically verify host/service/database/bridge invariants automatically with typed status & per-root alerts (never silence broken state) | [spec.md](spec.md) §10 |
| **q230–q232** | Kernel bounds & questions | Wait bound `120_000 ms`; `ask_user_async` is the sole question form across all roles; every role agent receives the kernel behind measured slot caps (`maxLive: 32` role workers; roots never queue) | [kernel/spec.md](kernel/spec.md), [spec.md](spec.md) §6 |
| **Q-Tailnet** | Peer authentication & links | Tailnet identity authenticates owner hosts (`Direct` / `Tailnet` transport security); remote peer links grant full owner access between owner hosts with namespace-isolated idempotency keys | [spec.md](spec.md) §10, [t3-decisions.md](t3-decisions.md) §F |

## Working Defaults for Minor Open Mechanics (Reversible During Build)

These do not block `KERNEL-ENGINE-PROBE`, core, or pool units; confirm or refine from measurements as each unit runs:

1. **Multi-host limit & pool authority placement:** Default is deterministic leader or designated home host (`hosts` table in `atlas`) holding the authoritative pool event ledger (`modules/sqlite`), with peers reporting `StartAttempt`, `ObservedWindow`, and `RateLimitError` via RPC and caching bounded read-only leases when temporarily disconnected (never overspending unmeasured headroom beyond a small leased slice). Unit `pool` measures reconnection drift on the simulated two-host fault harness.
2. **Default permission mode (`auto_edit` vs `supervised`):** Keep T3 D4 behavior on `Meta` (`Supervised` fallback when an adapter rejects a mode; never emulate) and explicit `ask` / `auto_edit` / `yolo` in `Direct`, persisted per workspace profile with config default `auto_edit` for non-destructive workspace ops under `guard` safety rules.
3. **Antigravity on macOS vs headless Linux workbox:** Start Antigravity `Meta` driver only on hosts with an installed compatible CLI (`>= 1.5.0`) and user sign-in; other hosts mark Antigravity capability absent cleanly rather than blocking `atlas serve`.
4. **Live `std/host` metrics package readiness:** If the sibling `std/host` unit has not landed when `direct-loop` / `pool` lands, wire `atlas`'s internal `HostCapacity` port so it compiles against `std/host` directly upon arrival (same shape: CPU count, 1-min load average, available memory, disk free).
5. **History importers:** Keep existing Claude (`~/.claude`), Codex (`~/.codex`), and OpenCode SQLite/session importers behind explicit one-shot `atlas import` commands so `atlas` starts clean on its own `modules/sqlite` store without touching external DBs unless invoked.
