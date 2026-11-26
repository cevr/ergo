ready: revision 3 closes the second review's findings for units 3 to 5 by construction; unit 3's acceptance is recorded red on its own assertions (18 tests: 5 red, 13 green), and the r3 cases of units 4, 5 and 7 are written but not yet run.
Digest: a session stops executing while it waits. A cell is cut after 2 min of waiting, what it left running becomes a handle, and one short letter wakes the session when handles settle. Since r2: one thing never gets two wakes (a call that answers with work still running is handed on, with no letter), "still running" is decided by what an answer names, a cut records every call the cell made, and a root never queues for a kernel. Unit 3 can start on this revision.

# kernel-wake — design reference, revision 3 (folded into `atlas`)

*Incorporated into [`atlas`](../spec.md) behind `KernelEngine`. Engine selection (`psychoscript`, `Luau`, `ergoscript` spike; Bun later only on measured difference) runs first in `KERNEL-ENGINE-PROBE` ([../units.md](../units.md)). The OpenCode dotfiles paths below reflect the historical TypeScript design moved into `atlas`.*

Historical run `kernel-wake` · failure cap 3 · timebox 3 h per round
Mode: long run of eleven units (`units.md`). Three are deep (`handle-registry`, `cell-cut`, `land-wakes`); `auto-parallel` is a lane that also needs an Opus review.

## What changed since r2

From the second review (`spec-review-r2.md`). Units 1 and 2 are unchanged. Each row has a red case.

| # | Finding | Closed by | Red case |
| --- | --- | --- | --- |
| a1 | A detached foreground shell answers "still running" later: a false "settled" letter, then the host's notice as a second wake | A shell's limit is clamped to the cell's **remaining** waiting time, so it answers before the cut. And a detached call whose answer is unfinished settles `started`: a terminal `Handed` phase that no claim can take (H2, H8) | `start-not-wait.test.ts` (remaining clamp, 204 cases); `cut.test.ts` (`settleOf`); `reference.ts` `Handed`, `handles.sim.test.ts` |
| a2 | `image`, and any tool that starts a job, is not in the table of unfinished answers | An answer is unfinished when it **names** a job, shell or child session the system lists as still running for the session, whatever the tool. The typed table is only the fast path | `start-not-wait.test.ts` ("by what an answer names") |
| a3 | `cut.started` named one of two | It is a list; a promise not awaited is an explicit start | `cut.test.ts` |
| b | A dead owner whose sessions nobody reads again keeps handles open for ever | At each instance start the liveness check runs over `openOwners(state)`: every open call, in every session | `handles.sim.test.ts` |
| c | HL1c named the wrong events | A stop of a running execution reaches a plugin (`session.execution.interrupted`, reason `user`). The unknown is a stop of an **idle** session after a cut: that is the probe | unit 4, step 0 |
| d | Interrupt mode loses a value that exists, says "did not finish", and its permission pause has no end | A call that had ended when the cut was decided is reported `finished` with its value; an interrupted one reads "may have run"; the pause ends at `askMs` (10 min), the cell returns, and the call that asked is kept so the ask stays answerable | `cut.test.ts` (three cases) |
| e | A cut loop's remainder is indexes only, and a plain `for` has no record | `cut.maps[].remaining` holds the values of the items not finished; every cut carries `cut.calls`, the cell's journal of calls made | `cut.test.ts` |
| f | A root can queue behind 32 role agents; queue time breaks W1; the Mac has 48 GiB | A root never queues; role agents queue among themselves; time queued is the cell's waiting time | `slots.test.ts` |
| g | The fixture is the memory adapter's, not the real store's bytes | The verifier's read-only check of a **copy** of the owner's real wake store is a required step before the install. The installed build is 7a68a16a; its wake, kernel and questions code is bcf7101f's (measured: the diff is empty) | unit 3, verifier step |
| h | `quint.log` lacked the mutant runs; "breaks exactly one" | `quint-runs.sh` writes all five; the comment is corrected | `quint.log` |

## What changed since r1

From the review (`spec-review-r1.md`) and the owner's answers (`owner-ask.md`, last section).

| # | Change | Where |
| --- | --- | --- |
| 1 | **Order.** A call that answers with work still running ends the cell there, unless the cell asked for the handle (`background: true`, or `tools.start.<tool>`). W3a is withdrawn | W2b, counterexample 32, `cut.test.ts`, `start-not-wait.test.ts` |
| 2 | **One deliverer per kind.** The registry opens and tells only `call` and `land`. Alarms, monitors, jobs and latches stay entries of the wakes model, their only deliverer; `handleOfEntry` is a read-only view | H8, `DELIVERS`, counterexamples 25 to 27 |
| 3 | **Ownership.** Each handle records the plugin instance that runs it. An instance's end loses only its own open calls | H4, counterexample 24, `Retired { owner }` |
| 4 | **Settle during the cut.** A call has no handle until the cut records one; its observer is attached only after that, and a fiber that already exited still yields its exit. After the cut is decided no reply goes to the worker | H11, counterexample 23, Quint `noLoss` |
| 5 | **HL1** split into three conditions (calls scope, `declined`, stop), with the interrupt design as the built fallback, not `blocked` | "How a cell ends", HL1, `atBound` |
| 6 | **Fork 24dca3c1**: an automation's cell is never cut; an archived session's letter is parked; counterexample 22 no longer cites a removed function | W1, H1, counterexamples 22, 35 to 37 |
| 7 | **Migration** proven on `store-bcf7101f.json`, written through bcf7101f's own `WakesModel`, with a mid-fire alarm and a `newSession` alarm | M1, `migration.test.ts`, `write-store.ts` |
| 8 | **Owner: 2 minutes.** A root's `waitMs` is 120,000 | W1, Decided, Texts |
| 9 | **Owner: the async form is the only one.** `question` is removed from every session | W4, unit 5 |
| 10 | **Owner: each role.** Every role gets the kernel at once, each measured against its own earlier steps; a cap of live kernels with a queue | W8, W5e, `slots.test.ts`, unit 11 |
| 11 | **Cost.** A letter joins a running turn; an idle session's settles within 2 s are one wake; a letter is at most 2,000 characters and carries no result | H6, H9, counterexamples 31, 39 |
| 13 | **Owner, ~03:09Z: loops are not a bad thing.** Loops stay first-class; law W9 states the balance by what the loop's body does, and what survives a cut | W9, counterexamples 40 to 45, `cut.test.ts` (six loop cases) |
| 12 | Smaller: Quint's `quiet` can now fail (three mutant runs); H9 is tested; every cut retires the worker (`cut.test.ts` agrees); compute hidden under a pending call is killed (28); a process a cell started is named (29); the restart notice is written only when something was lost; `idleMs` is set by a measurement | W5, W7, units 1, 4, 7 |

