# router

A message router. Its route table maps topics to destinations. Clients ask the router to change the table; the router takes one change at a time, asks the operator before a change sends a routed topic elsewhere, checks each change against a registry of destinations, and answers each request once. A dispatcher carries messages by the table, with one courier per route. The router runs under a supervisor over a journal; the dispatcher runs under a supervisor of its own.

Tag: `default`. It is an original program, a neutral re-creation of a common pattern: a class C machine that serves requests one at a time from a queue in its state, with supervision, durability, hosts for children, and observers. Its goldens were made by running the program, and then checked line by line against the rules below. It replaces the planned `order-desk` and `kiosk-router` (owner, 2026-11-09: neutral domains only).

## Rules

- Router states: `idle`, `confirming #ID` (the operator is asked about the current request) and `checking #ID` (the registry checks it: the state's work). Every state holds the table, the queue of waiting requests and the answers given so far.
- A request is `route ID TOPIC DEST` or `remove ID TOPIC`. A request with an id the router knows (waiting, served or answered) is refused (`? #ID is taken`): each request is answered once.
- An idle router serves a request at once. A busy one queues it (`(waiting: …)`). When a request ends, the router takes the first waiting one (the eventless step `Settle`); with none it is idle.
- A route that would send a routed topic to another destination asks the operator first (`! #ID: … ?`). `yes` checks it; `no` answers it `cancelled`.
- `abort ID` answers a waiting or current request `cancelled`. During a check the check is interrupted and its result is dropped. An answered or unknown id is refused.
- The registry (`memory.Directory`) knows `disk`, `socket` and `stdout`, and a table holds four routes. A refused change is answered `refused: …`; an applied one `applied: <table>`.
- The router keeps each committed signal in its journal before anyone sees the state. `drop` makes the registry's next check a defect: the supervisor restarts the router (twice at most) in the state the journal recovers, and the check runs again. `restart` stops the router and opens a new one from the journal, as a new process would; the replay must give the state that stopped.
- After each change, the dispatcher takes the table: a new entry, with a courier child per route, each named by a host (`actor.Host`) before it runs. `send TOPIC BODY` asks the topic's courier, found through its host, to carry the message, and waits until the dispatcher's log has it (`topic -> dest: body`). A body `crash` crashes the courier: a child's defect ends its parent's incarnation, the dispatcher's supervisor restarts it `waiting`, and it takes the table again.
- The last line, `end:`, is the table and the number of answers.

## Layout

The program is a module of packages (`ergo.mod`: `module router`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `route/` | The domain package. `table.ergo`: `Route`, `Table`, `Change`, `Request`, `Answer`, the typed failure `Unknown`, and the contract `Registry { Check fails Unknown }` with its laws. `machine.ergo`: the router's state `Router`, its `Signal`, `Settle`, `Step` and the machine `Routing`, with work `check`. `dispatch.ergo`: the courier machine `Couriers` and the dispatcher machine `Dispatcher`, whose `Carrying` entry starts a courier per lane in the lane's host. `laws.ergo`: the step's rules. |
| `memory/` | `Directory`, a `route.Registry`, with an arm for a dropped check. `laws.ergo`: the live rules over the actors. |
| `script/` | `Read` (fails `BadLine`), `Recover` (the journal's replay), `Open` (the supervised durable router) and `Play`. |
| `cmd/router/` | The binary: it binds `route.Registry` to a `memory.Directory`, the journal to std's `journal/memory`, and `Scheduler` to a manual clock that no one moves. It is the only place that chooses them. |

## What it shows

- **A request queue in the state (survey M52: no postpone).** `Board.Queue` keeps waiting requests; a request that arrives while the router is busy is a `Stay`, so the current entry (and its work) goes on.
- **Eventless step (M48's pattern).** `Settle` is the state after a request ends; every arm that ends one ends in it.
- **Ask and reply.** A client's `Ask` returns when its request is queued; its answer is in the state, where a client waits for its own (`actor.WaitFor`, MU12). Answers are not outputs.
- **State-owned work (MU4) with a typed failure.** `check` gives `Applying` a work against `Registry`; `Unknown` is a failure and a signal, so it needs no map.
- **Supervision (MU7) and durability (MU9).** `actor.SuperviseDurable` with `recover` = the journal's load and `machines.From`, and `save` = `Append`. A restart starts in the recovered state, never in the crashed memory, and the answers stay once each (law `RestartRecovers`).
- **Hosts (mu-host).** A lane names its courier before it runs; the script finds a courier through `Host.Get`, and a host of an ended entry fails `Stopped` (law `HostSeesItsEntry`).
- **Children (MU5) under a supervisor.** A courier's crash ends the dispatcher's incarnation; `actor.Restarts` counts it.
- **Exhaustive `match`.** Every step arm names each state; a check's result in a state with no check is `Ignore`.

## Laws

| law | where | rule | mutant it kills (measured, `MB2/router/mutants.ts`) |
| --- | --- | --- | --- |
| `AnsweredOnce` | route | each request the router took has one answer, or is served, or waits | a known id is taken again; `Settle` takes the last request; an abort of a waiting request gives no answer |
| `FifoCommit` | route | requests are served in the order they were taken | (with `AnsweredOnce`: the last-first mutant) |
| `AbortNeverCommits` | route | an aborted request is answered `cancelled` | — (`AnsweredOnce` kills the mutants first) |
| `AbortIsTaken` | route | an abort of a waiting or served request is taken | an abort during a check is refused |
| `ReplaceIsConfirmed` | route | a change that replaces a route is checked only after a yes | no question is asked |
| `AppliedIsTheTable` | route | the table is the table of the last `applied` answer | a check's table is not kept |
| `ReplayEqualsLive` | memory, live | a router opened from the journal is the one that stopped, and each dispatcher table opens an entry | `save` keeps nothing; `recover` replays nothing; `recover` gives `None`; a `Use` that stays |
| `RestartRecovers` | memory, live | a dropped check restarts the router, and the answers and the table are those of a run with no drop | (with the above) |
| `HostSeesItsEntry` | memory, live | an old lane's host fails `Stopped`; the new one gives a running courier | — (runner's rule, held by its property) |
| `Registry.CheckRoutesOne`, `CheckRemovesOne` | contract | a check changes the topic's route only | a remove that keeps the route |

10 of 11 measured mutants die by an assertion. One, a dispatcher that drops a courier's output, is not in the laws: every wait for the output (`WaitFor` on the log) then waits for ever (gap MB2-G6).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/router/cmd/router < examples/router/cases/table/stdin
./bin/ergo test examples/router/route
./bin/ergo test examples/router/memory
go run ./scripts/examples router
```

## Cases

- `table`: a queue behind an operator's question; a taken id; an abort of a waiting request; a refused destination; aborts and replies with nothing open; a remove; a message carried, and one with no route.
- `restart`: a dropped check restarts the router from its journal (twice: the budget); a courier crash restarts the dispatcher; a new process opens the router from the journal, and a taken id stays taken.
- `badscript`: an id that is not a number (status 1).

## effect-machine side by side (`docs/persistence-and-supervision.md`, ActorHost)

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| requests that wait | no postpone either; a queue in the state or the mailbox (inferred from its docs) | `Board.Queue`, with `Stay` so the current entry goes on | step laws |
| one answer per request | `call` answers with the transition; a later answer is a second protocol | the answer lives in the state; `WaitFor` reads it; `AnsweredOnce` holds it | law |
| persistence | `lifecycle.recovery.resolve` at spawn, `durability.save` after each commit | `SuperviseDurable(m, …, recover, save)`: save runs before anyone sees the state; the step must be pure (S108) | runner + checker |
| supervision | `Supervisor` with a restart schedule | `tasks.Restart{Limit}`; a restart runs `recover` again | runner (MU7 model) |
| a child named before it runs | `ActorHost.make` with `acquire` | a `Host` in the state, filled once by `ChildIn` in the entry's work, closed with the entry | runner (models/host) + law |
| observing | `subscribe`, `waitFor` | `Changes`, `WaitFor`, loops with no callback | runner (MU12) |

Where Ergo could not improve:

- **Two capabilities in one work row (MB F8).** The intended form is one machine: `Idle` hosts the couriers and `Applying` checks with the registry, so the router's work is `func work(s Router) Option[machines.Work[Signal, Registry | Scheduler]]`, and a courier's crash restarts the router from its journal. On this base the row does not parse (F8; fixed on `wf10/b13-fumb-b` cdfbe8cb, which lands in batch-13 integration), so the dispatcher is a second actor and the script gives it each table.
- **A host in a durable state (inferred, not measured).** Even with F8, a host kept in a journaled state is the host of an ended entry after a recovery, and `ChildIn` on it is a defect. The intended form needs the step to make fresh hosts on the recovered entry, or a recovery that leaves hosts out.

## Benchmark

No em-parity §3 scenario is this program's own: an `Ask`, a work start, a save and a child start are measured by MU3's, MU4's, MU5's and MU9's benchmarks. The command for an end-to-end time of the recorded cases is in `MB2/bench-pending.md` (router).

## Limits found

- **MB F8** (above): the dispatcher is a second actor.
- **MB2-G5** (fixed by fu-mb fumb-e) `machines.From` needed the machine's whole capability row though it runs no work, so `Recover` needed `route.Registry`. A machine's work row is now apart from its step row, and a fold needs the step's row only.
- **MB2-G6** A wait for a child's output (it reaches the parent as a signal, later) cannot be bounded: a law that waits for it hangs when the output is lost, and fails as a deadlock, not as an assertion.
- **No type aliases of a foreign type.** `actor.Actor[route.Router, route.Signal, Decision[route.Router, route.Output]]` is written out at each use.
