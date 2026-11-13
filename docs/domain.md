# Domain modeling in Ergo

This page maps the building blocks of domain-driven design, as `@alchemy.run/domain` names them, to Ergo's constructs. It states each as a rule and shows it in the worked example [`examples/pipeline`](../examples/pipeline): a build pipeline whose runs are aggregates, whose board is a view, whose retries are a saga and whose Api is one operation type. Every code block below is a quote of that program, not a copy: a marker above the block names the file and the declarations, and `TestDomainDocQuotesResolve` (`scripts/examples`) fails when the program and the page differ. The terms are defined in [GLOSSARY.md](../GLOSSARY.md#domain-modeling).

What Ergo adds to the source library's model: `Decide` has no `needs` row, so it cannot read a clock, a store or the network, and a replay gives the same events; a rejection is in the error row, so a caller that forgets one does not compile; `Evolve` is total by the exhaustive `match`; and the laws stand in the source beside the code and run against every provider.

## 1. The mapping

| Building block | Ergo form | State |
| --- | --- | --- |
| Command, event | A closed union per aggregate: `type Command = Submit{…} \| Start{} \| …`, `type Event = …`. The codec is derived | exists |
| Rejection, with evidence | A record per reason, in `Decide`'s `fails` row: `fails WrongState \| WrongStage \| Closed \| BadLength`. The evidence is the record's fields. The caller's `catch` is exhaustive | exists |
| Aggregate state | A union of modes (`Unsubmitted{} \| Queued{…} \| Running{…} \| …`), never a record with a status field | exists |
| Decide | A pure function `Decide(s S, c C) []E fails R`. It has no `needs` row; the checker proves that | exists |
| Evolve | A total function `Evolve(s S, e E) S` with no rows and an exhaustive `match (s, e)` | exists |
| The aggregate | A machine whose step is `Decide`, then the fold of `Evolve`, then `Emit{Next, Output: events}` | exists |
| Durability | `journal.Start(m, j, every, capacity, sink)` keeps the committed signals and snapshots, and replays them. An event log of commands, with positions and receipts, is module `domain` (`modules/domain`, DK2): `Log[E]`, and `Sourced`, whose save appends the step's events and whose recover folds the stream. A stream may hold many entities (a desk of many runs), and the actor's signal may be a union of commands and its own work's and timers' signals: `Keep` commits without leaving the entry, so the state's work and timer go on, and `StepID` names a record no command caused (DK5) | exists |
| Laws | Inside the machine (`law ClosedIsFinal`), at top level, and on the Api's operation type. Each is seeded, shrunk and replayable | exists |
| View (read model) | A fold over events, with a law that the view equals the fold of the log | exists |
| Policy (an event causes a command or a notice) | A pure function `React(e E) []C`, run by a follower of the log: `domain.Follow(log, cursor, follower, stream, handle)` delivers each record after the follower's saved cursor, in order, at least once, and a command the handle sends takes the id `domain.SentID(delivery, index)` (follower, stream, position, index), so a second delivery is a resend the log keeps once (law `RedeliveryAddsNothing`) | exists ([modules/domain](../modules/domain/README.md)); `examples/pipeline` runs `React` and its board as followers of its desk's log (`cmd/pipeline` `told`, `pipeline/projection.ergo`) |
| Saga (a process over time) | A machine of its own, with a timer (`after`) and `work` | exists |
| External system | An operation type with laws, bound in `cmd/<name>/main.ergo`; a `testing.Script` of it in a scene | exists |
| Context (actor, tenant, time, command id) | Data in the command's envelope, never an ambient value (rule 3) | pattern |
| Api | One operation type: commands and reads as operations, laws beside them | exists |
| Projection of the Api | `ergo gen rpc` (HTTP, Effect RPC's protocol) and `ergo gen cli` (a std `flag` tree with the law `ArgsRoundTrip`); MCP is planned (AP3); a UI is a pattern (rule 6) | rpc and cli exist |

## 2. The aggregate

**Rule 1. State is a union of modes; commands, events and rejections are unions and records.** A mode carries only what holds in it, so a field that means nothing in a state cannot be read there.

<!-- quote examples/pipeline/run/run.ergo Run Command Event -->
```go
// Run is where one run is:
//
//	Unsubmitted  no command reached it yet
//	Queued       it waits for the runner; Stages is its length
//	Running      the runner works on stage Next (0 is the first)
//	Failing      stage Stage failed; a retry or a give-up comes next
//	Done         it ended, with an Outcome
type Run = Unsubmitted{} | Queued{Stages int} | Running{Stages int, Next int} | Failing{Stages int, Stage int} | Done{Outcome Outcome}

// Command is what can be asked of a run. Submit, Cancel and Retry come
// from an operator; Start, Finish and GiveUp from the pipeline that runs
// it (a retry after a backoff is a Retry too).
type Command = Submit{Stages int} | Start{} | Finish{Stage int, OK bool} | Retry{Stage int} | GiveUp{Stage int} | Cancel{Why string}

// Event is what happened to a run. The run is the fold of its events.
type Event = Submitted{Stages int} | Started{} | StagePassed{Stage int} | StageFailed{Stage int} | Retried{Stage int} | Finished{} | Abandoned{Stage int} | Stopped{Why string}
```

**Rule 2. `Decide` has no `needs` row; `Evolve` is total; the step is `Decide` then the fold.** A rejection is a record in `Decide`'s row, with its evidence as fields. A durable actor (`actor.StartDurable`, which `journal.Start` and `examples/pipeline`'s event log both run on) refuses a step that is not pure (S108), so a durable actor of the machine replays to the same state.