## Goal

The owner, 2026-11-26: "we want things to wake us up rather than constantly be executing", "let's design the kernel so it reduces weight as much as possible", "let's make sure that the orchestrator is never always stalled or busy", "And then we can give it to our subagents", "maybe we can consolidate concepts". Answers: the wait bound is "2 minutes"; "make the async question form the only one"; which role gets the kernel: "each".

Done means:

1. No kernel cell of any session holds its turn past a stated bound: 2 min of waiting and 30 s of compute, about 151 s in all. Measured today: 190 orchestrator cells ran longer than 2 min and held 16.4 h past it; four ran over 30 min.
2. Anything long returns a handle at once, and its end wakes the session with one short letter. Code after a call that is not finished does not run by accident.
3. An idle session has no kernel process and nothing that repeats.
4. Time inside cells is shown per root, and a root past 15% of an hour is flagged.
5. Every role agent has the same kernel under the same laws.
6. The agent's vocabulary is three nouns: automation, job, handle.
7. No session has a blocking question.

**What the owner still sees as busy** (inferred from `kernel-benefit/report.md`, section 3). With a 2 min bound every wait under 2 min still holds its turn: about 10.7 h of the 27.1 h spent in cells of a minute or more, and the 1,244 cells of 10 to 60 s. The bound removes at least 14.8 h (16.4 h less at most 190 × 31 s). For the rest the remedy is W6: the per-root display shows the share, and the 15% flag names the longest cells.

Not in this run: the owner's schedules onto upstream automations (fork 24dca3c1 gives plugins `ctx.automation`, so it can now be planned as its own unit), and adopting upstream's `execute` engine (see Convergence).

## The model

### Nouns

| Noun | What it is | Who tells its end (exactly one) |
| --- | --- | --- |
| **handle** | Anything running that settles and belongs to one session. Its one verb is "wake me when it settles" | by kind, below |
| **job** | A durable command outside the server. A monitor is a job that polls | the wakes model |
| **automation** | The owner's schedules. Upstream's noun, later | upstream |
| letter | The delivery, not a noun the model picks: one short synthetic message per wake | its deliverer |

`DELIVERS`, a total table in `mods/wake/handle.ts` (and `acceptance/reference.ts`):

| Kind | Is | Deliverer | State kept by |
| --- | --- | --- | --- |
| `call` | a `tools.*` call a cut cell left in flight | **registry** | registry record; its value in its owner's memory only |
| `land` | a land accepted and owed | **registry** | registry record and the work mod's debts |
| `timer` | an alarm entry | **wakes model** (as today) | the entry, untouched |
| `job` | a job or a monitor entry | **wakes model** (as today) | the entry and the runner's record |
| `subagent` | a subagent, or a latch entry (a held start) | **host** notice; the wakes model for a latch's opening | the host; the entry |
| `shell` | a background shell | **host** end notice | the host |
| `question` | an `ask_user_async` question | **questions mod** (as today) | the questions mod |

The registry's `Open` command admits only `call` and `land` (its `Kind` type has no other member), so no code path lets it claim an entry. The `handles` tool lists all seven by reading each owner at the call; listing is not delivering.

### Decide, commit, act, observe

Every change of a registry handle goes through one decider (`Handles`, held trace for trace to `acceptance/reference.ts`):

- **Decide**: `Open`, `Settle`, `Claim`, `Delivered`, `Park`, `Unpark`, `Cancel`, `Stopped`, `Retired`, `Forget` → events or `[]`. Pure; every time is in the command; no command is refused.
- **Commit**: events are written to the wake store before anything is sent or started. A letter's delivery id is in `Claimed`, so a send that stopped short goes again under the same id and the host's inbox keeps one.
- **Act**: start the work, send the letter, cancel the work; each idempotent on its committed id.
- **Observe**: a settle is observed from the thing itself (a fiber's exit, the land's phase record), never from text. A letter's `metadata.handles` carries `{ id, kind, disposition, at }`; a land's carries `{ verdict, pushed, landed, step }`. Nothing reads a letter's words. `Tools.pushedOf(landed.text)` (`cmd/orchestrate/server.ts:1489`) goes.

### How a cell ends

Two clocks, both on the host's side of the pipe (`mods/kernel/worker/workers.ts`):

- **Waiting** runs while one or more of the cell's `tools.*` calls are in flight. Budget: `waitMs` (120,000) for the whole cell.
- **Compute** runs the rest of the time. Budget: `timeoutMs` (30,000), as today.

| End | When | What happens | Bindings |
| --- | --- | --- | --- |
| **cut (started)** | one or more awaited calls answered `Unfinished` (work still running) and the cell had not asked to start them | The cell ends at that await. `cut.started` lists every such call with its handle; the answer says the code after it did not run | kept |
| **cut (waiting)** | the waiting budget is spent and the worker answers the cut request within 1 s | `atBound: "detach"`: each call not yet replied is opened as a `call` handle. `atBound: "interrupt"`: a call that had already ended is reported in `cut.finished` with its value; one still running is interrupted and named "may have run; nothing will wake" (never "did not finish": a `tools.edit` stopped after its write has written) | kept |
| **cut (asking)** | `interrupt` mode: the waiting clock stood still for an open permission ask for `askMs` (10 min) | The cell returns. The call that sits on the ask has had no effect yet; it alone is kept running, so the ask stays answerable, and declining it is the owner's stop. Needs HL1a and HL1b only; if either is red the call is interrupted and the answer says the permission was not answered | kept |
| **cut (idle)** | the compute budget is spent and the worker answers within 1 s (it was asleep, not computing) | As a waiting cut; usually no calls | kept |
| **kill** | either budget is spent and the worker does not answer within 1 s (a loop that never yields, with or without a call pending) | The process group is killed | lost since the last snapshot |

Rules that make the cut safe:

1. **Calls scope.** Every inner call is forked from its start into a per-session scope the plugin owns, never into the worker's scope. The worker only gets replies, and only while its cell runs. (Today: `Effect.forkIn(handle.scope)`, `workers.ts:217`, which the worker's end closes.)
2. **One decision.** The host decides the cut in one step: the calls not replied at that moment are the cut's, and no reply is sent to the worker after it. A call's result is therefore read by the cell or owned by a handle, never neither (H11).
3. **Record, then observe.** For each of the cut's calls: commit `Opened { id, owner }`, then attach the observer (`Fiber.await`). A fiber that exited before the record still yields its exit.
4. **Every cut retires the worker**, after one synchronous snapshot step. A continuation left alive would be a process executing while the session waits, and Bun cannot save one. A process the cell itself started (`Bun.spawn`, `$`) ends with it; the description and the cut's answer say so.
6. **A call is told what is left.** Every call carries the cell's remaining waiting time (`how.remainingMs`). A foreground shell's limit, and a subagent's `wait`, is clamped to it, so the shell answers "still running" before the cell's bound and the cell ends `started`. No shell is ever both a detached call and a host shell.
7. **A detached call that answers unfinished is handed on.** Its settle is `started`, with the id of what it started: the handle is `Handed`, terminal, and no claim can take it. What it started is told by its own deliverer, once.
8. **Every cut carries the cell's journal**: `cut.calls`, each call the cell made, in order, as `finished`, `pending` (with its handle), `interrupted` or `started` (with its handle). The author of any loop reads which passes ran.
5. **An automation's cell is never cut.** The host's `Tool.Context.automation` (fork 24dca3c1) means the tool must finish within the call, and there is no session to wake: no waiting bound, no `Unfinished`, no clamp. The compute kill stays.

