# Evaluation from Real OpenCode Sessions & Workflow History

To keep `atlas` lean and tailored to our actual multi-agent orchestration workflow rather than speculative abstractions, this evaluation combines **workbox empirical SQLite + telemetry data** (`opencode.db`, server stall profiles, `wf10` history) with **summarized findings from five MacBook session studies** (covering `dream-week`, kernel persistence vs `psychoscript`, automation suitability, `pstack` orchestration weight, and today's operational friction).

> **Privacy & Read-Only Scope:** All workbox SQLite numbers come from read-only inspection (`mode=ro`); MacBook findings are summarized cleanly from internal study reports without copying private logs verbatim. Unrelated external workspace names are excluded.

---

## 1. Combined Empirical Corpus (Workbox + MacBook)

### 1.1 Workbox Sessions & Telemetry (Measured Directly from `opencode.db`)

| Metric | Workbox Count | Notes |
| --- | --- | --- |
| Total sessions (`session_v2`) | **3,593** | **41** root sessions, **3,552** child subagents (`86.6 : 1` subagent-to-root ratio) |
| Messages (`session_message`) | **702,662** | Multi-turn orchestrator and subagent execution transcripts |
| Total recorded parts (`session_part`) | **360,656** | **212,205** `tool`, **131,987** `reasoning`, **16,467** `text` |
| Completed / error / streaming tool calls | **210,030 / 2,190 / 2** | `1.03%` hard tool failure rate overall |
| Wake inbox deliveries (`session_inbox`) | **371** | All `synthetic` wake deliveries |
| Persistent background `job` / `automation` rows | **5 / 2** | Negligible adoption next to **5,484** synchronous `shell(action: "wait")` calls |
| Live Bun server stalls (`stall-*.json`) | **5 recent events** | **2.04 s – 10.79 s** main-thread stalls; live Bun server RSS **1.99 – 2.90 GiB** |

### 1.2 Workbox Tool Distribution (All `212,205` Calls)

| Tool | Total | Root Calls | Errors | Mean Dur | P90 Dur | Max Dur | Workflow Reality |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `shell` | **136,695** | 0 | 1,337 | 40.8 s | 19.7 s | 8,529.9 s | **64.4%** of all tool calls (`exec` 130,308, `wait` **5,484**, `check` 508, `kill` 282, `run` 47, `list` 66) |
| `edit` | **21,414** | 0 | 121 | 0.44 s | 0.83 s | 92.6 s | Primary surgical edit tool in subagents |
| `read` | **17,125** | 0 | 212 | 1.00 s | 2.01 s | 610.6 s | Primary file reader |
| `write` | **15,068** | 0 | 50 | 0.34 s | 0.57 s | 136.8 s | File creation / rewrite |
| `patch` | **10,720** | 0 | 250 | 1.09 s | 1.74 s | 160.1 s | Overlaps `edit`; **`224` line-verification errors** (`4.1x` error rate vs `edit`) |
| `kernel` | **7,663** | **7,663** | 96 | 7.08 s | 11.85 s | 6,467.5 s | Used **100% by roots**; **15.04 hours** inside cells |
| `ask_orchestrator` | **1,343** | 0 | 52 | 0.17 s | 0.22 s | 20.5 s | Subagent escalations (`33` question-schema length failures) |
| `principles` / `skill` | **913 / 866** | 0 / 0 | 1 / 0 | 0.31 s | — | 20.1 s | Rule/skill loading |
| `webfetch` / `websearch` | **328 / 56** | 0 / 0 | 41 / 0 | 0.7–1.7 s | — | 13.8 s | Occasional external reference lookup |
| `memory_save` / `memory_search` | **3 / 2** | 3 / 2 | 0 / 0 | 0.4 s | — | 0.64 s | Essentially unused (`0.002%`; Mac study also saw only 4/2 in 105 sessions) |
| Malformed names (`message`, `mcp_*`, etc.) | **24** | 0 | **24** | — | — | — | `100%` error rate |

### 1.3 Inside Kernel Cells Across Both Hosts (`7,663` Workbox Root Cells + `13,400` Mac Root Cells)

| Observation | Workbox (`7,663` cells) | MacBook (`9,641` orch + `3,759` other roots) | Combined Conclusion |
| --- | --- | --- | --- |
| Role breakdown | **7,663** root / **0** subagent | **13,400** root / **0** subagent (1,683 direct worker calls) | Subagents never had kernel; orchestration loops drove kernel calls |
| Loops (`for` / `while`) | **4,600** (`60.0%`) | 4,097 multi-call cells (`1.9` tool calls/cell avg) | Kernel primarily cuts round trips (`~9,000` saved on Mac) + loops (`W9`) |
| `await` usage | **6,141** (`80.1%`) | Dominant control pattern (`full_codemode` blockers) | Async tool sequencing dominates over CPU compute |
| Time spent inside cells | **15.04 h** (`230` >=30 s, `51` >=120 s, max `6,467 s`) | **56%** of orch time (`38.3 h` / `68.3 h`), **65%** other roots (`19.6 h`); `190` >2 m cells burned `16.4 h` past 2 min | Unbounded waits inside cells are the #1 root stall bug |
| Explicit persistence utility | `9` closure-drop notices (`[not kept across restarts: ...]`) | **66%** reused a binding, yet **91%** were static constants (`T0` 3,419x) or small wrappers (`sh2`, `out`, median **208 chars**); only **6.1%** (`591`) held live values, **1.2%** (`115`) live across restart | Most "persistence" is tiny session constants + short-lived result vars, NOT arbitrary long-lived JS heaps |
| Restart notice bloat | Noisy restore logs (`[new kernel; restored find, I, ...]`) | **337** orch restarts dumped **201,494** names (**1.03 MB**, ~250k tokens) + **1.23 MB** in other roots; **77** non-restorable bindings (`closures`, `Response`, `Database`) | Restore notices must stay silent unless bindings actually dropped (`<= 160` chars) |
| Node/Bun OS/NPM APIs | `Bun.spawn`/`spawnSync`: **2** (`0.026%`), `import`: **276** | `npm` / `node:` static imports: **5** (`0.05%`); `Bun.file`/`write`: 2,469; `process.*`: 414 | Real scripts do **not** need full Node/npm; they only need `tools.*` (`subagent`, `message`, `queue`, `shell`, `handles`, `read`/`edit`, `ask_user_async`) + file/loop basics |
| Parallelism (`tools.all`/`map`) | `tools.all` **408**, `Promise.all`/`map` **224** | **3.7%** (`152`) actually ran >1 call concurrently | Batching saved turns more than concurrency; clean `map`/`start` fixes fan-out |

Top `tools.*` actually invoked inside the `7,663` workbox kernel cells: `message` (**3,262**), `answer_subagent` (**1,246**), `queue` (**1,220**), `subagent` (**1,014**), `subagents` (**701**), `shell` (**527**), `all` (**408**), `wake` (**274**), `job` (**227**), `write` (**191**), `ask_user_async` (**171**), `monitor` (**124**), `wake_cancel` (**122**), `map` (**84**), `wake_list` (**46**). Everything else (`docs`, `webfetch`, `pr`, `read`, `orchestrate`, `principles`, `grep`, `edit`, `skill`, `commitments`, `glob`, `verifier`) totaled `1–17` hits each across all 7,663 cells.

---

## 2. Concrete Problems & Friction Across Both Hosts

### A. Waiting & Polling Inside Turns and Cells
- **Workbox:** **5,484** `shell(action: "wait")` calls (`2,119` >= 30 m, `1,473` 10–30 m, `774` 2–10 m -> **85.1%** of numeric waits >= 2 m), **600 aborted shell calls**, **39+ blocked `guard-sleep` subagents**, **199 kernel `sleep` calls**.
- **MacBook:** **1,596** `shell:wait` calls asking for **958 ks**, `5` blocking `question` calls stalling up to **35 min**, `1` loop of six `land`s holding an orchestrator **72 min**, and `1` sleep loop stalling **275 min**.
- **Fix in `atlas`:** Strict **recursively event-driven zero-spin invariant** across the entire tree (`root <-> subagent <-> nested subagent`): blocking `question` removed globally, synchronous long `wait` eliminated, every long step returns a handle in `DELIVERS` and wakes on arrival, and cells cut cleanly at `120 s` wait / `30 s` compute (`models/kernel/kernel.ergo`, `models/restart/restart.ergo`).

### B. Heavy-Command Queue, Review Bounces & Gate Overkill (MacBook `dream-week` & Today's Runs)
- **External heavy queue deadlocks:** Workers stalled **42 and 32 minutes** waiting on a single typecheck behind the host check queue at load 7, suffered **3 workbox deadlocks**, and logged **54** `systemd-run --user --scope` errors before the 10,716-line heavy queue was deleted.
- **Process/review bloat:** Orchestrator units averaged **3.5 rounds** (up to 9–10 rounds on process-tooling units); **30% of subagent tokens** went into orchestration process/tooling overhead (`27%` review/verify, `25%` owner asks, `17%` pool); **8 of 52 accepted reviews** still sent work back; **33 of 100 `CHANGES` verdicts** raised non-defect style/strength nits; Mac Opus implementors carried a **14.2 k-char system prompt** (`26.8 k` opening tokens vs `15.4 k` Sol) heavy on round-policy verdict tables.
- **Targeted checks beat bloated merge waits:** Switching from 10–54 minute full merge checks to short targeted affected checks (**35–58 s**) caught every real fault without losing velocity, while **actual bugs** (double-wake at exact pool opening, 5.4 s Mac-only check refusal) were caught by **running built binaries** rather than code review!
- **Fix in `atlas`:** Permanently drop `host-admit` / heavy queues (`std/host` capacity signals replace them); drop round-policy taxonomies and redundant planner/review ceremonies; back every stateful rule with verified Ergo machine models (`models/*.ergo`) + runtime integration verification; keep `35–58 s` targeted verification gates cleanly inside `modules/orchestrate`.

### C. Pool Holds, Double-Wakes & Uncoordinated Cross-Host Quotas
- On the workbox, **56 Claude steps ended 74 times** on quota/pool holds totaling **64 hours** held; cross-host quota checks lacked shared fleet state (25 h diagnosis delay); today's live test caught a **double-wake at the exact opening (`now == retryAt`)**; pool holds mid-run ended subagents in error instead of parking cleanly.
- **Fix in `atlas`:** Native fleet `pool` with cross-host unsettled-start tracking, soonest-resetting under-line preference, door refusal with concrete `retryAt`, atomic single-wake delivery key (`models/pool/pool.ergo`), and automatic limit recovery (`models/limit/limit.ergo`).

### D. Restart Loss, Server Stalls & 100k-Item Cut Crashes
- Workbox OpenCode Bun server showed **2.0–10.8 s event-loop freezes** (`1.99–2.90 GiB` RSS); MacBook fork stayed pinned at `c6664062` (302 commits behind `prime-look`, blocking `request-routing` for 6.1 days and `latch` for 61 h); cells holding **100k items killed their worker on cut**; non-serializable closures (`sh`, `poolStatus`) failed noisily across reloads (`77` names on Mac, `9` errors on workbox).
- **Fix in `atlas`:** BEAM/OTP-modeled native Ergo daemon (`std/actor`, `std/machines`, `modules/sqlite`) with live FD/PTY server-to-server **cutover** (`models/cutover/cutover.ergo`) and crash **restart** reconciliation (`models/restart/restart.ergo`), lazy/capped snapshots avoiding 100k-item worker crashes, and immediate non-closure serialization diagnostics.

### E. Tool Surface Overlap & Schema Friction
- `patch` failed at **2.33%** (`250 / 10,720`, 224 line mismatches) vs `edit` at **0.56%** (`121 / 21,414`).
- Subagents bypassed orchestration rules **30 times** calling `ask_user_async` directly instead of `ask_orchestrator` (while `ask_orchestrator` failed **33 times** on 30-char header caps, `shell` failed **14 times** inventing `max_output_chars`, `read` failed **10 times** on mutually exclusive flags, and workers hallucinated `mcp_*` / `message` names **24 times**).
- Upstream OpenCode **automations** cannot settle on shells/jobs/subagents, run polling inside a 30 s budget, and steer heavyweight user prompts (`delivery: "steer"`).

---

## 3. Explicit Slim-Down & Cut List for `atlas`

Grounded in the combined workbox + MacBook metrics, `atlas` deliberately cuts:

1. **Full Bun / Node / npm / `Bun.spawn` runtime inside `kernel` cells** (`0.026%` `Bun.spawn` workbox, `0.05%` npm Mac, `133 MiB/worker` RSS, `100k`-item cut crash risk) -> replaced by an embedded confined VM behind `KernelEngine` (`KERNEL-ENGINE-PROBE`: `psychoscript`, `Luau`, or `ergoscript` spike) plus explicit serializable session scratch constants (replacing 91% of binding reuse) and capped live snapshots.
2. **External heavy-command queues (`host-admit` / `light.sh` / `nice` / `systemd-run`)** (`42m`/`32m` stalls + 3 deadlocks + 54 `systemd-run` errors) -> removed permanently; `std/host` supplies host capacity directly.
3. **Synchronous `shell(action: "wait")` past short bounds & blocking `question` tool** (`4,366` workbox waits >= 2 m + `1,596` Mac waits + `600` aborted shells + `35 m` question block) -> `DELIVERS` wake handles + `ask_user_async` globally (gated to orchestrators, denied on workers unless explicit).
4. **Redundant `patch` tool** (4x error rate of `edit`) -> unified surgical `read`, `edit`, `shell`, `search`.
5. **Process/policy ceremony & bloated prompts (`end-of-step.md` 3,188 words, 4k-char round-policy verdict tables, redundant `planner` mode/taxonomy)** -> state transitions live in `modules/orchestrate` & `atlas` state machines (`~5 k` concise worker instructions, `2–3` verdict words, targeted verification on built binaries).
6. **Low-value features:** `memory_save` / `memory_search` (`0.002%` usage -> `AGENTS.md` handles persistent repo notes), upstream OpenCode `automation` triggers for agent settles (reserved solely for optional owner calendar prompts), uncurated mega-catalog inside kernel globals (slimmed to `subagent`, `message`, `queue`, `handles`, `start.{shell,job}`, `ask_user_async`, `read`/`edit`, `map`), and noisy 600-name restore dumps.

---

## 4. 10-Task Benchmark Cell Set for `KERNEL-ENGINE-PROBE`

Derived directly from the verified kernel workloads (`4,600` loops, `6,141` awaits, `91%` small constants/helpers + `6%` live values, cut & wake continuity, and top tools `message` / `subagent` / `queue` / `shell` / `wake` / `job` / `ask_user_async` / `map`), `KERNEL-ENGINE-PROBE` (`units.md` Unit 0) runs these identical 10 Hydrogen-worker tasks on `psychoscript`, `Luau`, and the `ergoscript` spike:

| # | Benchmark Task | Grounded Session Evidence (Workbox + MacBook) | Tests Engine Capability |
| --- | --- | --- | --- |
| 1 | **Parallel status sweep** across 12 workspace units: filter ready vs blocked, format compact table | `for` loops (4,600 workbox) + `tools.all` (408) + `subagents` (701) | Loops, arrays/maps, string formatting, batched reads |
| 2 | **Fan-out build/check starts** over 6 lanes returning handles immediately without waiting | Replaces sequential `for ... await lane` (110 Mac cells) + `tools.map` (84) | Non-blocking `tools.map` / `tools.start.shell` collection |
| 3 | **Cut & wake resumption**: start slow shell past cell wait bound, pause cleanly at `Unfinished`, resume in cell 2 reading `handles.get(id)` | Fixes `190` >2 m Mac cells (`16.4 h`) & `51` workbox cells >= 120 s | Clean cut stop (later lines never execute), durable handle lookup after wake |
| 4 | **Role subagent routing**: dispatch 3 workers (`planner`, `implementor`, `verifier`) and send messages to settled peers | Top kernel tools (`subagent` 1,014 + `message` 3,262 + `answer_subagent` 1,246 + 17–27% `codemode:false` blocker) | Reaching native orchestration tools cleanly inside the cell |
| 5 | **Persistent inbox/queue drain**: read pending queue, deduplicate by unit key, dispatch top 2, update remaining queue | `tools.queue` (1,220 workbox) | Ordered collections, mutation, multi-step tool update |
| 6 | **Code-only file inspection & edit** via `tools.read`/`tools.edit` without spawning `Bun.spawn` | Replaces `Bun.file`/`Bun.spawn` (`0.026%` real need) & high `patch` error rate | Pure host-tool delegation without raw OS spawn permissions |
| 7 | **Session constant & capped live-state carry across cells**: keep path constants (`T0`) and a live result table cleanly, rejecting non-serializable closures | `91%` static bindings (`T0` 3,419x) + eliminating `77` Mac / `9` workbox closure-drop errors & noisy restore notices | Serializable scratch/lazy restore without closure leaks |
| 8 | **`tools.map` partial-cut resume**: run map over 8 items where 3 finish, 2 stay pending, 3 not started; resume `kernel.cut.maps[0].remaining` cleanly | `kernel.cut.maps` recovery (`kernel/spec.md` W9) + avoiding 100k-item memory crashes | Partial-cut journal inspection without recomputing completed items |
| 9 | **Non-blocking owner escalation**: emit `ask_user_async` with concise header, options, and working default while continuing safe dispatch | `ask_user_async` (171) + fixing 5 blocking `question` stalls (`<=35 m`) + `33` schema length rejects | Concise schema compliance & non-blocking continuation |
| 10 | **Declarative job monitor replacing `sleep` poll**: register `tools.start.job({ until })` rather than `while (true) { sleep }` | Eliminates `199` workbox `sleep` cells + `275 m` Mac sleep loop (`monitor` 124 / `job` 227) | Choosing event wake handle over imperative sleep polling |
