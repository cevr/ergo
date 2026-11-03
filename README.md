# Ergo

**An experimental Go-shaped language that compiles to plain Go. It is designed to check the guarantees that large Go programs keep only by convention.** The guarantees are typed errors, declared dependencies, resource safety, structured concurrency with cancellation, every failure cause kept, built-in tracing and observability, testable time, and laws. Ergo's design puts them in the language, where the compiler checks them (`ergo test` runs the laws). The compiler then erases the checked rows, so a row adds no wrapper type and no run-time cost.

Ergo is a design and a prototype. The program below is the designed language. It parses and formats today, and most of its checks are designed, not built:

```go
import testing
import timeout

// A signature lists every expected failure and every service the caller must supply.
func Charge(card Card, amount int64) Receipt fails Declined | Frozen | timeout.Expired needs Accounts, Payments, Scheduler {
	return timeout.After(2s) { // the block runs as a child task; at 2s it is stopped and drained
		let lock = try Accounts.Lock(card) // a resource: released on every exit, the timeout included
		Payments.Charge(lock, amount)      // lends lock for the call
	}
}

// A law lives in its operation type. It must hold for every Payments provider, and ergo test checks it like a fuzz target.
type Payments {
	Charge(lock Lock, amount int64) Receipt fails Declined,
	Refundable(r Receipt) int64,

	law RefundNeverExceedsCharge(r Receipt) { Refundable(r) <= r.Amount },
}

// A test with replaced providers and virtual time: no sleeps and no mock framework.
let SlowWorld = context {
	Accounts:  MemoryAccounts{},
	Payments:  NewSlowGateway,         // a constructor: it answers after 5s on this context's Scheduler
	Scheduler: testing.VirtualClock{}, // jumps to the next timer when every task waits
}

test SlowGatewayTimesOut {
	let r = with SlowWorld { Charge(Card{Number: "4242"}, 100) } catch {
	case timeout.Expired:
		Receipt{ID: "timed-out"} // reached at once, in virtual time
	case Declined, Frozen:
		Receipt{}
	}
	assert r.ID == "timed-out"
}
```