A later cell reads a handle with `await tools.handles.get(id)`, which answers at once: `{ state: "open" }` or `{ state: "settled", disposition, value }`. A `call`'s value lives in its owner's memory only (at most 256 KiB each and 16 MiB per session; past that the value is replaced by a typed `Unavailable`), is never written to the store and never put in a letter. There is no wait to call.

### HL1, and the fallback

`atBound: "detach"` rests on three conditions on the host. Unit 4 proves each first, on the simulated host and on real OpenCode, before any other code.

| | Condition | Reading of c6664062 and 24dca3c1 (inferred from code, reviewer) | If false |
| --- | --- | --- | --- |
| HL1a | A `Tool.Runtime.call` effect run in a plugin-owned scope keeps running after the kernel call that started it settled, and writes no progress to the finished call | holds: `Runtime.call` is a plain Effect (`tool.ts:326-337`) | fallback |
| HL1b | Its permission ask stays answerable (the session-level `pending` map), and a decline reaches the plugin as a typed failure | holds; a decline is a defect that `asCallFailed` catches. It is settled `declined` and told as a notice that starts no turn (H10) | fallback |
| HL1c | The owner's stop of a session is visible to a plugin, so the session's open `call` handles are cancelled and their fibers interrupted | A stop of a **running** execution is visible: c6664062 emits `session.execution.interrupted { reason: "user" \| "shutdown" \| "superseded" \| "inactivity" }` (`packages/schema/src/session-event.ts:259`), and the fork's goal plugin subscribes to it. **Unknown: a stop of an idle session**, which is the usual state after a cut: `Session.stop` calls `execution.stop`, which likely emits nothing when nothing runs. The probe: Escape and `session.stop` on an idle session that holds a detached call | fallback; the one fork hook ("a stop of an idle session reaches plugins") is proposed to the orchestrator, not built here |

**The fallback, built in the same unit and chosen by `atBound: "interrupt"`**: at the bound, calls still running are interrupted and named; nothing is detached, so no `call` handle, no `lost`, no HL1. A call that had ended when the cut was decided keeps its value (`cut.finished`); an interrupted one is named "may have run". The waiting clock stands still while one of the session's permission asks or forms is open, because the owner is answering, for at most `askMs` (10 min): then the cell returns (cut `asking`), so an ask nobody answers never holds an orchestrator for hours. Everything long is already a handle from its start (W2). It needs no host law and no fork. Unit 4 ships `detach` only if HL1a, b and c are all green on real OpenCode; otherwise `interrupt`, and the unit is still `ready`.

### What a role may do

One policy value, `{ role, waitMs }`, applied in `mods/kernel/opencode/toolbox.ts` where no cell code can step round it:

| | Root | Subagent (any role) | Automation |
| --- | --- | --- | --- |
| `waitMs` | 120,000 | 120,000 | none |
| foreground `tools.shell({ command, timeout })` | `timeout` clamped to `(0, the cell's remaining waiting time]`; if the shell is still running at its limit the answer is `Unfinished` and the cell ends | same | as written |
| any tool's answer | `Unfinished` when it names a job, shell or child session that the wakes or the host list as still running for the session (the policy's `running`, read at the answer), unless the call only read or acted on work that existed (`check`, `list`, `kill`, `handles.get`) | same | never |
| `tools.shell({ background: true })`, `tools.start.<tool>(input)` | the handle is the value; the cell goes on | same | same |
| shell, job, subagents `wait` | refused, naming `tools.handles` | offered, `ms` clamped to `waitMs` | as written |
| `question` | not a tool (W4); the error names `ask_user_async` and its assumption | same | same |
| land | answers after the cheap part with its handle (`Unfinished` unless started explicitly); the outcome wakes | refused (as today) | refused |

A subagent keeps a clamped `wait` because its turn's end is its hand-back; a root has none because its handle wakes it.

## Laws

Each law names the counterexample it forbids, and the acceptance file that holds it.

**Waiting**

