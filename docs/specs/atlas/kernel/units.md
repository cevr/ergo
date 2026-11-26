# kernel-wake — historical dotfiles units, revision 3 (superseded by `../units.md` for `atlas`)

*Retained as the detailed breakdown of cut, handle registry, slot queue, hoisting, and role policy mechanics incorporated into `atlas` units (`KERNEL-ENGINE-PROBE`, `kernel-iface`, `kernel-wake`) in [../units.md](../units.md).*

Eleven units, smallest first within what each waits on. Laws and counterexamples by number are in `spec.md`. Acceptance files are in `acceptance/`; all are red on main `bcf7101f` on their own assertions (`acceptance/red.log` and one later `texts.test.ts` run: 90 tests, 68 red, 22 green guards). Every unit is plugin only.

| # | Unit | Laws | Mode | Waits on | Acceptance (red now) | Review |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | kernel-notice | W5c | lane | none | `notice.test.ts` | verifier |
| 2 | cell-time | W6 | lane | none | `time.test.ts` | verifier |
| 3 | handle-registry | H1–H11, M1 | **deep** | none | `handles.sim.test.ts`, `handle.qnt`, `migration.test.ts` | **Opus** |
| 4 | cell-cut | W1, W2b, W7, W9, H10, H11 | **deep** | 3 | `cut.test.ts` | **Opus** |
| 5 | start-not-wait | W2, W4 | lane | 4 | `start-not-wait.test.ts`, `texts.test.ts` (first two groups) | verifier |
| 6 | land-wakes | W2, H7 | **deep** | 3 | `land-wakes.test.ts` | **Opus** |
| 7 | kernel-light | W5a, b, d, e | lane | 4 | `light.test.ts`, `slots.test.ts` | verifier |
| 8 | one-noun | H8, vocabulary | lane | 3 | `nouns.test.ts` | verifier |
| 9 | auto-parallel | W3 | lane | 5 | `hoist.test.ts` | **Opus** |
| 10 | skill-texts | texts | lane | 5, 6 | `texts.test.ts` (third group) | verifier |
| 11 | worker-kernel | W8 | lane, then a measurement per role | 4, 5, 7, 10 | `texts.test.ts` (last group), `start-not-wait.test.ts` (W8) | verifier |

**Started:** units 1 and 2. **Start now:** unit 3: `reference.ts` has the `started` outcome (`Handed`) and `openOwners`, and its acceptance is red on its own assertions. Units 4 and 5 may start when 3 lands. Then 4 when 3 lands; 6 and 8 need only 3 and run beside 4; 5 and 7 after 4; 9 after 5; 10 after 5 and 6; 11 last.

## 1. kernel-notice