<!-- quote examples/pipeline/run/run.ergo Decide Evolve Handle -->
```go
// Decide is the events that the command c gives in the state s, or the
// rejection. It is pure: it reads no clock, no store and no network.
func Decide(s Run, c Command) []Event fails WrongState | WrongStage | Closed | BadLength {
	match (s, c) {
	case (Done, _):
		Closed{Status: StatusOf(s)}
	case (Unsubmitted, Submit as m):
		if m.Stages < 1 || m.Stages > MaxStages {
			return BadLength{Stages: m.Stages}
		}
		[]Event{Submitted{Stages: m.Stages}}
	case (Queued, Start):
		[]Event{Started}
	case (Running as r, Finish as f):
		if f.Stage != r.Next {
			return WrongStage{Expected: r.Next, Got: f.Stage}
		}
		if !f.OK {
			return []Event{StageFailed{Stage: f.Stage}}
		}
		if r.Next+1 == r.Stages {
			return []Event{StagePassed{Stage: f.Stage}, Finished}
		}
		[]Event{StagePassed{Stage: f.Stage}}
	case (Failing as f, Retry as k):
		if k.Stage != f.Stage {
			return WrongStage{Expected: f.Stage, Got: k.Stage}
		}
		[]Event{Retried{Stage: k.Stage}}
	case (Failing as f, GiveUp as g):
		if g.Stage != f.Stage {
			return WrongStage{Expected: f.Stage, Got: g.Stage}
		}
		[]Event{Abandoned{Stage: g.Stage}}
	case (Queued, Cancel as k):
		[]Event{Stopped{Why: k.Why}}
	case (Running, Cancel as k):
		[]Event{Stopped{Why: k.Why}}
	case (Failing, Cancel as k):
		[]Event{Stopped{Why: k.Why}}
	case (_, _):
		WrongState{Status: StatusOf(s), Command: NameOf(c)}
	}
}

// Evolve is the state after the event e. It is total: an event that the
// state cannot have keeps the state (Decide never gives one).
func Evolve(s Run, e Event) Run {
	match (s, e) {
	case (Unsubmitted, Submitted as m):
		Queued{Stages: m.Stages}
	case (Queued as q, Started):
		Running{Stages: q.Stages, Next: 0}
	case (Running as r, StagePassed):
		Running{Stages: r.Stages, Next: r.Next + 1}
	case (Running as r, StageFailed as f):
		Failing{Stages: r.Stages, Stage: f.Stage}
	case (Failing as f, Retried):
		Running{Stages: f.Stages, Next: f.Stage}
	case (Running, Finished):
		Done{Outcome: Passed}
	case (Failing, Abandoned as a):
		Done{Outcome: GaveUp{Stage: a.Stage}}
	case (_, Stopped as k):
		Done{Outcome: Cancelled{Why: k.Why}}
	case (_, _):
		s
	}
}

// Handle is the aggregate's step: Decide, then the fold of its events,
// which it gives as the output.
func Handle(s Run, c Command) Decision[Run, []Event] fails WrongState | WrongStage | Closed | BadLength {
	let es = try Decide(s, c)
	Emit{Next: Fold(s, es), Output: es}
}
```

The aggregate is a machine, and its rules are laws of the machine:

<!-- quote examples/pipeline/run/laws.ergo Life -->
```go
// Life is the run as a machine: its step is Handle, so a durable actor
// of it keeps the commands and replays them (std journal).
type Life machine[Run, Command] {
	initial: Unsubmitted,
	step:    Handle,

	// ClosedIsFinal: a run that ended refuses every command with Closed,
	// whatever its outcome.
	law ClosedIsFinal(o Outcome, c Command) {
		let _ = Handle(Done{Outcome: o}, c) catch {
		case Closed:
			return true
		case WrongState, WrongStage, BadLength:
			return false
		}
		false
	},
}
```