- **W1 bounded time in a cell.** For every cell of a session that can be woken: `returned − started ≤ timeoutMs + waitMs + 1 s + snapshot` (about 151 s); in `interrupt` mode, plus the time a permission ask of the session was open, at most `askMs` (10 min). Time a role agent's cell waits for a kernel slot is waiting time of that cell. An automation's cell is exempt and never cut. Forbids: `await sh("bun run gate", 1_700_000)` holding a root for 28 minutes. [`cut.test.ts`, `handle.qnt` `cutHasHandle`]
- **W2 starting is not waiting, and order is kept.** (a) No input a root's cell can write reaches a tool as a wait: a foreground shell limit is at most `waitMs`, `timeout: 0` never passes, a `wait` action is refused, a land answers before its merge check. (b) A call that answers with work still running ends the cell at that await unless the cell asked for the handle (`background: true`, `tools.start.<tool>`, or a promise it does not await); then the handle is a value and the cell goes on. "Still running" is decided by what the answer names, not by the tool's name. One thing has one wake: a call that answered unfinished never gets a letter of its own. Forbids: `.wait(600_000)`; six lands in a loop for 72 min; `await tools.shell("bun install")` moved to the background and `bun test` run on the next line. [`start-not-wait.test.ts`, `cut.test.ts`, `land-wakes.test.ts`]
- **W3 independent reads start together.** Adjacent top-level awaits of read-only tools that do not read each other's results start together. Any other statement, a non-tool `await`, a tool not in the read table, every shell action (a `check` moves a cursor) and every start are barriers. A hoisted read may run although the read written before it failed, and may ask its own permission. Several long things run at once only when the cell starts them with `tools.start` or `background: true`. Forbids: five reads taking five round trips; and a read hoisted over the write before it. [`hoist.test.ts`]
- **W4 no session has a blocking question.** The questions plugin removes the host's `question` tool from the registry for every session, so no model and no cell reaches it. `/answer`'s form is the host's Form service and stays; Herdr's and Rex's "blocked on question" come from forms and stay. Forbids: the 35-minute cell. [`start-not-wait.test.ts`, `texts.test.ts`]
- **W5 light.** (a) While a session waits nothing of its kernel repeats: no interval, no poll; one one-shot timer per burst of settles (H6) is the only timer. (b) A worker is dropped after `idleMs` and after every cut; an idle session has no kernel process. `idleMs` is set by unit 7's measurement of the gaps between a cell and the next cell that reads a live value, and is at most 10 min. (c) A new kernel says so only when something was lost: the counts, at most three lost names and `kernel.bindings()`, within 160 characters. (d) A restore decodes only what a cell reads; a snapshot writes only what changed. (e) Role agents share at most 32 live workers on a host. **A root never queues**: its cell takes a free slot, else the slot of the idle worker used longest ago, else runs over the cap (the overshoot is at most the roots inside a cell at once, and is paid back when the root's cell ends). A role agent's cell takes a free or idle slot, else waits its turn behind role agents only, first come first served. Time queued counts toward the cell's waiting bound: a cell still queued when its budget is spent leaves the queue and answers "no kernel free; nothing ran", so W1 holds for it too. Forbids: the 250 ms parent poll; 337 notices of 600 names; re-encoding up to 256 MiB after every cell; 60 role agents holding 8 GiB. [`light.test.ts`, `notice.test.ts`, `slots.test.ts`]
- **W6 visible.** Status shows, per root, the share of the last hour spent inside cells with the count of cells and of cuts, time queued for a slot included; a share over 15% is flagged once an hour naming the three longest cells. Forbids: 56% of busy time inside cells that nobody saw for three weeks. [`time.test.ts`]
- **W7 compute stays bounded, apart from waiting.** A cell that does not answer a cut request within 1 s is killed, at the compute budget or at the waiting budget (compute hidden under a pending call). A cell that yields is never killed: it is cut and keeps its bindings. Forbids: `while (true) {}`; `const p = tools.slow(); while (true) {}` spinning for the length of the call; a sleeping cell losing its bindings ("I do mind compute kill"). [`cut.test.ts`]
- **W8 one model for every role.** Every role agent has the kernel, under W1 to W7; it reaches exactly the tools its direct rules allow; its handles wake it, not its parent; a parent records no hand-back while the child holds an open handle, and the protocol tells a parent that an answer with a handle open is not final. Forbids: a worker's cut answer read as its final answer; a role reaching `subagent` through the kernel. [`start-not-wait.test.ts`, `texts.test.ts`]

- **W9 loops are first-class; a bound never loses a loop's work.** The owner: "making for loops and while loops … in the kernel isn't a bad thing. I just suppose how do we balance, not blocking the thread". No loop is refused, rewritten or discouraged as a loop; the bounds apply to what its body does. [`cut.test.ts`, "W9"]

  | The loop's body | What happens | What the author sees and keeps |
  | --- | --- | --- |
  | **computes, or makes calls that finish** (a `for` over 20 lanes reading each) | Runs to its end. Compute is bounded at 30 s and the calls' waits at 2 min in sum, for the cell, not per pass | its result |
  | **fans out starts** (`for (…) hs.push(await tools.start.shell(…))`) | Runs to its end: an explicit start answers at once | its own array of handles; each wakes the session |
  | **awaits a long thing each pass** (`for (…) await tools.shell("gate")`) | Ends at the first answer that is unfinished (W2b): order is kept, nothing ran ahead | `cut.started` names the handle; bindings the earlier passes made are kept. The answer says: use `tools.map` or `tools.start` to start them all |
  | **the same as `tools.map(items, fn)`** | Every item is started (within `concurrency`); the cell ends when each is finished or a handle. Cut at the waiting bound, items in flight become handles and the rest are listed | `cut.maps`: per map `{ items, finished: [{ index, value }], pending: [index], started: [{ index, handle }], notStarted: [index], remaining: [value] }`. Every item is in exactly one index list; `remaining` holds the **values** of the pending and not-started items. The record is kept as `kernel.cut` for the next cell, so `tools.map(kernel.cut.maps[0].remaining, fn)` runs what is left and recomputes nothing |
  | **a plain `for` whose passes each finish, cut at the waiting bound** | Cut on the await in flight; that call becomes a handle (or, in interrupt mode, "may have run") | `cut.calls`, the journal: each call made, in order, with its input and `finished`, `pending` + handle, `interrupted` or `started` + handle. The loop variable is gone, the journal is not: no side effect is rerun blind |
  | **polls and sleeps** (`while (true) { check; sleep }`) | Cut only on an await, never inside a pass, at the compute budget (a sleep is not a tool wait). The worker is retired, so the loop is over | bindings its passes made; `cut.sleeps` counts the timers awaited, and the answer says: one call does this and wakes you, `tools.start.job({ command, until })` or `tools.monitor` |

  Forbids: a loop over 20 lanes refused or killed for being a loop; a cut `tools.map` that says only "cut", so the author cannot tell which of 20 gates ran; a poll loop whose collected state is lost at its cut; a serial loop of six lands holding the turn 72 min.

**Handles** (`reference.ts`, `handles.sim.test.ts`, `handle.qnt`)

- **H1 told once.** Every settle of a living session's registry handle is named in exactly one letter in that session's inbox. The letter starts a turn unless a turn runs (H9), the owner stopped the session, or the session is archived (the letter is parked and sent, under the same id, when the session is unarchived or at the next instance start; never resent in a loop). Forbids: a result lost in a restart; the same result in two wakes.
- **H2 settled only.** A letter names no handle that is still open, and none whose call answered with work still running (`Handed`). Forbids: "done" for a shell the limit moved to the background.
- **H3 first settle wins.** A second signal of the same end records nothing. Forbids: `succeeded` rewritten to `failed`.
- **H4 only its owner's end loses a call.** Each handle records its owner (`<server id>/<location>/<epoch>`, from `Config.server`). `Retired { owner }` settles that owner's open `call` handles `lost` ("it may have run; it is never run again") and touches nothing else. It is issued by the owner's own scope finalizer, or by an instance that proves the owner dead: the same server and location with a later epoch, or `Config.server.live(id)` false; checked at every instance start over the owners of every open call in every session (`openOwners`), and when a session's handles are read; never by a timer. So a dead owner whose sessions nobody reads again is retired at the next start. A land is never lost. Forbids: a reload of one location, or the second server during an install, marking another instance's live calls `lost` and dropping their real results.
- **H5 nothing follows a deleted session.** Its handles are cancelled, its shells and jobs killed, and no letter is sent. An archived session's jobs and shells run on; only its letters wait.
- **H6 one wake per burst.** An idle session's settles are claimed together when 2 s have passed since the first; two wakes of one session are never closer than 2 s. Forbids: three results seconds apart starting three turns.
- **H7 started once.** No handle's work is started twice, whatever restarted. Forbids: a second push; a tool call replayed after an install.
- **H8 one deliverer per kind.** `DELIVERS` is total, and the registry can open only its own kinds. Forbids: a registry letter and the wakes model's fire for one alarm; a host notice and a letter for one shell.
- **H9 a letter never starts a turn beside one.** A claim made while a turn runs (or a compaction, or the pool holds the session, or the owner's prompt waits) joins that turn at once, with no window. Forbids: results interleaved into the owner's typing as extra turns.
- **H10 the owner's no is final.** A detached call whose permission the owner declined settles `declined` and is told as a notice that starts no turn (beside a real result it rides in that result's letter). The owner's stop of a session cancels its open handles and interrupts their work; no letter follows. Forbids: a turn started after the owner said no; `tools.edit` running on after Escape.
- **H11 a result cannot arrive before its handle.** A call's settle is issued only by an observer attached after `Opened` is committed, and after the cut is decided no reply goes to the worker. Forbids: a call that ended during the cut, its exit dropped, its handle open until a restart marks it `lost`.

