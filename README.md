# Ergo

Ergo reads like Go and compiles to plain Go. What Go leaves to convention, Ergo writes in the signature: every expected failure, every service a function needs, every cleanup, every task and every rule that must always hold. The compiler checks all of it, then erases it, so the Go it emits is the Go you would write by hand.

```go
import timeout

type Declined { Reason string }
type Receipt { ID string, Amount int64 }

// An operation type: the service a caller must supply, with its contract.
type Payments {
	Charge(amount int64) Receipt fails Declined,

	// A charge that succeeds is receipted, for the amount charged.
	law Receipted(amount int64) {
		let receipt = Charge(amount) catch {
		case Declined:
			return true
		}
		Receipted(receipt, amount)
	},
}

// Receipted reports whether receipt has an ID and the amount: the law's
// check, which a provider's test calls.
func Receipted(receipt Receipt, amount int64) bool {
	receipt.ID != "" && receipt.Amount == amount
}

// A signature lists every expected failure and every service the caller must supply.
func Checkout(amount int64) Receipt fails Declined | timeout.Expired needs Payments | Scheduler {
	timeout.After(2s) { Payments.Charge(amount) } // the block runs as a child task; at 2s it is stopped
}

func Purchase(amount int64) string needs Payments | Scheduler {
	let receipt = Checkout(amount) catch {
	case Declined:
		Receipt{ID: "declined"}
	case timeout.Expired:
		Receipt{ID: "timed-out"}
	}
	receipt.ID
}
```

The binary binds the contract to providers in a context. Package `memory` holds two of them, `Gateway`, which pays, and `Decliner`, and its context `Paying` binds `payments.Payments` to `Gateway`:

```go
import checkout/memory
import checkout/payments
import time

// This binary is the only place that chooses the providers. Memory binds
// the memory providers and a time.Manual, so a test needs no host; main
// replaces the Manual with the host's scheduler, which needs Ambient.
let Memory = context { Scheduler: time.NewManual(0), memory.Paying }
let Declining = context { Memory, replace payments.Payments: memory.Decliner }

func main() needs Console | Ambient {
	let live = context { Memory, replace Scheduler: NewLiveScheduler() }
	Console.Println(with live { payments.Purchase(100) })
}

test ChargeSucceeds {
	let receipt = try with Memory { payments.Checkout(100) }
	assert receipt.Amount == 100
}

test DeclineIsHandled {
	assert with Declining { payments.Purchase(100) } == "declined"
}
```

From the signature alone you know how `Checkout` can fail and what it needs. `Purchase` catches both failures, so its own signature has no `fails`. The law `Receipted` states what every provider of `Payments` must keep. `Memory` embeds the context of another package, and `Declining` keeps every provider of `Memory` and replaces one, so the tests swap providers without a mock framework. Only `main` holds `Ambient`, the authority to choose the host, so only `main` binds the host's scheduler. In Go, `try` becomes `if err != nil`, and the rows disappear. This program is [examples/checkout](examples/checkout/cmd/checkout/main.ergo): `ergo run` prints `paid`, and `ergo test` runs both tests.

## Designed, not built yet