There is no `aggregate` keyword and no std `domain` package: the aggregate is a pattern over `machine`, which needs a few lines of glue per program and no construct a library cannot say.

## 3. Contexts are data

**Rule 3. The command's context (its id, and the actor, tenant and time when the domain needs them) travels in an envelope record, and `Decide` takes what it needs as arguments.** An ambient context would put a `needs` row on `Decide`, and S108 would refuse the durable actor. The provider of the Api stamps the envelope (a time from `Clock`, an id from the caller) before it sends it. Module `domain` (`modules/domain`, DK2) holds the shared `Envelope[C] { ID, By, At, Command }`, `Receipt` and the log port `Log[E]`, whose laws hold the rule below for every provider (`ResendGivesFirstReceipt`, `SameIdOtherPayloadIsRefused`); the worked example declares its own envelope until it moves onto the log:

<!-- quote examples/pipeline/pipeline/desk.ergo Envelope -->
```go
// Envelope is a command for the run Run, named ID. The command's context
// travels in it as data, so Decide needs nothing.
type Envelope { ID string, Run string, Command run.Command }
```

A command's id makes a resend harmless: the context keeps the receipt of each id it took and answers a resend with the first receipt (the law `DuplicateIsNoop` below).

## 4. One actor per context, for now

**Rule 4. One actor runs a bounded context; its state holds every aggregate by id, and its step calls that aggregate's `Decide` and `Evolve`.** An actor per aggregate id needs a keyed registry of actors that outlives its parent's transitions, which has no direct form yet. `Decide` stays per aggregate, so moving to an actor per id changes the host and not the domain. The cost is one mailbox for every run of the context.

## 5. The Api, declared once

**Rule 5. The Api is one operation type: commands and reads are its operations, and its laws stand beside them.** Every surface is a provider of it or a caller of it: the in-process model, the generated RPC client (`ergo gen rpc`), the generated command line (`ergo gen cli`), and later a sync replica. `ergo test` runs the Api's laws against each provider that has a constructor, so a projection is admitted by the laws.

<!-- quote examples/pipeline/pipeline/api.ergo Pipeline -->
```go
// Pipeline is the Api of a build pipeline.
type Pipeline {
	Submit(c SubmitRun) Receipt fails run.WrongState | run.WrongStage | run.Closed | run.BadLength | NoSuchRun | Unavailable,
	Cancel(c CancelRun) Receipt fails run.WrongState | run.WrongStage | run.Closed | run.BadLength | NoSuchRun | Unavailable,
	Retry(c RetryRun) Receipt fails run.WrongState | run.WrongStage | run.Closed | run.BadLength | NoSuchRun | Unavailable,
	Board() board.View,
	RunOf(id string) run.Run fails NoSuchRun,

	// DuplicateIsNoop: a command sent twice is taken once: the resend
	// gets the first receipt.
	law DuplicateIsNoop(c SubmitRun) { DuplicateIsNoop(c) },
}
```

Projections are generators over the checked operation type: no reflection at run time and no checker special case. They lower to plain Go calls: a static `flag` tree, a `switch` on the request's tag.

**An external system is an operation type with laws**, chosen in `cmd/<name>/main.ergo` and scripted in a test:

<!-- quote examples/pipeline/pipeline/runner.ergo Runner -->
```go
// Runner runs try attempt (1 is the first) of stage stage of the run id,
// and says whether it passed. A runner keys its work by the run, the stage and the try, so a
// call sent again after a lost answer runs nothing twice.
type Runner {
	Run(id string, stage int, attempt int) bool fails Offline,

	// Repeatable: the same try asked twice gets the same answer.
	law Repeatable(id string, stage int, attempt int) {
		let first = Run(id, stage, attempt) catch {
		case Offline:
			return true
		}
		let again = Run(id, stage, attempt) catch {
		case Offline:
			return false
		}
		again == first
	},
}
```

## 6. Views, policies and sagas

**A view is a fold over events, with a law that it equals the view of the states.**

<!-- quote examples/pipeline/board/board.ergo Apply -->
```go
// Apply is v after the event e of the run id: an event that moves the
// run takes it out of its column and puts it last in the new one.
func Apply(v View, id string, e run.Event) View {
	match moved(e) {
	case Some[Column] as c:
		let out = View{Queued: without(v.Queued, id), Running: without(v.Running, id), Failing: without(v.Failing, id), Passed: without(v.Passed, id), Stopped: without(v.Stopped, id)}
		put(out, c.Value, id)
	case None:
		v
	}
}
```

