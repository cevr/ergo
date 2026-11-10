# fetchpool

A small download manager over in-memory connections. A dialer task opens
connections and moves them into a queue. A worker Set borrows that queue
and a shared admission limiter. Each worker owns the connection it pops;
the queue releases any unread tail. The limiter outlives both.

Measured support: the recorded CLI case and the generated tests run real
tasks, not a simulation. The source reaches no network, host clock or
private application. The queue has room for the input batch; the dialer
finishes before workers start. This policy makes an unread tail's order
observable without relying on the Go scheduler.

## Layout

- `domain/`: `Fetcher`, `Conn`, `Limiter`, typed failures, and each download's
  `Queued | Fetching | Done | Failed` machine. Exhaustive matches interpret
  states and signals. `KeepsNumber` and `StartKeepsNumber` state identity
  contracts.
- `memory/`: recording connections, a law-test Script provider and a finite
  synchronized limiter. Connection 2 fails its read; negative IDs fail to
  open. Releases append to a synchronized log.
- `pool/`: ownership transfer through a resource queue, a dialer task and
  workers kept by a Set. Incoming limiter and queue captures are structured
  lends, not moving copies or ambient registries.
- `cmd/fetchpool/`: the only provider selection and the generated property.
  It varies input count, admission budget and worker count, then checks
  terminal reports and exactly-once cleanup after every task ended.

## Run

```
ergo run examples/fetchpool/cmd/fetchpool
ergo test examples/fetchpool/cmd/fetchpool
go run ./scripts/examples fetchpool
```

The demo admits three of five downloads. It prints success for 0 and 1,
a typed read failure for 2, closes queued connections 4 then 3, then closes
the limiter. There is no hand `Close` in application code, no epoch check,
no finalizer registry and no host-interface trust assertion. Effect's
acquire/release and bounded concurrent processing are the intent; Ergo
uses owned values, rows and structured lends instead of library scopes
or callback lifetime bookkeeping. Go's buffered channel and worker group
are the control shape; this example makes no speed claim.

`testdata/mutants.json` names the application mutations only these new
laws and this composed example catch. The CLI case fixes the deterministic
single-worker release order; multi-worker tests require counts and a
completed limiter boundary, not an order between independent workers.

## Explored schedules

`TwoWorkersShareTheBudget` runs two workers on a budget of two. The
`explored` file has the examples gate search its schedules with
`ergo test cmd/fetchpool --run TwoWorkersShareTheBudget --explore dfs
--preempt 1`, which proves it for one preemption. A limiter whose `Admit`
reads the budget and then writes it (`testdata/mutants.json`, X8) admits a
third download in the 20th schedule; the plain tests, the generated
property and the `Limiter` law all pass it. The report names each handle
by its Ergo call (`read on sync.NewRef at memory/fetch.ergo:39:14`) and
prints a `--schedule` command that replays the failure.

## Limit found

An implicit conversion from a concrete resource argument to a lending
operation-type parameter can lose its release in the current compiler,
under both lowering selectors. The composition root names its owned local
as `domain.Limiter`, so provider selection happens at the owned binding,
not inside a call argument. A minimal neutral repro is filed in the
X-L8 request for a separate compiler fix; no support is claimed for that
conversion path. This example exposed the gap by requiring the final
limiter-close event.