**Goal.** A new kernel that restored everything says nothing. One that lost something says `[new kernel; 600 bindings restored, 3 lost: a, b, c; kernel.bindings() lists them]`: the counts, at most three lost names then a count, within 160 characters. Bindings a restart cannot keep are named when a cell makes them, up to eight, then `… and 32 more`.
**Owns.** `mods/kernel/{answer.ts,answer.test.ts,session.ts}` (`KernelStarted` gains `lost`), `mods/kernel/model.ts` (what was lost: the last snapshot's names less the restored ones), `mods/kernel/worker/program.ts` (the `kernel` global: `kernel.bindings(filter?)` answers `[{ name, kind, bytes }]`, installed like `tools`), `worker/workers.test.ts`.
**Also add.** A worker test: `kernel.bindings()` lists a restored name; `kernel` cannot be rebound and is no binding in the next snapshot.

## 2. cell-time

**Goal.** Each cell records `CellTimed { session, startedAt, endedAt, waitedMs, computeMs, cut }` (time queued for a slot counts as in the cell). A view keeps one hour per session. The kernel answers an RPC `time({ sessionIDs })`. The status pane and `orchestrate({ action: "status" })` show `cells 4% of the last hour · 31 cells · 2 cut` per root; over 15% it is in the warning colour and the root gets one `notify` notice an hour naming the three longest cells by their description.
**Owns.** new `mods/kernel/cell-time.ts` (+ test), `mods/kernel/{model.ts,service.ts}` (a `CellTimes` contract over `ctx.storage`, so a restart keeps the hour), `cmd/kernel/server.ts`, `mods/status/**`, the status lines of `mods/orchestrate/tools.ts`.
**Also add.** `cmd/kernel/cell-time.sim.test.ts`: a restart keeps the hour; the notice fires once per hour, never per cell.
**Note.** With a 2 min bound this display is the owner's only view of the waits under 2 min (about 10.7 h of the measured 27.1 h). `waitedMs` and `cut` are zero and false until unit 4. It also gives the first reading of the workbox roots.

## 3. handle-registry (deep)

**Goal.** `mods/wake/handle.ts`: the `Handles` decider and `view`, equal to `acceptance/reference.ts` on every seeded trace; `DELIVERS`; `handleOfEntry(entry)` (a read-only view: `{ id, session, kind, state, deliverer: "wakes", note, settlesAt?, repeats?, startsSession?, delivery? }`, an entry with `firing` as `claimed` under `firing.delivery`); `letterOf(handles)` (at most 2,000 characters, eight handles then `… and N more · tools.handles.list()`, no result, metadata at most 4,000). The wakes plugin gains the registry's service (`open`, `settle`, `get`, `list`, `cancel`, `stopped`) and an RPC other mods call (`HANDLES`, a protocol file as `latch.ts` is). Records go in the wake store beside the entries, each with `version: 1` and its `owner`. The claim: at once when the session's turn runs (join), else one one-shot timer at `dueAt` (the first settle plus 2 s). A `Settle` with `started` hands the handle on (`Handed { to }`): no letter, ever. At every instance start: for each owner in `openOwners(state)` (every open call, in every session, read or not) that is proven dead, `Retired`; then `Unpark`; then every owed letter is sent again under its delivery id. An `ArchivedError` from the host parks the letter.
**Owns.** `mods/wake/{handle.ts,handle.test.ts,message.ts,model.ts,service.ts,laws.ts,memory/**,opencode/**}`, `cmd/wake/server.ts`, new `cmd/wake/handles.sim.test.ts`, new `mods/wake/handle.qnt` (a copy of the acceptance model with its mutant runs, checked in the gate).
**Also add, on the simulated host.** (a) a restart between `Claimed` and the send, and between the send and `Delivered`: one message per delivery id; (b) a settle during a compaction, and while a turn runs: no second turn (H9); (c) three settles 500 ms apart while idle: one turn (H6); (d) a deleted session: no letter; an archived one: parked, one try per start, sent on unarchive; a stopped one: admitted, no turn; (e) two instances on one store, one reloads: the other's handles untouched (H4); a dead owner found at a read; (f) the stored world of `store-bcf7101f.json` run through the plugin's own store adapter (`mods/wake/opencode/store.ts`), not only the memory one.
**Required verifier step, before the install** (spec, risk 9): on a copy of the owner's real wake store made by the verifier's batch (`/bin/cp`; never the live file), the lane's code decodes every entry unchanged and `handleOfEntry` lists the ids, notes and times that the installed build lists from the same copy. The unit is not accepted without its receipt.
**Risk.** Risks 2, 5, 9. The registry must have no code path that sends for an entry kind, a shell or a subagent. Hold `WakeStore`'s laws for the new records.
**Why deep.** A defect loses a result or tells it twice, in every session.

## 4. cell-cut (deep)

**Step 0, before any other code: the HL1 probe.** As host laws (`sim/host-laws.ts`, `tests/host-laws/`), on the simulated host and on real OpenCode at c6664062: HL1a (a runtime call run in a plugin-owned scope outlives its kernel call and writes nothing to it), HL1b (its permission ask stays answerable; a decline arrives as a typed failure), HL1c (a plugin sees the owner's stop of a session. A stop of a running execution arrives as `session.execution.interrupted` with reason `user`: confirm it. The unknown to probe: Escape and `session.stop` on an **idle** session that holds a detached call). Record each as green or red with its run in `state.md`. All green: the default is `atBound: "detach"`. Any red: the default is `"interrupt"`, the red law and its evidence go to the orchestrator with the one fork hook it would need, and the unit goes on. It is not `blocked`.
**Goal.** The two clocks and the four ends of `spec.md` (How a cell ends), with its five rules. `ProcessWorkers.layer({ timeoutMs, waitMs, atBound, askMs })`; `evaluate(session, { source, tools, call, automation?, asking? })`; `call(name, input, how)` with `how.explicit` (also set for a call the cell does not await: the worker marks a call explicit unless its promise is awaited or passed to `tools.all`/`tools.map` in the same tick it is made) and `how.remainingMs`; `settleOf(answer)` in `service.ts` (an unfinished answer of a detached call settles `started` with the id it names); `cut.started` as a list, `cut.finished`, `cut.interrupted[].mayHaveRun`, `cut.calls` (from the journal in `operations.ts`), the `asking` cut at `askMs` (600,000); `Unfinished` in `service.ts`; `tools.start.<tool>` in the worker (a proxy that marks the frame explicit); `CellResult.cut = { reason, detached, interrupted, started?, maps?, sleeps? }` (W9: `tools.map` keeps a ledger per map in the worker and reports it in the cut's answer frame; the worker counts awaited timers; the record is installed as `kernel.cut` in the next worker and rendered in the answer as one line per map: `map of 6: 2 finished, 2 running (h1, h2), 2 not started`); a `cut` request frame and its answer in `frame.ts`. The model forks each inner call in the session's calls scope (owned by the plugin, closed at the session's end and the plugin's), opens one `call` handle per detached call through `HANDLES` (record, then observe), keeps its value in memory under the caps, settles `declined` from a declined ask, cancels on stop, snapshots, retires the worker, and answers with the cut's lines. `tools.handles.get/list/cancel` in a cell. An automation's call (`Tool.Context.automation` where the fork has it) sets `automation`.
**Owns.** `mods/kernel/{frame.ts,service.ts,session.ts,answer.ts,model.ts,operations.ts,laws.ts}`, `mods/kernel/worker/{workers.ts,program.ts}` and their tests, `cmd/kernel/**`, new `mods/kernel/waiting.ts` (the policy value and its numbers), `sim/host-laws.ts`, `tests/host-laws/**`, `tests/kernel/**`.
**Also add.** `cmd/kernel/cut.sim.test.ts`: counterexamples 1, 2, 7, 17, 23, 33, 34, 35; a cut during a plugin reload (`lost`, told once, the other location's calls untouched). A measurement in `state.md`: the worker's worst time to answer a cut request under a loaded host, which fixes the 1 s grace (risk 6).
**Risk.** Risks 1, 3, 4, 6. The journal (`operations.ts`) closes with the cell: a detached call's operation reads `→ h…`, and its later end writes nothing to the call.
**Why deep.** It changes what every cell of every session does at its bound.

## 5. start-not-wait

**Goal.** The policy table of `spec.md` (What a role may do) in `opencode/toolbox.ts`: `layer(source, journal, policy)`; the clamp, the refusals and their words; the clamp of a foreground shell's limit and a subagent's `wait` to `how.remainingMs`; the mark of an answer as `Unfinished` **by what it names**: the policy's `running` (the ids of the session's running jobs from the wakes, its running shells and child sessions from the host, read at the answer) matched against the string values of the answer, with the typed table (a shell still `running`, a job start, a land's handle, a background or held subagent) as the fast path; never for a call that only read or acted on existing work; unless `how.explicit`; a root's shell handle without `wait`; the kernel's description (Texts). The role comes from the session: a session with a parent is a subagent; an automation's context is `automation`. **The question removal:** `mods/questions/ask.ts` exports `REMOVED_IDS = ["question"]`, `cmd/questions/server.ts` removes them in its registry transform (as `cmd/wake/server.ts` does `AUTOMATION_IDS`), `notATool("question")` names `ask_user_async` and its assumption, and `mods/kernel/catalog.fixture.ts` drops `question`.
**Owns.** `mods/kernel/opencode/{toolbox.ts,sessions.ts}` (+ tests), `mods/kernel/worker/{shell-handle.ts,subagent-handle.ts}` (+ tests), `mods/kernel/{description.ts,description.test.ts,tools.ts,catalog.fixture.ts}`, `cmd/kernel/server.ts`, `mods/questions/{ask.ts,ask.test.ts}`, `cmd/questions/{server.ts,server.test.ts}`, `tests/kernel.test.ts`.
**Also add.** In real OpenCode (`tests/kernel.test.ts`), with an injected `waitMs` of 5 s: a root's cell `await tools.shell({ command: "sleep 12", timeout: 600000 }); marker()` returns within 7 s, `marker` did not run, and the host's end notice wakes the session once, with no letter of ours (risk 7, H8). A session of each agent lists no `question`; `/answer` still opens its form.
**Risk.** Risks 7, 12.

## 6. land-wakes (deep)

**Goal.** `work`'s and `orchestrate`'s land answer after the cheap part with `metadata.handle`. The rest (merge, checks, push, cleanup) is an owed land in the debts (`post-land-owed.json`, a `lands` list with its phase), run in the plugin's scope and again by the next server. Its end settles a `land` handle whose letter carries `metadata.land { handle, verdict: "pushed" | "checked" | "stopped", pushed?, landed?, step? }`. The owner's stop before the push cancels it and frees the integration workspace; after the push it finishes its cleanup. Orchestrate's run tables, digest and commitments follow that outcome, not the answer's text.
**Owns.** `mods/work/{land.ts,model.ts,service.ts,owed.ts,memory/**,filestore/debts.ts}`, `cmd/work/**`, the land path of `cmd/orchestrate/server.ts`, `mods/orchestrate/{tools.ts,digest.ts}` (`pushedOf` goes), their tests, `tests/work.test.ts`.
**Also add.** `cmd/work/work.sim.test.ts`: a restart at each phase (accepted, merged, checked, pushed): one push, one letter; a land of two bases; the accept check and the post-land step still owed after the push as today; a stop before and after the push.
**Risk.** Risk 3. A phase is recorded before its act. A resumed land that finds its head already on origin skips to cleanup. `Notice` gains `metadata`; the memory adapter and `opencode/` both carry it.
**Why deep.** A defect pushes twice or drops a land.

## 7. kernel-light

**First step: measure, and record in `state.md`.** (a) From a `session export` of the orchestrator (as `kernel-benefit/report.md` did; never an `opencode.db`): the time gaps between a cell and the next cell that reads a live value. `idleMs` is the gap's 90th percentile, clamped to 1 to 10 min. The review is right that r1's 120 s was a guess. (b) A worker's start to ready, a restore of a store of 600 generated bindings, and one idle worker's resident memory.
**Goal.** W5a: the worker learns of its host's death from a pipe that closes, not a 250 ms interval. W5b: `idleMs` as measured. W5e: `mods/kernel/slots.ts`, the `Slots` decider of `slots.test.ts` (`Want { session, root }`: a root is never queued), `maxLive: 32` in `Options`; the model asks it before each cell and acts on `Dropped` (stop that idle worker) and `Queued` (a role agent's cell waits inside its own waiting budget; at the budget it sends `Left` and answers "no kernel free; nothing ran"). W5d: a restore installs each stored value as a lazy accessor and runs stored declarations at first read; a snapshot answers `carried` and the store writes only what changed.
**Owns.** `mods/kernel/worker/{program.ts,codec.ts,declarations.ts,workers.ts}` (+ tests), new `mods/kernel/slots.ts` (+ test), `mods/kernel/{frame.ts,service.ts,model.ts}`, `mods/kernel/opencode/snapshots.ts` (+ test), `cmd/kernel/server.ts`.
**Risk.** Risks 10, 11. A stored import is restored eagerly: an accessor cannot await. The cap is per host, so it is held across the plugin's instances (one count in the server, as the pool's is).

## 8. one-noun

**Goal.** One `handles` tool (`list`, `get`, `cancel`) over the registry's records, the wakes' entries (through `handleOfEntry`) and the host's shells and subagents, read at the call. `cancel` goes to the kind's owner. `wake_list` and `wake_cancel` answer as its aliases until `ALIAS_REMOVAL = "2027-01-15"`. A new `monitor` call starts a job whose runner polls (`until`, `repeat`), and a matching line wakes; a monitor pending at the install keeps its in-server poller until it ends.
**Owns.** `mods/wake/{tools.ts,job.ts,model.ts,process/**}`, `cmd/wake/{server.ts,job.ts}`, their tests, `tests/wake.test.ts`, `tests/job.test.ts`, the README's Wakes section.
**Also add.** `cmd/wake/wake.sim.test.ts`: a monitor set before and one set after both wake; `repeat: "change"` wakes per change through a job.
**Risk.** Risk 9. A 1 s monitor interval as a job must not spawn a process a second: the runner polls in one process.

## 9. auto-parallel

**Goal.** `mods/kernel/worker/hoist.ts`: `hoist(source, isRead)` as `hoist.test.ts` states it; `isRead(tool, input)` in `mods/kernel/tools.ts` (`read`, `glob`, `grep`, `webfetch`, `websearch`, `handles` get and list, `job` list; no shell action, no `job` check, nothing that starts). Only adjacent statements group. A cell's metadata records each group formed, and `sequential`: calls that ran one after another with no data between them, which `cell-time` shows as a count.
**Owns.** new `mods/kernel/worker/hoist.ts` (+ test), `mods/kernel/worker/program.ts`, `mods/kernel/{tools.ts,operations.ts}`.
**Risk.** Risk 8. The rewrite keeps line numbers in errors, or maps them. The description states that a hoisted read may run although the one before it failed.
**Why an Opus review.** It changes the order in which a cell's effects start.

## 10. skill-texts

**Goal.** The orchestrate skill's Between messages and the protocol's Shell paragraph as in `spec.md` (Texts), including the parent's rule for which child answer is final; every line that names the `question` tool, in the skill, `end-of-step.md`, `opencode/AGENTS.md` and `README.md`; `bun run agents` after.
**Owns.** `skills/orchestrate/{SKILL.md,protocol.md,end-of-step.md}`, `opencode/{AGENTS.md,README.md}`, `opencode/mods/orchestrate/agents.json` (generated).
**Risk.** The skill's own checks (`prompt-inventory.json`, `scripts/agents.ts --check`, `tests/orchestrate.test.ts`) must follow.

## 11. worker-kernel

**Goal.** Every role agent (planner, implementor, verifier, reviewer, worker) allows `kernel`, at once. The kernel reaches exactly what the role's rules allow (no `subagent`, `wake`, `monitor`, `orchestrate`, `work`, `commitments`, `question`). One worker process per subagent session, dropped when idle (`idleMs`), after a cut and when its session ends; the host's cap and queue are unit 7's. `unit-log.ts` records a hand-back only when the child holds no open handle.
**Measurement, per role.** From `session export`, as `kernel-benefit/report.md` did: the role's next 10 steps with the kernel against its last 10 without: wall time per step, output tokens per step, model round trips per step, failed steps, and the share of its tool calls made inside cells. Each role is judged on its own numbers. A role whose round trips do not fall, or whose wall time or failures are worse, is reported to the orchestrator with the numbers; turning it off is the orchestrator's (Reserved).
**Owns.** `skills/orchestrate/agents/**`, `opencode/mods/orchestrate/{agents.json,unit-log.ts,role.ts}` (+ tests), `opencode/scripts/agents.ts`, `tests/orchestrate.test.ts`.
**Also add.** A property over `agents.json`: for every role, the tools its runtime reaches equal its allowed tools less `kernel` (counterexample 22). `cmd/orchestrate` sim: a child that ends a turn with an open handle writes no hand-back; its settle wakes the child, and its next answer is the hand-back.
**Bound.** About 133 MB per live kernel (measured by the orchestrator). Role agents hold never more than 32 live (4.2 GiB: 8.9% of the Mac's 48 GiB, 6.9% of the workbox's 62 GiB): the 33rd role agent's cell takes the slot of the idle worker used longest ago, else waits its turn. A root never waits.

## Upstream issues to open (Reserved: the orchestrator opens them)

If HL1c is red: one issue for a plugin-visible event when the owner stops a session. From `automations-vs-wakes.md`, for later: a quiet delivery for automation prompts, and a `settled` trigger.