<!-- quote examples/pipeline/board/laws.ergo BoardIsFoldOfEvents -->
```go
// BoardIsFoldOfEvents: the board folded from the events of every run is
// the board of the runs' states, with each run once, in the column of
// its state, in the order the runs moved last.
law BoardIsFoldOfEvents(acts []Act) {
	let mut runs = Map[string, run.Run]{}
	let mut order = []string{}
	let mut v = Empty
	for a in acts {
		let id = nameOf(a.Pick)
		let s = match runs.Get(id) {
		case Some[run.Run] as r:
			r.Value
		case None:
			run.Unsubmitted
		}
		let c = if a.Move%4 == 0 { run.Cancel{Why: "stop"} } else { run.Forward(s, a.OK, a.Stages) }
		let es = run.Decide(s, c) catch {
		case run.WrongState, run.WrongStage, run.Closed, run.BadLength:
			[]run.Event{}
		}
		for e in es {
			v = Apply(v, id, e)
			match moved(e) {
			case Some[Column]:
				order = without(order, id).Append(id)
			case None:
			}
		}
		runs[id] = run.Fold(s, es)
	}
	v == Of(order, runs)
}
```

**A policy is a pure function from an event to what it causes**, so a replayed log gives the same notices or commands:

<!-- quote examples/pipeline/notify/notify.ergo React -->
```go
// React is the notices that the event e of the run id gives.
func React(id string, e run.Event) []Notice {
	match e {
	case run.Finished:
		[]Notice{Passed{ID: id}}
	case run.Abandoned as a:
		[]Notice{GaveUp{ID: id, Stage: a.Stage}}
	case run.Stopped as s:
		[]Notice{Cancelled{ID: id, Why: s.Why}}
	case run.Submitted, run.Started, run.StagePassed, run.StageFailed, run.Retried:
		[]Notice{}
	}
}
```

**The log is the source; a view and a policy are its followers.** `examples/pipeline`'s desk keeps each step's entries in the stream `pipeline` of a `domain.Log` before it commits the step, and a restart folds the stream back into the desk (`domain.Sourced` with `Evolve`). A command taken while the desk runs a try or waits out a backoff is committed with `domain.Keep`, which keeps the entry: the try and the wait go on (`CommandKeepsTheWait`). The board and the notices are not part of the desk: each is a `domain.Follow` follower of the stream with its own cursor, so a follower added later reads the whole history, and one that falls behind catches up from its cursor. The laws hold the parts together: the stream folds back into the desk the scene ended in, a restart part-way goes on to the story's end, and the followed board is the fold of the entries.

<!-- quote examples/pipeline/pipeline/log_laws.ergo RestartGoesOn restarted -->
```go
// RestartGoesOn: a context stopped after the acts before cut and opened
// again over the same log (a restart: it recovers by Evolve, then
// appends after the last record) plays the rest of the acts to the desk
// that the story of all of them gives.
law RestartGoesOn(acts []Act, flaky []int, cut int) { restarted(acts, flaky, cut) }

// restarted is whether the acts, played on a log, stopped after the acts
// before cut and played on again, end in the story's desk.
func restarted(acts []Act, flaky []int, cut int) bool {
	let k = len(acts) + 1
	let at = ((cut % k) + k) % k
	let log = memory.New[Entry]()
	match played(acts[0:at], 0, flaky, log) {
	case Some[Played]:
	case None:
		return false
	}
	match played(acts[at:len(acts)], at, flaky, log) {
	case Some[Played] as p:
		Normal(p.Value.Desk) == Normal(Story(acts, flaky))
	case None:
		false
	}
}
```

**A saga is a machine of its own**, with a timer on its waiting state; its laws run under `machines.RunTimed` on a virtual clock:

<!-- quote examples/pipeline/retry/retry.ergo Saga -->
```go
// Saga is the tries of one stage under the Standard policy.
type Saga machine[Attempt, Signal] {
	initial: First(Standard),
	step:    Step,
	after:   Timer,
}
```

## 7. Stories and scenes

**A story is the pure reference of what the program should do; a scene plays the same acts on the real program; a law says they agree.** The scene binds a `testing.Script` of each external system and a clock that only the scene moves, so it is deterministic and replayable from its seed. A Script call can be held and answered later (`s.Run.Hold()`, `s.Run.Release(f)`), and a test that ends with a held call or an untaken release fails, so a scene accounts for every call it scripts.