The program below is the language as the owner designed it on 2026-11-03. It is a fragment, and it does not build yet: the `testing` package with its virtual clock, context constructors ([#37](https://github.com/cevr/ergo/issues/37)) and resources ([#197](https://github.com/cevr/ergo/issues/197)) are not built, and a law is checked but not run ([#162](https://github.com/cevr/ergo/issues/162)).

```go
import testing
import timeout

// A signature lists every expected failure and every service the caller must supply.
func Charge(card Card, amount int64) Receipt fails Declined | Frozen | timeout.Expired needs Accounts | Payments | Scheduler {
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

From the signature alone you know how `Charge` can fail and what it needs. The lock cannot leak. The timeout stops its block and waits for cleanup. The law holds for every payment provider, not only this one. The test swaps providers and the clock without a mock framework. In Go, `try` becomes `if err != nil`, and the rows disappear.

## Tasks that cannot leak

Every task belongs to a scope. The scope waits for its tasks, stops them on a timeout, and keeps every cause when several fail.

```go
func Prices(ids []string) []Price fails NotFound | timeout.Expired needs Catalog | Scheduler {
	return timeout.After(2s) {
		tasks.Map(ids, 8, func(id string) Price fails NotFound needs Catalog {
			return Catalog.Price(id)
		})
	}
}
```

At most 8 lookups run at once, and the results keep input order. The first failure stops the rest. A stop is never cut short by cleanup: releases run to the end.

## Machines

A state machine is a function from a state and a signal to a decision. The `match` must cover every pair, so a forgotten transition does not compile.

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
```

`ergo inspect` draws the graph from the cases. A machine also carries laws: a law about which states can follow which is proved on that graph, and a law about payloads is tested.

## Laws

A law says what must always hold. It lives in the type it constrains and applies to every provider of that type.

```go
type OrderCodec {
	Encode(o Order) string,
	Decode(s string) Order fails DecodeFailed,

	law RoundTrip(o Order) { try Decode(Encode(o)) == o },
}

type Store {
	Find(id string) Order fails NotFound,
	Put(id string, o Order),

	law FindAfterPut(id string, o Order) { Put(id, o); try Find(id) == o },
}
```

`ergo test` runs each law with generated inputs and shrinks a failure to its smallest case. A passing law is reported as tested, never as proved. A refinement such as `type Port int where 1 <= it && it <= 65535` gives a type its law, its parser and its test generator in one line.

## You can see what happened

The compiler knows every task, every service call and every failure, so it names the trace events itself. The source has no span calls. These are recorded lines from a prototype run, with whole lines omitted ([full run](docs/previews/2026-10-27-traceability.md)). A 100 ms timeout interrupted a 2 s sleep, so the gateway call after it never ran:

```text
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

With tracing off, a program makes no extra allocations ([receipt](runtime/rt/RECEIPT.md#tracing-off-and-on-188)).

## Built for agents

An agent reads the same checked model as the compiler. A signature tells it what a call can fail with and what it needs, so it can change a function without reading the whole call graph. Every diagnostic has a code, a position and a rule:

```console
$ ergo diagnostics pay.ergo
pay.ergo:19:9: error S102: this call requires main.Gateway, which is not in the capability row of function pay and no with provides it
```

One formatter gives one layout, so edits make small, stable diffs. `ergo inspect` is designed to answer questions about a definition (its failures, its needs, its calls and a machine's graph) as JSON, and MCP will serve the same model.

## Go is one import away

```go
import go strconv

func parsePort(text string) int64 fails go.Error needs go.Foreign {
	return strconv.ParseInt(text, 10, 64)
}
```

A Go `(T, error)` becomes `T fails go.Error`, and a Go panic becomes a defect. Every call into Go is charged to `go.Foreign`, so the signature says where foreign code runs. The emitted Go carries `//line` directives, so panics, pprof, delve and the race detector point at Ergo lines.

## Small on purpose

Ergo keeps Go's spelling, runtime, garbage collector and single binaries. It removes what makes Go code hard to trust: there is no `nil` (absence is `Option[T]`), no `goto`, no `fallthrough`, no type assertion, no package-level mutable state and no `init`. A panic is a defect, kept apart from expected failures. A construct the compiler must know gets a keyword, and a dot is always a library.

## Status

Ergo is a prototype. The opening program builds, runs and passes its tests. The sections after it show the designed language, and parts of them are not built. The parser and `ergo fmt` accept all of the designed language except refinements and laws inside machines, which were decided on 2026-11-03. `ergo check` checks one package of v1 ([#196](https://github.com/cevr/ergo/issues/196)): types, unions and `match`, error and needs rows, contexts, generics, tasks, machines, tests and laws. `ergo build`, `ergo run` and `ergo test` lower every construct that the checker accepts, except the few that the [support table](docs/spec/support.md) lists with their owners. `ergo test` runs tests and property tests ([testing](docs/testing.md)). Laws are checked, not run; running them is [#162](https://github.com/cevr/ergo/issues/162). A pure function compiles to a plain Go function with no hidden argument. The task runtime `runtime/rt` runs scopes, stops and traces. It keeps every defect, but not yet every typed failure: a child's failure that nobody waited for is dropped when its scope ends normally ([runtime](docs/spec/runtime.md)). Not built yet: the `tasks` package, packages of several files (196b), resource tracking ([#197](https://github.com/cevr/ergo/issues/197)), virtual time, `import go` ([#202](https://github.com/cevr/ergo/issues/202)), laws inside machines, refinements, and `ergo inspect` and MCP on the new front end ([#203](https://github.com/cevr/ergo/issues/203)).

## Learn more

- [Specification](docs/spec/README.md)
- [North star](NORTH_STAR.md): the pillars and how ties are broken
- [Prior art](PRIOR_ART.md): Go, Effect, Gleam, ReScript, Elixir, MoonBit and others
- [Glossary](GLOSSARY.md)
- [Roadmap](https://github.com/cevr/ergo/issues/1)

## Working in this repo

You need Go 1.27 or later and Git.

```sh
git clone https://github.com/cevr/ergo.git
cd ergo
go build -o bin/ergo ./cmd/ergo

./bin/ergo run examples/checkout/cmd/checkout      # prints: paid
./bin/ergo test examples/checkout/cmd/checkout     # prints: PASS (2 tests)
./bin/ergo run examples/editions/sketch/main.ergo  # prints: hello, Ada / no friend named Bob
./bin/ergo fmt --check examples/editions/sketch/main.ergo
go run ./scripts/examples                          # builds and runs the example programs against their recorded cases
./scripts/gate.sh                                  # the merge gate: formatting, vet, tests and CLI checks
```

A source is one `.ergo` file, with no manifest. The parser is in `internal/sketch/syntax`, the checker in `internal/sketch/check`, lowering in `internal/sketch/lower`, the task runtime in `runtime/rt` and the CLI in `cmd/ergo`.