**Cost of a wake.** A letter's text is at most 2,000 characters (about 500 tokens at four characters a token, a guess): eight handles a line each, then `… and N more · tools.handles.list()`. It carries no result. Its metadata is at most 4,000 characters. The turn it starts reads the session's cached prefix. Turns started by letters are bounded by H6 and H9: at most one per 2 s burst per session, none while a turn runs.

**Migration**

- **M1 nothing pending is dropped or doubled.** (The installed build is now dotfiles 7a68a16a, two pool commits after bcf7101f; `git diff --stat bcf7101f 7a68a16a` over `opencode/{mods,cmd}/{wake,kernel,questions}` is empty, measured, so the fixture's writer is the installed wake code.) Every entry the wake store holds at the install (alarm, repeating alarm, `newSession` alarm, monitor, job, latch) is read unchanged by the new build, keeps its id, note and time, and fires once at its time; an entry whose fire was recorded and not sent is sent under the id it was recorded with. Repeats and `newSession` stay the wakes model's and are outside H1 and H3. [`migration.test.ts` on `store-bcf7101f.json`]

## Counterexamples

| # | Case | Law | What happens |
| --- | --- | --- | --- |
| 1 | A handle settles while the kernel worker is restarting | H1, W5b | The registry lives in the server, not the worker |
| 2 | A handle settles while the server restarts or an install swaps the tree | H1, H4, H7 | A land, a job, a shell and a subagent run on and are reconciled; that server's open calls are settled `lost` and told; the store is outside the tree |
| 3 | Two results arrive in one wake | H6 | One letter names both |
| 4 | A cell awaits a handle an earlier cell made | W2 | `tools.handles.get(id)` answers at once |
| 5 | A wake arrives during a compaction | H1, H9 | The letter is committed under its id and joins the turn after it |
| 6 | The pool holds the session when the wake fires | H9, H1 | In the inbox once; not fired again |
| 7 | A detached call needs a permission prompt | W1, HL1b | The cell is cut; the ask stays open; the answer lets it run and the letter follows. In `interrupt` mode the waiting clock stands still while the ask is open |
| 8 | A detached shell outlives the session | H5 | Deleting the session kills its shells and jobs |
| 9 | A monitor loop written by hand inside a cell | W1, W7 | Cut at the compute budget (it yields), the worker retired, so the loop is gone |
| 10 | An infinite compute loop "waiting" in disguise | W7 | Killed at 30 s |
| 11 | The owner types while results arrive | H9 | The letter joins the owner's turn |
| 12 | A subagent's kernel is cut and its turn ends | W8 | Its handle wakes the subagent; no hand-back is recorded while it holds one |
| 13 | A cell passes `timeout: 0` | W2a | Clamped |
| 14 | A land's gate is red | W2 | The call succeeded; the wake carries `verdict: "stopped"`, `step: "gate"` |
| 15 | A restart between a land's merge and its push | H7 | The owed land starts again from origin's head; one push, one letter |
| 16 | `await tools.write(f)` then `await tools.read(f)` | W3 | The write is a barrier |
| 17 | A cut cell's `Promise.all` had one call done and two pending | W1 | Two handles |
| 18 | The same end is signalled twice | H3 | One settle |
| 19 | A cut would lose a live binding | W5c | The answer names it, at most eight names then a count |
| 20 | An alarm set before the install is due after it | M1 | It fires at its time, under its id |
| 21 | A root lists handles after a compaction | H8 | One list, read from each owner at the call |
| 22 | A worker reaches `subagent` or `wake` through its kernel | W8 | A wholly denied tool is not listed to a runtime. Held by a property over `agents.json` in unit 11, not by citing a host function (`whollyDisabled` on c6664062, `PermissionPolicy.isActionDenied` on 24dca3c1) |
| 23 | A call ends while its cell is being cut | H11 | Read by the cell or handed back with its value, never neither. `cut.test.ts` either side of the bound; Quint `noLoss`, with the witness `raceNeverSeen` |
| 24 | A reload of one location, or two servers overlapping during an install | H4 | Only the ended instance's calls are `lost`. Sim with three owners; Quint `ownLoss` |
| 25 | The registry claims a timer the wakes model also fires | H8 | Cannot be written: the registry's `Kind` is `call \| land` |
| 26 | A repeating alarm; a `newSession` alarm | M1, H8 | The wakes model's; outside the reference |
| 27 | An entry mid-fire at the install | M1 | Viewed as `claimed` under its recorded delivery id; sent once |
| 28 | `const p = tools.slow(); while (true) {}` | W7 | Killed at the waiting bound plus 1 s |
| 29 | A process the cell started with `Bun.spawn` | How a cell ends, rule 4 | Ends with the worker; the description and the cut's answer say so and name `tools.shell({ background: true })` |
| 30 | A 33rd live kernel with none idle | W5e | Queued, first come first served; granted when a cell ends |
| 31 | A handle's value is a file with a secret, or 50 MB | Cost | Never in the store or a letter; in memory, capped |
| 32 | `await tools.shell("bun install")` backgrounded at its limit, then `bun test` | W2b | The cell ends at the await; the next line does not run. Same for a lane made and a worker started in it, and a land followed by reading what it pushed |
| 33 | The owner declines a detached call's permission | H10 | `declined`, a notice, no turn |
| 34 | The owner stops the session with calls detached | H10, HL1c | Cancelled and interrupted; no letter |
| 35 | A cell run by an automation waits 10 min | W1 | Never cut; it has no session to wake |
| 36 | A letter for an archived session | H1 | Refused by the host (`ArchivedError`), parked, not resent in a loop, sent under its id on unarchive |
| 37 | A letter for a session the owner stopped | H1 | Admitted; it does not wake; read at the owner's next prompt |
| 38 | The host reports a child's cut answer to its parent | W8 | The protocol: an answer without the hand-back line, or with a handle open, is not final |
| 39 | Three settles seconds apart, idle | H6 | One wake, 2 s after the first |
| 40 | A `for` over 20 lanes that reads and computes | W9 | Runs to its end, uncut |
| 41 | A `for` that starts 20 gates with `tools.start` | W9 | 20 handles in the author's array, uncut |
| 42 | A `for` that awaits a gate each pass | W9, W2b | Ends at the first gate with its handle; the passes' bindings kept; the answer names `tools.map` |
| 43 | `tools.map` over 5 lanes, three of them long | W9 | All five started; `cut.maps` lists two finished and three handles |
| 44 | `tools.map` of six slow calls, two at a time, cut at the bound | W9, W1 | Two finished with values, two in flight as handles, two not started; none lost, none twice |
| 45 | A `while` that polls and sleeps | W9, W7 | Cut on an await; its bindings kept; `cut.sleeps` and the job form named |
| 46 | A shell started at 100 s of the cell's 120 s, with a 120 s limit | W2b, rule 6 | Its limit is 20 s: it answers "still running" at the bound, the cell ends `started`, and the host's notice is its one wake |
| 47 | A detached call answers unfinished after the cut | H2, H8, rule 7 | `Handed`: no letter. What it started tells its own end |
| 48 | `await tools.image(…)` then a read of the file | W2b | The answer names a running job: the cell ends there |
| 49 | `await Promise.all([gate(a), gate(b)])`, both unfinished | W2b | `cut.started` names both |
| 50 | A server dies holding calls of a session nobody opens again | H4 | Retired at the next instance start, from `openOwners` |
| 51 | Interrupt mode: a call ends in the instant the cut is decided | How a cell ends | In `cut.finished` with its value; nothing that completed is reported interrupted |
| 52 | Interrupt mode: `tools.edit` stopped after its write | How a cell ends | "may have run" |
| 53 | A permission ask nobody answers, overnight | W1 | The cell returns at 10 min; the ask stays open and answerable |
| 54 | `tools.map` over items built in the call, cut | W9 | `remaining` holds their values |
| 55 | A `for` of five pushes, cut in the third | W9, rule 8 | `cut.calls`: two finished, the third pending with its handle |
| 56 | A root's cell with 32 role agents inside cells and more waiting | W5e | Granted at once, over the cap, ahead of the queue |
| 57 | A role agent queued past its waiting budget | W1, W5e | Leaves the queue; "no kernel free; nothing ran" |

## Convergence with upstream

Upstream's direction: a fresh program per call (#66007), runs saved and resumed, a job that moves to the background after 2 min and wakes the session (`full_codemode`). The owner's 2 min is now the same number.

- **Converge on the shape, keep the engine.** A worker is disposable: retired at every cut, dropped when idle. What a new one starts from is a lazily read store of named values; live bindings are a cache over it.
- **Do not move to `execute` now.** 17% of orchestrator cells and 27% of root cells call `subagent`, `glob`, `grep`, `webfetch` or `skill`, which no `execute` script reaches, and #66278's `agent()` waits for its result.
- **One real difference.** Upstream's background script continues its code after the call returns; our cut ends the cell. W2b keeps our order correct, and the cut's answer says what did not run. Under psychoscript a cut cell could be suspended to bytes and resumed at the settle: the one behaviour to revisit with the engine.
- **What keeps a later adoption small:** the registry knows no engine (`Open`, the settle, `Cancel` are `Host.durableFn({ start, wait, cancel })`); a cell reaches tools only through `CellTools.call`; handles cross cells as ids; the binding store is keyed by name.
- **Watch:** `full_codemode` on by default in a release; #66007 reversed or a host key-value scratch; #66278 gaining a non-blocking start. When all three hold, plan the engine swap as one unit behind `Workers`.

## Vocabulary: what folds into what

| Today | Becomes | Migration |
| --- | --- | --- |
| `wake` (alarm) | a handle of kind `timer`, listed; the wakes model still owns and fires it | Entries untouched (M1) |
| `monitor` | a job that runs a poll | New monitors start as jobs. One pending at the install keeps its in-server poller until it ends; the poller's code goes at `ALIAS_REMOVAL` |
| `job` | job; its end is a handle of kind `job` | Unchanged |
| latch | a handle of kind `subagent`, state `held` | Entries untouched |
| `wake_list`, `wake_cancel` | `handles({ action: "list" \| "get" \| "cancel" })`; in a cell `tools.handles.*` | Aliases until `ALIAS_REMOVAL` (2027-01-15) |
| subagent hand-back | the settle of a `subagent` handle, told by the host | Unchanged |
| `message` with wake | a letter to another session: a delivery, not a handle | Unchanged |
| `question` | removed; `ask_user_async` is the question handle | The questions plugin's registry transform |

## Workspace

Historical dotfiles plugin layout (`opencode/mods/{kernel,wake,questions}` at baseline `bcf7101f` / fork `24dca3c1`). In `atlas`, these semantics live behind `KernelEngine` and the integrated `wake` / `handles` service (`../spec.md`).

## Owns

Per unit in `units.md`. Across the run: `opencode/mods/kernel/**`, `opencode/cmd/kernel/**`, `opencode/mods/wake/**`, `opencode/cmd/wake/**`, `opencode/mods/questions/{ask.ts,ask.test.ts}`, `opencode/cmd/questions/{server.ts,server.test.ts}`, `opencode/mods/work/{land.ts,model.ts,service.ts,owed.ts,memory/**,filestore/debts.ts}`, `opencode/cmd/work/**`, `opencode/mods/orchestrate/{tools.ts,digest.ts,unit-log.ts,role.ts,agents.json}` and the land path of `opencode/cmd/orchestrate/server.ts`, `opencode/mods/status/**`, `opencode/sim/{host-laws.ts,host.ts,world.ts}` for the new host laws, `opencode/tests/{kernel.test.ts,kernel/**,host-laws/**,wake.test.ts,job.test.ts,work.test.ts,orchestrate.test.ts}`, `opencode/scripts/agents.ts`, `opencode/README.md` and `opencode/AGENTS.md` (the kernel, wakes, work and questions lines), `skills/orchestrate/{SKILL.md,protocol.md,end-of-step.md,agents/**}`.

## Do not touch

- The fork (`~/Developer/personal/opencode`, every `.work/opencode/*`): its advance to 24dca3c1 is pending and owned by the orchestrator. No unit changes it. If HL1 is red, unit 4 ships `atBound: "interrupt"` and reports; it does not patch the fork.
- `opencode/mods/pool/**`, `opencode/mods/update/**`, `opencode/mods/prime/**`: other runs own them. (`mods/prime/group.ts` names `question` in a tool grouping: a follow-up.)
- `~/.config/opencode/opencode.json`, any `opencode.db`, the pool's auth, the keychain.
- The acceptance files and `store-bcf7101f.json`: an implementor copies a test into its lane and may not weaken an assertion; a needed change is a question to the orchestrator. `write-store.ts` refuses to run on any head but bcf7101f.
- The wake store's stored shapes (`Entry` in `mods/wake/wake.ts`): fields may be added as optional; none renamed or removed.

## Acceptance check

Historical acceptance layout run at baseline `bcf7101f`:

```
cd <unit dir>/acceptance
OPENCODE_DEV=<fork> KERNEL_WAKE_ROOT=<lane>/opencode bun test ./<file>
quint run handle.qnt --invariant=safe --max-samples=20000 --max-steps=40
```

Recorded:

- **Unit 3, revision 3 (measured, 2026-11-26):** `unit3.log`, `handles.sim.test.ts` and `migration.test.ts`: 18 tests, 5 fail and 13 pass, exit 1, 0.5 s. The five failures are the file's own `expect`s (`Handles`, `openOwners`, `letterOf`, `handleOfEntry`, `DELIVERS`). The 13 that pass are the reference model, with the new `started` and `openOwners` cases, and the migration's fire guards.
- **The whole set, revision 2 (measured, 2026-11-25):** 90 tests, 68 fail, 22 pass, no import error or timeout.
- **Not yet run:** the revision 3 cases added to `cut.test.ts`, `start-not-wait.test.ts`, `slots.test.ts` and `texts.test.ts`, and `quint-runs.sh` (`quint.log` still holds only r2's `safe` run; the three mutant runs and the witness were measured by hand in r2 and by the r2 reviewer). The first attempt queued an hour in the host's check queue and was refused at its deadline. Run each with a declared width: `bun test ./<file>`. The implementor of unit 4, 5 or 7 records its file red at its lane's base before its first change; a case that is not red for its own assertion there is a question to the orchestrator.

| File | Unit | Kind | Red now for |
| --- | --- | --- | --- |
| `notice.test.ts` | kernel-notice | property over counts 1 to 5,000 | a notice at every start, listing every name |
| `time.test.ts` | cell-time | property over 200 generated hours | `mods/kernel/cell-time.ts` absent |
| `reference.ts`, `handles.sim.test.ts`, `handle.qnt` | handle-registry | reference model; 300 seeded interleavings with three owners, lost sends, archived, stopped and deleted sessions; H6, H9, H10, C24, C25, C31 cases; Quint with three mutants | `mods/wake/handle.ts` absent (2 red; 8 reference tests green) |
| `migration.test.ts`, `store-bcf7101f.json`, `write-store.ts` | handle-registry | the installed build's own store, replayed over two restarts and an hour | `handleOfEntry`, `DELIVERS` (2 red; 3 fire guards green) |
| `cut.test.ts` | cell-cut | the real worker process; both `atBound` modes | `waitMs` ignored; `Unfinished` and `tools.start` absent; a sleeping cell killed; hidden compute not killed; no record of a cut map or loop (14 red; 5 guards) |
| `start-not-wait.test.ts` | start-not-wait, worker-kernel | property over 207 timeouts; refusals and marks by role | no policy; no `Unfinished`; `question` still a tool |
| `land-wakes.test.ts` | land-wakes | the work mod's memory adapters, with a restart | the call answers after the gate |
| `light.test.ts`, `slots.test.ts` | kernel-light | the real worker; a decider property over 300 runs | eager restore; a 250 ms poll; 30 min idle; no cap |
| `hoist.test.ts` | auto-parallel | 300 generated cells with barriers, against the unhoisted run | `hoist.ts`, `isRead` absent |
| `nouns.test.ts` | one-noun | the tool names and words | no `handles` tool |
| `texts.test.ts` | start-not-wait, skill-texts, worker-kernel | the words | the description, the skill, `agents.json` |

The green guards must stay green: the reference model's tests, the fire tests of the migration, a short cell is not cut, an automation's cell is not cut, a non-yielding loop is killed, the permission pause, a background shell keeps its timeout, handle reads pass, explicit starts and finished work are not marked, no role is allowed `question`.

Each unit also adds, in its lane, the simulated cases `units.md` names and, where it changes what a user meets, drives its `verify-opencode` feature.

## Gate

Round gate, each leg under a minute, into `<unit dir>/<unit>/gate-N.log`:

```
bun test <the changed packages' test files>
bun run typecheck
bunx oxlint <changed files>
KERNEL_WAKE_ROOT=$PWD bun test <acceptance files>
bun scripts/receipt.ts -- '<the commands above>'
```

Lands: `push`, each unit alone on its receipt through the land's merge check, in the order of `units.md`; the deep units are verified before their merge.
CI: none for the dotfiles' `opencode/`; the land's accept run (`bun run accept:land <base>`) and the post-land deep run stand for it.

## Risks

What a verifier should attack hardest:

1. **HL1a, b, c on real OpenCode**, at c6664062 and again when the fork advances to 24dca3c1. A detached `tools.edit` after Escape is the worst case: if HL1c cannot be shown, `interrupt` ships.
2. **Told once.** Kill the server between `Claimed` and the send, between the send and `Delivered`, and during a compaction; count letters by delivery id.
3. **Started once.** A `call` is never replayed; a land pushes once across a restart at each phase.
4. **The cut's one decision** (H11). Drive calls that end within a few milliseconds of the bound; a reply sent after the decision is the bug.
5. **Ownership.** Start a second server on the same store while the first has calls detached; reload one location. `Retired` for a live owner is the bug; so is a dead owner never retired (its handles open for ever), which the read-time liveness check must catch.
6. **The 1 s grace.** A worker in a long garbage collection could be killed for compute it did not use. Unit 4 measures the worst answer time under load before fixing the number.
7. **Order.** Every answer that can carry unfinished work must be marked: a tool the table misses lets the next line run. The table is typed over the built-in tools; a plugin's tool that returns early is out of reach and is named in the description.
8. **Hoisting.** Every way an argument can observe order; the rule is an allow-list of plain literals and identifiers.
9. **Stored shapes and the mid-fire entry.** `store-bcf7101f.json` is the memory adapter's map, not the bytes the real storage adapter writes. So a step of unit 3's verification is **required before the install**: the verifier's batch copies the owner's real wake store (`/bin/cp` into its scratch; never the live file, never an `opencode.db`), and the lane's code must decode every entry of the copy unchanged, map each through `handleOfEntry`, and list the same ids, notes and times as the installed build's `wake_list` reads from the same copy.
10. **Lazy restore.** An accessor must not read as a changed binding and must survive `Object.keys(globalThis)`.
11. **The slot queue.** A queued cell whose session ends must leave the queue; a worker is never dropped inside a cell.
12. **Cache.** The kernel's description is frozen per session; new words reach only new sessions. A running root learns the bound from the cut's own answer. Do not unfreeze.

## Decided

- **A cut ends the cell; it does not resume**, and every cut retires the worker. By W5 and `principle-redesign-from-first-principles`.
- **`detach` is primary and `interrupt` is built beside it**, chosen by one option. `detach` keeps a slow call's result (the orchestrator's W1: "every pending call becomes a durable handle"); `interrupt` is what ships if any of HL1a, b, c is not shown. Both are small, and the switch is then reversible at the next fork advance.
- **The registry delivers only `call` and `land`.** The wakes model already delivers entries correctly, with a recorded fire and a backstop; a second deliverer would be a second truth (`principle-subtract-before-you-add`).
- **A `call`'s value is never written to disk.** It is volatile work; its value can hold a secret. After its owner's end, `get` answers the disposition and `Unavailable`.
- **`question` is removed for every session** (owner). `ask_user_async` carries the assumption (`principle-never-block-on-the-human`).
- **A root's `wait` is removed; a subagent's is clamped.**
- **Plugin only.** The one possible fork point is a stop event for HL1c, proposed, not built.
- **Every role gets the kernel at once** (owner: "each"), each measured against its own last ten steps; a role whose numbers are worse is reported to the orchestrator, who decides.
- **Bounds**: `waitMs` 120 s for a root and a subagent (owner), compute 30 s, cut grace 1 s, the wake window 2 s, a letter 2,000 characters, `idleMs` by measurement and at most 10 min, unwell share 15%, 32 live kernels per host.
- **Memory**: about 133 MB per live kernel (the orchestrator's measurement). The cap of 32 is 4.2 GiB: 8.9% of the Mac's 48 GiB (`hw.memsize`, the reviewer's measurement) and 6.9% of the workbox's 62 GiB. Roots run over it by at most their own number. An idle or ended session's kernel is dropped; the 33rd role agent's cell queues, a root's never (W5e).
- **Queue time counts toward the cell's bound** rather than the call answering "queued" at once: an immediate answer would make every role agent write a retry loop, which is a poll.

## Texts

| Where | From | To |
| --- | --- | --- |
| `mods/kernel/description.ts`, Limits | "30 s of compute per call, paused while tool calls run … Kernel calls in one session run one at a time, so put independent work in one cell" | "Limits: 30 s of compute and 2 min of waiting per call. Past 2 min of waiting the cell is cut: each call still running becomes a handle (or, where the host cannot keep it, is stopped and named "may have run"), the code after it did not run, and its result wakes this session. The cut lists every call the cell made and how each stands. A cell that does not yield is killed. A process the cell itself starts (`Bun.spawn`, `$`) ends with the cell: use `tools.shell({ background: true })`." |
| `description.ts`, new paragraph | none | "Long work: start it and end your turn. A call that answers with work still running (a shell past its limit, a job, a land, a background subagent) ends the cell there, so the next line never runs on unfinished work. To start several and go on, write `tools.start.shell(…)`, `tools.start.job(…)`: each returns its handle at once. A call not awaited is a start too. Its end wakes this session with one short letter. Read a result later with `await tools.handles.get(id)`, which answers at once. There is no wait to call. Ask the owner with `tools.ask_user_async` and its assumption." |
| `description.ts`, new paragraph "Loops" | none | "Loops are what the kernel is for. A loop that computes or reads runs to its end. To run long things over many items write `await tools.map(items, (item) => tools.shell(…))`: every item is started, and the cell ends with a record of which finished and which are handles (`kernel.cut.maps` in your next cell). To watch for something, one call does it and wakes you: `tools.start.job({ command, until })`. A loop that sleeps is cut at 30 s and keeps what it stored." |
| `description.ts`, the parallel example | a `Promise.all` of two shells | keeps `tools.map(`; adds "Reads written one after another start together when none reads another's result." |
| `mods/kernel/worker/shell-handle.ts` | `.wait(ms)`, `DEFAULT_WAIT_MS = 10 * 60 * 1_000` | no `wait` on a root's handle; a subagent's is clamped to 2 min |
| `mods/kernel/catalog.fixture.ts` | lists `question` | does not |
| `mods/wake/tools.ts` | `JOB_DESCRIPTION` "A subagent … uses action wait"; `MONITOR_DESCRIPTION`; `LIST_DESCRIPTION` | the job's end wakes; a subagent's wait is at most 2 min; a monitor is a job that polls; the list is the handle list |
| `skills/orchestrate/SKILL.md`, Between messages | "A command that can run longer than about 10 seconds … goes to a `job`, a background shell or a subagent" | "**Start, then end the turn.** Anything long returns a handle; its end wakes you. A cell is cut after 2 min of waiting, and a call that answers unfinished ends the cell, so nothing you write holds the turn or runs ahead of its work. Start several with `tools.start`. Read a settled handle with `tools.handles.get(id)`." |
| `skills/orchestrate/protocol.md`, Shell | "Run a long command once in the background …" | adds the role's kernel: the same model, `tools.handles`, a `wait` of at most 2 min; and for a parent: "a child's answer is its hand-back only with its first line and no handle open" |
| `skills/orchestrate/{SKILL.md,protocol.md,end-of-step.md}`, `opencode/README.md`, `opencode/AGENTS.md` | any line that sends an agent to the `question` tool | `ask_user_async` |
| `skills/orchestrate/agents/*` then `bun run agents` | no kernel | `kernel` allowed for every role |

## Work rules

- Each request to an implementor carries: the unit's section of `units.md`, its acceptance files, this spec's laws by number, and `KERNEL_WAKE_ROOT`.
- A law is changed only by the orchestrator. An implementor that finds a law unimplementable reports the counterexample; it does not narrow the test.
- No synthetic load, no full gate, no build. One heavy check at a time.

## Reserved

Merge, push and each land; any change to the fork; an install or a server restart; turning the kernel off for a role after its measurement; removing an alias before `ALIAS_REMOVAL`; opening the upstream issues `units.md` lists.

## Follow-ups

- finding outside the unit: `cmd/orchestrate/server.ts:1489` reads what a land pushed from its answer's text (`Tools.pushedOf`); `land-wakes` replaces it.
- finding outside the unit: the workbox roots (Ergo) were never measured; `cell-time` gives the first reading there with no export.
- finding outside the unit: the owner's schedules onto upstream automations, now that 24dca3c1 gives plugins `ctx.automation`.
- finding outside the unit: `opencode/mods/prime/group.ts` names the `question` tool in a grouping; the prime mod's owner drops it once `question` is gone.