<!-- quote examples/pipeline/pipeline/scene.ergo SceneAgreesWithStory -->
```go
// SceneAgreesWithStory: the program, played act by act with a scripted
// runner and a manual clock, ends in the desk that the story gives.
law SceneAgreesWithStory(acts []Act, flaky []int) {
	let want = Story(acts, flaky)
	let s = testing.Script[Runner]{}
	s.Run.Answer(func(id string, stage int, attempt int) bool fails Offline {
		match answerOf(flaky, stage, attempt) {
		case Some[bool] as a:
			a.Value
		case None:
			Offline{Why: "down"}
		}
	})
	let clock = time.NewManual(0)
	let got = with context { Scheduler: clock, Runner: s } { scene(acts, clock) } catch {
	case domain.Conflict, domain.Reused, domain.Unkept, Unavailable:
		return false
	}
	got == want
}
```

A **stage** (`testing.Stage`) is the one kernel that a domain scene, a UI scene and a page scene share: play an act, settle, advance the clock, show. `testing.Play(st, steps)` plays a list of `Step`s and gives what the stage shows after each, and `actor.NewStage(a, clock)` is the stage of an actor over a `time.Manual` (stdlib.md, Testing 14); its law `ActorStageAgreesWithFold` holds each settled show to the fold.

## 8. A UI is a view of a domain

**Rule 6. Projecting the Api to a UI is a pattern, not a package.** The screen is its own machine, whose state holds what only the screen knows and the domain's view as a load. A command is a signal that carries the Api's request value; the view asks the host for the call as a load (`ui.Load`), so the answer, a rejection included, comes back as a signal and so as state that the view shows. (A signal that an actor's `work` sends would change the state with no new pass of the view, since a `ui` session renders after its own events.) The view is one function of the state, given the provider of the Api, which the test host, the terminal host, the desktop host and the page host all draw. `cmd/<name>/main.ergo` chooses that provider; the screen does not change. The worked example's screen is `examples/pipeline`'s `screen` and `view`: scenes on `ui.TestHost` hold a call of the runner and of the Api, and `SurfacesAgree` plays one list of requests on the screen as on the model, the generated RPC client and the command line.

## 9. Infrastructure

A deployment is a domain too. A **stack** is an ordinary function that asks **kinds** for objects (`Ensure(id, props)`); a **driver** is what a provider module implements for one kind, with laws (`RetrySafe`, `DeleteIsIdempotent`, and the others). Plan and apply are two providers of the same kinds. The apply protocol (write ahead to the state store, call the driver, write the live row; sweep what the stack no longer wants) is checked as an Ergo machine by `ergo test models/infra` ([models/infra/infra.ergo](../models/infra/infra.ergo)): every state of a small scope, with its invariants as laws and each broken variant refuted. Module `infra` ([modules/infra](../modules/infra/README.md)) holds the engine, its laws and a memory provider; its model-based test crashes runs at every call. **Outputs.** A stack returns a record of its own; `infra.Deploy` writes it as JSON once the apply ended (after the sweep), so a run that fails leaves the outputs of the last one that ended, and the program reads it with std `config` (`config.Load[O](infra.OutputsSpec(path))`), as any configuration file (law `OutputsRoundTrip`). **The command line.** `infra.Run` gives a stack `plan` (with `-refresh`, the drift, read only), `apply -yes`, `destroy -yes`, `state` and `unlock HOLDER`, as text or `-json`, and exits 3 when another holds the lock.

## 10. Prior art

| Language | What it does | What Ergo takes |
| --- | --- | --- |
| Elixir | Commanded: `execute/2` (decide) and `apply/2` (evolve), process managers, event handlers with a cursor; an aggregate is a GenServer | The split of decide and evolve, a cursor for followers, state in the actor with snapshots (std `journal`); not the dynamic dispatch: `match` is checked for totality |
| F# | The Decider (`decide`, `evolve`, `initialState`) and Equinox | The names. A pure `decide` is a convention there; in Ergo it is the empty row |
| Gleam | Lustre's model, update, view; `lustre/dev/simulate` runs an app with simulated events | The simulated run as the test shape (stories and scenes). Gleam has no effect row, so its update can do anything |
| ReScript | Variants for actions and `useReducer` | The lowering: a command union is a Go tag switch, with no allocation for a variant without fields |
| Effect (`@alchemy.run/domain`) | Aggregates, commands with ids, rejections, views, policies, sagas, stories, one Api projected to RPC, HTTP, CLI and MCP | The guarantees, not the API shape: no `Effect.gen`, no Schema classes, no tags; rows, unions, operation types with laws and contexts instead |