**What runs today:** the parser and the formatter accept all of this code. The prototype checker checks error rows, `try`, total `catch`, `needs` rows, closed wires and `timeout.After`. The task runtime `runtime/rt` records traces. Resource and law checks, provider replacement, `testing.VirtualClock` and `ergo test` are designed, not built. See the [status table](#status).

Designed output, because `ergo test` is not built yet ([#196](https://github.com/cevr/ergo/issues/196)):

```console
$ ergo test --trace
TestSlowGatewayTimesOut  ok  virtual 2s
  └ timeout.After(2s)    pay.ergo:10:9   expired
      └ Accounts.Lock    pay.ergo:11:15  released
      └ Payments.Charge  pay.ergo:12:3   interrupted
```

In about thirty lines, the design checks what Go leaves to convention. Every expected failure is in a type. Every service the caller must supply is declared. The lock cannot leak. The timeout stops its block and waits for its cleanup. The law covers every Payments provider. The test is designed to run in virtual time, and its trace needs no logging code. The prototype already emits plain `if err != nil`, ordinary interfaces and calls into a small task runtime.

## The guarantees

The "In Ergo" column is the design. The last column says what runs today.

| Guarantee | What it means | In Go today | In Ergo | Runs today |
| --- | --- | --- | --- | --- |
| **Typed errors** | You can see every expected failure of a function, and the compiler knows which failures you handled. | `error` says only "something failed" | `fails NotFound \| Declined`, exhaustive `catch` | prototype checker |
| **Declared dependencies** | You can see every service the caller must supply. A missing one is a compile error. | `context.Context` values, globals, constructor plumbing | `needs` rows, `wire … provides`, `with` | prototype checker: `needs` rows, closed wires |
| **Resource safety** | Everything acquired is released once, on every exit: success, failure, panic or cancellation. | `defer`, if you remember it | release at scope end; moves and lends | release rules in `runtime/rt`; checks designed ([#197](https://github.com/cevr/ergo/issues/197)) |
| **Structured concurrency** | No task outlives its scope. A stop reaches every child. A stop cannot cut cleanup short. | goroutines can leak; `context` is cooperative | scoped tasks, interruption points, masked release | task tree, stops and masks in `runtime/rt` |
| **Every cause kept** | When several things fail, you get all of them, with defects kept apart from expected failures. | needs explicit aggregation (`errors.Join`) and recovery code | one cause with every failure, defect and interruption | designed; `runtime/rt` keeps defects, not yet every child failure |
| **Built-in observability** | Every task and every service call can be traced, keyed to its source, with no code changes. | spans added by hand | trace events named by the compiler; off adds no allocations | `runtime/rt` and the prototype ([#188](https://github.com/cevr/ergo/issues/188)) |
| **Testable time and effects** | A test swaps any service, the clock included, with no mock framework. | interfaces and hand-written fakes | `replace` in a test wire; `testing.VirtualClock` | designed; `runtime/rt` has a virtual clock |
| **Laws** | You state what must always hold, and every implementation is checked against it. | comments and example tests | `law X(…) { … }` in an operation type | designed ([#162](https://github.com/cevr/ergo/issues/162)) |

Ergo keeps Go's spelling, Go's runtime and garbage collector, and single static binaries built by `go build`. The compiler is designed to check the guarantees, erase them and emit ordinary Go, with no wrapper values, so a function is still a function and an error type still implements `error`.

---

## Errors you can see

*Status:* the prototype checker enforces the rules below today ([rows](docs/spec/rows.md)). One part of the example is designed, not built: a method call on a value, such as `order.Paid()`.

```go
type NotFound { ID string }
type Declined { Reason string }

func Checkout(id string, card Card) Receipt fails NotFound | Declined needs Orders, Payments {
	let order = try Orders.Get(id)
	let receipt = try Payments.Authorize(card, order.Total)
	Orders.Save(order.Paid())
	return receipt
}

func Settle(id string, card Card) fails NotFound needs Orders, Payments {
	let receipt = try Checkout(id, card) catch {
	case Declined:
		Receipt{} // handled: Declined leaves the row
	} // NotFound is not handled: try passes it up, into the row of Settle
	Orders.Note(id, receipt.ID)
}
```

- `try` passes a failure up, and the caller's row must include it.
- A `catch` without `try` must handle every failure in the row. A `catch` after `try` handles the types it names and passes the rest up.
- Interruption and defects are in no row. They pass through every `catch`.
- In Go, these become plain `if err != nil { return … }`. The row is checked, then erased.

## Needs and provides

*Status:* the prototype checks `needs` rows, inline wires and closed named wires today. The rest of this section is designed, not built: the `provides` and `needs` wire header, constructors, embedding, `replace` and an error row on `main` parse and format, and their checks are designed ([rows](docs/spec/rows.md#wire)).

```go
let Store = context provides Orders needs Config {
	Orders: OpenPostgres, // a constructor that returns a resource: closed when the with block ends
}

let Live = context provides Orders, Payments {
	Payments: Stripe{},
	Config:   EnvConfig{}, // Store uses it; callers cannot see it
	Store,
}

func main() fails NotFound | Declined | DialFailed needs Console {
	let receipt = try with Live { Checkout("ord-7", Card{Number: "4242"}) }
	Console.Println(receipt.ID)
}
```

- `needs` lists the services the caller must supply. `provides` lists what a named wire exposes. Every other entry stays private to the wire's build.
- `with` meets the needs of its block. A capability that nothing provides is a compile error that names it.
- A service that a `with` inside the body supplies leaves the row: `main` calls `Checkout`, which needs `Orders` and `Payments`, yet `main`'s row lists only `Console`. The row is what remains for the caller, not every service the body touches. The footprint, every capability a body uses, is designed: the compiler will infer it and `ergo inspect` will show it ([rows](docs/spec/rows.md#footprints)).
- A constructor's failures join the row of the `with` that builds it, so `main` lists `DialFailed`.
- Prior art: Roc's `provides` and `requires`, and Google Wire.

### Tests replace providers, with no mock framework

*Status:* designed, not built. `replace` and embedding parse and format; `testing.VirtualClock` is designed ([stdlib](docs/spec/stdlib.md#testing)).

```go
let Fixture = context provides Orders, Payments, Scheduler {
	Live,
	replace Orders:   MemoryOrders{}, // OpenPostgres is never built
	replace Payments: FakePayments{Decline: "4000"},
	Scheduler:        testing.VirtualClock{},
}
```

## Resources close themselves

*Status:* designed, not built: the checker does not yet track resources ([resources](docs/spec/resources.md), [#197](https://github.com/cevr/ergo/issues/197)). `runtime/rt` implements the release rules that emitted code will call.

```go
func copyFile(src, dst string) fails NotFound | WriteFailed needs FS {
	let in2 = try FS.Open(src)
	let out = try FS.Create(dst) // if this fails, in is still closed
	try io.Copy(out, in2)        // lends out and in
} // out closed, then in
```

- A type with `Close` is a resource. So is a struct with a resource field. The scope that owns a resource closes it, in reverse order, on every exit.
- Returning a resource, storing it in a struct or giving it to a task moves it. Using it after a move is a compile error.
- Passing a resource as an argument lends it. A lent resource cannot escape the call, so no lifetime annotations are needed.
- Releases run to completion, even when the scope ends because it was stopped.
- There is no cleanup call to forget, and no double close.

## Tasks that cannot leak

*Status:* `task.Go`, `Task` and the `tasks` package are designed, not built ([runtime](docs/spec/runtime.md#tasks)). Today `runtime/rt` implements the task tree, stops, masks and `timeout.After`, and the prototype checks `timeout.After`. The runtime keeps every defect, but not yet every typed failure. A completed child's failure that nobody waited for is dropped when its scope closes normally. When a deadline wins, `timeout.After` drains the child and returns `timeout.Expired` to its caller, with any cleanup defects; the child's own exit keeps its own failure, marked interrupted when the stop reached the child first, and the caller's cause does not carry it ([receipt](runtime/rt/RECEIPT.md)).

```go
func Prices(ids []string) []Price fails NotFound | timeout.Expired needs Catalog, Scheduler {
	return timeout.After(2s) {
		tasks.Map(ids, 8, func(id string) Price fails NotFound needs Catalog {
			return Catalog.Price(id)
		})
	}
}
```

- `tasks.Map` keeps input order and runs at most 8 calls at once. The first failure stops the rest.
- A child task belongs to a scope. The scope waits for it and stops it on timeout. By design, the scope's exit keeps every cause: the timeout, each child failure and each cleanup defect.
- A stop is checked at interruption points, and cleanup runs uninterrupted.
- `go f(x)` starts a task and gives a `Task[T, E]`. A `Task` is a resource, so its owner scope stops and drains it.

## Machines and actors, as ordinary code

*Status:* designed, not built. The `machine` declaration and the multi-subject switch parse, and their checks are designed ([runtime](docs/spec/runtime.md#machines-and-actors)). The actor library's hand-written Go lowering runs in [`prototypes/actorlib`](prototypes/actorlib/doc.go), with its cost measured ([#101 note](docs/research/actor-library.md)).

```go
type State = Closed | Open | HalfOpen
type Signal = Failure | Success | Retry

func step(s State, sig Signal) Decision[State, string] fails ProbeDown needs Probe {
	match (s, sig) {
	case (Closed, Failure):
		return Enter{Open{sig.Endpoint}}
	case (HalfOpen, Success):
		return Enter{Closed{0}}
	case (_, _):
		return Ignore{}
	}
}

type Breaker machine[State, Signal] {
	initial: Closed{0},
	step:    step,
}

func trip(endpoint string) fails actor.Stopped needs Probe {
	let b = actor.StartMachine(Breaker{}, 16) // the actor library runs it; stopped at scope end
	try b.Send(Failure{Endpoint: endpoint})
}

func join(name string) int64 fails RoomFull | actor.Stopped | actor.NoReply {
	let room = actor.StartAgent(Room{}, 16)
	return actor.Call(room.Address(), func(r *Room) int64 fails RoomFull { return r.Join(name) })
}
```

- `step` is an ordinary function. Its switch must cover every pair of state and signal. `ergo inspect` will draw the machine's graph from its cases.
- Actors are a library, not syntax. `actor.Start`, `actor.Ask` with a typed `actor.Reply[T, E]`, `actor.StartMachine`, `actor.StartAgent` and `actor.Call` are ordinary code, and no compiler rule names them.
- A reply is a resource. Every ask is answered once, or the caller gets `actor.NoReply` or `actor.Stopped`.

## Laws: say what must always hold

*Status:* designed, not built ([types](docs/spec/types.md#laws), [#162](https://github.com/cevr/ergo/issues/162)). The #125 spike built the law check, generators, shrinker and receipts for the earlier language, on a branch that is not merged.

```go
// A law lives in the operation type it constrains, and runs with generated
// inputs and shrinking. A failure that escapes it refutes it.
type OrderCodec {
	Encode(o Order) string,
	Decode(s string) Order fails DecodeFailed,

	law RoundTrip(o Order) { try Decode(Encode(o)) == o },
}

// It is checked against every provider bound to Store.
type Store {
	Find(id string) Order fails NotFound,
	Put(id string, o Order),

	law FindAfterPut(id string, o Order) { Put(id, o); try Find(id) == o },
}
```

- A law is `law X(…) { … }` among the operations of a type, and its body ends in a `bool` expression. `ergo test` checks it with generated arguments.
- A law applies to every provider of its type. A new provider must pass the same laws as the old one.
- A refuted law fails the build. A passing law is reported as tested, never as proved.
- Under research: evidence values and a tier of laws that are proved instead of tested ([laws note](docs/research/laws.md)).

## Observability by construction

*Status:* `runtime/rt` and the prototype's kernel build record traces today. From `prototypes/sketch9`, `go run ./cmd/sketchc build -rt kernel -rtroot ../.. testdata/trace/timeout.sketch -out /tmp/gen -o /tmp/timeout` builds the example; `ERGO_TRACE=/tmp/timeout.jsonl /tmp/timeout` records a run; `sketchc trace` queries it ([how](prototypes/sketch9/README.md#kernel-mode-and-traces-188)). The output below was recorded on 2026-10-27. `ergo trace`, `ergo ps` and an OpenTelemetry exporter are designed, not built.

The checker knows every definition, every capability call and every row. So the compiler can name every trace event, and the source needs no span calls. The [#188 spike](docs/research/traceability.md) put this into `runtime/rt` and the prototype compiler. This is real output of the prototype's trace query, recorded on 2026-10-27. In that run, a 100ms timeout interrupted `Scheduler.Sleep(2s)`, before `Gateway.Authorize` ran (trimmed; [full run](docs/previews/2026-10-27-traceability.md)):

```text
$ go run ./cmd/sketchc trace testdata/trace/timeout.sketch timeout.jsonl
tasks
  #1 latency.main (func) at timeout.sketch:81:1, exit: success
    #2 latency.checkout/timeout.After#1 (timeout block) at timeout.sketch:39:17, exit: interrupted

cause trace of the root exit (links recorded by the runtime; candidates are labelled)
  task #1 latency.main (func, timeout.sketch:81:1) exited with success
    handled timeout.Expired at timeout.sketch:75:2 in latency.report (task #1, seq 12)
      caught the deadline of task #2 latency.checkout/timeout.After#1 (seq 6): the timeout block timeout.After(100ms) at timeout.sketch:39:17 in latency.checkout, row error[latency.GatewayDown | timeout.Expired]
        call host.Scheduler.Sleep at timeout.sketch:32:2 was open when the deadline passed (call seq 5); it ended interrupted (call-exit seq 8)
        cancel-request of task #2 (seq 7)
        task #2 observed the stop in latency.roundTrip at timeout.sketch:32:2 (seq 9)
        task #2 exited with interrupted (seq 10)
```

- The query follows a failure back along links that the runtime recorded, to its deadline, child task or failed call. Where no link was recorded, it says so and labels the candidates.
- Tracing off adds no allocations, and its time cost is inside the measurement noise. Each recorded event costs about 60 to 130 ns (measured, see [Performance](#performance-measured)).
- A written trace holds no values by default. It names failures by type.
- Designed, not built: `ergo trace`, `ergo ps` for the live task tree of a running program, and an OpenTelemetry exporter. The mapping to OpenTelemetry spans is written; the exporter is not.

## Built for agents

*Status:* `ergo check`, `ergo diagnostics` and `ergo fmt` work today. `ergo inspect`, the MCP server and the language server are unavailable until they are ported to the new front end ([#203](https://github.com/cevr/ergo/issues/203)).

Agents read the same checked model as the compiler, through the CLI today and through MCP once it is ported. A diagnostic has a registered code, a position and a message. This is real output of the current CLI for a file whose `pay` calls `Gateway` without declaring it:

```console
$ ergo diagnostics pay.ergo
pay.ergo:19:9: error S102: this call requires main.Gateway, which is not in the capability row of function pay and no with provides it
```

`ergo inspect` will answer the same questions as JSON when it returns on the new front end ([#203](https://github.com/cevr/ergo/issues/203)). Designed output:

```console
$ ergo inspect checkout.ergo Checkout
{
  "def": "checkout.Checkout",
  "errors": ["checkout.Declined", "checkout.NotFound"],
  "needs": ["checkout.Orders", "checkout.Payments"],
  "calls": ["Orders.Get", "Payments.Authorize", "Orders.Save"]
}
```

- Each code has its rule, its spec section and, where one exists, a fixture ([diagnostics](docs/spec/diagnostics.md)).
- One formatter, `ergo fmt`, gives one canonical layout, so agent edits make small, stable diffs.
- Every expected failure, and every service the caller must supply, is in the signature. An agent can call a function, or change its body, without reading its whole call graph.

## Go interop

*Status:* designed, not built: the prototype checker refuses `import go` with S109 ([grammar](docs/spec/grammar.md#declarations)).

```go
import go strconv // every call through strconv charges go.Foreign

func parsePort(text string) int64 fails go.Error needs go.Foreign {
	return strconv.ParseInt(text, 10, 64)
}
```

- A Go function that returns `(T, error)` becomes one that returns `T fails go.Error`. A Go result that may be nil becomes `Option`. A Go panic becomes a defect. Slices and maps are copied at the boundary.
- A bodiless declaration with a `//ef:go pkg.Func` directive binds one Go function with stated types, and a generated adapter checks them.
- The emitted Go stays readable. The prototype carries source positions in `//line` directives, so Go panics, pprof, delve and the race detector point at Ergo lines.

## Performance, measured

Ergo is designed to lower to the Go you would write by hand: rows are erased, generics stay Go generics, and `try` becomes `if err != nil`. Each row below links its receipt.

| Measured | Result | Receipt |
| --- | --- | --- |
| Tracing off | no extra allocations; the time difference is inside the noise | [`runtime/rt` receipt, #188](runtime/rt/RECEIPT.md#tracing-off-and-on-188) |
| One traced event | about 60 to 130 ns | [traceability note](docs/research/traceability.md#measured-overhead) |
| Interruption check | one atomic load: 1.4 to 2.3 ns, about 0.3x a `select` on a channel | [`runtime/rt` receipt](runtime/rt/RECEIPT.md#design-notes) |
| Library actor against hand-written Go with the same contract | 1.16x to 1.60x the baseline's time (paired medians, five operations) | [#101 note, Q8](docs/research/actor-library.md#q8-cost) |
| Checked overflow on an accounting sum | 2.2x to 2.5x the wrapping time, so `+`, `-` and `*` wrap by default | [#108 record](docs/research/numeric-arithmetic.md#cost-of-checked-overflow-in-emitted-go-108-2026-10-27) |
| Rebuild of an emitted program after a one-literal edit | emit 4 to 5 ms, `go build` 234 to 260 ms (single samples) | [`prototypes/sketch9` receipt](prototypes/sketch9/RECEIPT.md#build-time-of-the-emitted-checkout-program) |

Performance claims in this repo require receipts. Each receipt names its machine and its load. The losses stay in the table: the actor library is slower than the hand-written baseline today.

## What Ergo does not do

These are rules of the designed language. The parser enforces the syntax rules today: `goto`, `fallthrough`, `const`, a type assertion and a type alias are S001 ([grammar](docs/spec/grammar.md#parser)).

- Go is the only target. Ergo adds no scheduler and no garbage collector; it uses Go's.
- There is no `nil`. Absence is `Option[T]`.
- There are no pointer fields and no reference fields. Linked structures use unions, index arenas or `sync.Ref`. This is a loss against Go.
- There is no `goto`, `fallthrough`, `const`, type assertion or type alias. A union switch replaces the type assertion.
- There are no macros and no user-defined operators. A construct that the compiler must know gets a keyword. A dot form is always a library.
- Expected failures are not exceptions: they are in the row. A panic is a defect, kept apart from typed failures.
- There is no package-level mutable state and no `init` function.
- A loop that calls no capability and starts no task has no interruption point, so a stop waits until the loop ends.
- In v0, `int64` is the only integer type. Other numeric types and floating point are designed with [#81](https://github.com/cevr/ergo/issues/81).
- Kernels, hard real-time and execution without a garbage collector are out of scope.

## Status

| Part | Status |
| --- | --- |
| Parser and `ergo fmt` | In the main compiler ([#195](https://github.com/cevr/ergo/issues/195)) |
| `ergo check`, `diagnostics`, `build` and `run` | Accept the prototype subset: rows, `try`, `catch`, closed wires and `timeout.After` ([example](examples/editions/sketch/main.ergo)) |
| Checker: types, generics, error and needs rows | In progress ([#196](https://github.com/cevr/ergo/issues/196)): v1 stages A (typed literals), B (type shapes, bare records, patterns and totality, tuples, conversions), C (tail results, `if` and `match` expressions, one `catch` with row values) and D1 (`let mut`, no shadowing, prelude names) are checked and lowered |
| Linearity, borrows and captures | Designed ([#197](https://github.com/cevr/ergo/issues/197)) |
| Lowering to Go in the main compiler | Open ([#50](https://github.com/cevr/ergo/issues/50)); prototype in [`prototypes/sketch9`](prototypes/sketch9/README.md) |
| Task runtime `runtime/rt` | In the main tree, with tracing ([#198](https://github.com/cevr/ergo/issues/198), [#188](https://github.com/cevr/ergo/issues/188)) |
| Standard library | Designed ([outline](docs/spec/stdlib.md), [#206](https://github.com/cevr/ergo/issues/206)) |
| Laws | Designed ([#162](https://github.com/cevr/ergo/issues/162)); the [#125 spike](docs/research/laws.md) prototyped them for the earlier language on an unmerged branch |
| Actors as a library | Landed as a spike in [`prototypes/actorlib`](prototypes/actorlib/doc.go) ([#101](https://github.com/cevr/ergo/issues/101), closed); the std package is [#57](https://github.com/cevr/ergo/issues/57) |
| Go interop | Designed |
| Lint rules, `ergo inspect`, MCP and the language server | Unavailable until ported to the new front end ([#203](https://github.com/cevr/ergo/issues/203)) |
| The earlier `.ef` language | Removed by [#205](https://github.com/cevr/ergo/issues/205) ([record](docs/receipts/retire-ef.md)) |

## Learn more

- [Specification](docs/spec/README.md): grammar, types, rows, resources, runtime, standard library and diagnostics.
- [North star](NORTH_STAR.md): the seven pillars and how ties are broken.
- [Prior art](PRIOR_ART.md): Effect, Go, Gleam, ReScript, Elixir, MoonBit, Borgo and others, with what Ergo takes from each.
- [Glossary](GLOSSARY.md): every domain term.
- [Roadmap](https://github.com/cevr/ergo/issues/1): the live Wayfinder map ([how it works](docs/wayfinder/README.md)).

## Working in this repo

You need Go 1.27 or later and Git.

```sh
git clone https://github.com/cevr/ergo.git
cd ergo
go build -o bin/ergo ./cmd/ergo

./bin/ergo run examples/editions/sketch/main.ergo  # prints: hello, Ada / no friend named Bob
./bin/ergo fmt --check examples/editions/sketch/main.ergo
./scripts/gate.sh                                  # the merge gate: formatting, vet, tests and CLI checks
```

- A source is one `.ergo` file. There is no manifest and no edition.
- Where things live: the parser in `internal/sketch/syntax`, the formatter in `internal/sketch/format`, the prototype checker and its bridge in `internal/sketch/proto`, the front end the commands share in `internal/sketchfront`, the task runtime in `runtime/rt`, the CLI in `cmd/ergo`, and the specification in `docs/spec`.
- [#205](https://github.com/cevr/ergo/issues/205) removed the earlier `.ef` language ([record](docs/receipts/retire-ef.md)). Its documents stay as design reference: [implemented syntax](docs/prototype.md), [testing](docs/testing.md) and [bundled modules](docs/bundled-modules.md).
