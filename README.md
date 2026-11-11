# Ergo

Ergo reads like Go and compiles to plain Go. What Go leaves to convention, Ergo writes in the signature: every expected failure, every service a function needs, every cleanup, every task and every rule that must always hold. The compiler checks all of it, then erases it, so the Go it emits is the Go you would write by hand.

```go
import timeout

type NotFound { Name string }
type Answer { Name string, Addr string }

// An operation type: the service a caller must supply, with its contract.
type Resolver {
	Resolve(name string) Answer fails NotFound,

	// A lookup that succeeds answers the name asked, with an address.
	law Answered(name string) {
		let answer = Resolve(name) catch {
		case NotFound:
			return true
		}
		Answered(answer, name)
	},
}

// Answered reports whether answer is for name and has an address: the
// law's check, which a provider's test calls.
func Answered(answer Answer, name string) bool {
	answer.Name == name && answer.Addr != ""
}

// A signature lists every expected failure and every service the caller must supply.
func Lookup(name string) Answer fails NotFound | timeout.Expired needs Resolver | Scheduler {
	timeout.After(2s) { Resolver.Resolve(name) } // the block runs as a child task; at 2s it is stopped
}

func Address(name string) string needs Resolver | Scheduler {
	let answer = Lookup(name) catch {
	case NotFound:
		Answer{Name: name, Addr: "unknown"}
	case timeout.Expired:
		Answer{Name: name, Addr: "timed-out"}
	}
	answer.Addr
}
```

The binary binds the contract to providers in a context. Package `memory` holds two of them, `Static`, which answers, and `Empty`, and its context `Listing` binds `hosts.Resolver` to `Static`:

```go
import lookup/hosts
import lookup/memory
import time

// This binary is the only place that chooses the providers. Memory binds
// the memory providers and a time.Manual, so a test needs no host; main
// replaces the Manual with the host's scheduler, which needs Ambient.
let Memory = context { Scheduler: time.NewManual(0), memory.Listing }
let Missing = context { Memory, replace hosts.Resolver: memory.Empty }

func main() needs Console | Ambient {
	let live = context { Memory, replace Scheduler: NewLiveScheduler() }
	Console.Println(with live { hosts.Address("example.org") })
}

test ResolveAnswers {
	let answer = try with Memory { hosts.Lookup("example.org") }
	assert answer.Name == "example.org"
}

test NotFoundIsHandled {
	assert with Missing { hosts.Address("example.org") } == "unknown"
}
```

From the signature alone you know how `Lookup` can fail and what it needs. `Address` catches both failures, so its own signature has no `fails`. The law `Answered` states what every provider of `Resolver` must keep. `Memory` embeds the context of another package, and `Missing` keeps every provider of `Memory` and replaces one, so the tests swap providers without a mock framework. Only `main` holds `Ambient`, the authority to choose the host, so only `main` binds the host's scheduler. In Go, `try` becomes `if err != nil`, and the rows disappear. This program is [examples/lookup](examples/lookup/cmd/lookup/main.ergo): `ergo run` prints `192.0.2.1`, and `ergo test` runs both tests and the law `Answered` under each of the contexts `Memory` and `Missing` (laws under a context, L5).

## Designed, not built yet

The program below is the language as the owner designed it on 2026-11-03. It is a fragment, and it does not build yet: the `testing` package with its virtual clock, context constructors ([#37](https://github.com/cevr/ergo/issues/37)) and resources ([#197](https://github.com/cevr/ergo/issues/197)) are not built.

```go
import testing
import timeout

// A signature lists every expected failure and every service the caller must supply.
func Fetch(url URL, size int64) Reply fails Refused | Busy | timeout.Expired needs Pool | Transport | Scheduler {
	return timeout.After(2s) { // the block runs as a child task; at 2s it is stopped and drained
		let conn = try Pool.Acquire(url.Host) // a resource: released on every exit, the timeout included
		Transport.Send(conn, size)            // lends conn for the call
	}
}

// A law lives in its operation type. It must hold for every Transport provider, and ergo test checks it like a fuzz target.
type Transport {
	Send(conn Conn, size int64) Reply fails Refused,
	Unread(r Reply) int64,

	law UnreadNeverExceedsSize(r Reply) { Unread(r) <= r.Size },
}

// A test with replaced providers and virtual time: no sleeps and no mock framework.
let SlowWorld = context {
	Pool:      MemoryPool{},
	Transport: NewSlowTransport,       // a constructor: it answers after 5s on this context's Scheduler
	Scheduler: testing.VirtualClock{}, // jumps to the next timer when every task waits
}

test SlowTransportTimesOut {
	let r = with SlowWorld { Fetch(URL{Host: "example.org", Path: "/"}, 100) } catch {
	case timeout.Expired:
		Reply{Status: "timed-out", Size: 0} // reached at once, in virtual time
	case Refused, Busy:
		Reply{Status: "failed", Size: 0}
	}
	assert r.Status == "timed-out"
}
```

From the signature alone you know how `Fetch` can fail and what it needs. The connection cannot leak. The timeout stops its block and waits for cleanup. The law holds for every transport provider, not only this one. The test swaps providers and the clock without a mock framework. In Go, `try` becomes `if err != nil`, and the rows disappear.

## Tasks that cannot leak

Every task belongs to a scope. The scope waits for its tasks, stops them on a timeout, and keeps every cause when several fail.

```go
func Addresses(names []string) []Answer fails NotFound | timeout.Expired needs Resolver | Scheduler {
	return timeout.After(2s) {
		tasks.Transform(names, 8, func(name string) Answer fails NotFound needs Resolver {
			return Resolver.Resolve(name)
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

`ergo inspect machine` draws the graph from the cases. A machine also carries laws: a law about which states can follow which is proved on that graph, and a law about payloads is tested.

## Laws

A law says what must always hold. It lives in the type it constrains and applies to every provider of that type.

```go
type EntryCodec {
	Encode(e Entry) string,
	Decode(s string) Entry fails DecodeFailed,

	law RoundTrip(e Entry) { try Decode(Encode(e)) == e },
}

type Store {
	Find(id string) Entry fails NotFound,
	Put(id string, e Entry),

	law FindAfterPut(id string, e Entry) { Put(id, e); try Find(id) == e },
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

With tracing off, a program makes no extra allocations ([measured](runtime/rt/MEASURED.md#tracing-off-and-on-188)).

## Built for agents

An agent reads the same checked model as the compiler. A signature tells it what a call can fail with and what it needs, so it can change a function without reading the whole call graph. Every diagnostic has a code, a position and a rule:

```console
$ ergo diagnostics fetch.ergo
fetch.ergo:19:9: error S102: this call requires Transport, which is not in the capability row of function fetch and no with provides it
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

Ergo is a prototype. The opening program builds, runs and passes its tests. The sections after it show the designed language, and parts of them are not built. The parser and `ergo fmt` accept all of the designed language except refinements and laws inside machines, which were decided on 2026-11-03. `ergo check` checks a v1 program ([#196](https://github.com/cevr/ergo/issues/196)): one file, or a package folder of one or more files with the local packages it imports (a folder is a package, and `ergo.mod` marks the module root). It checks types, unions and `match`, error and needs rows, contexts, generics, tasks, machines, tests and laws. `ergo build`, `ergo run` and `ergo test` lower every construct that the checker accepts, except the few that the [support table](docs/spec/support.md) lists with their owners. `ergo test` runs tests and property tests ([testing](docs/testing.md)). When a source checks, `ergo check` also runs its pure tests and laws, machine laws included, in the checker's process with no Go build, gives the same verdicts as `ergo test` for the same seed, and lists each one it skips (a capability, a task, Go code) with the reason ([check-time evaluation](docs/testing.md#check-time-evaluation); `examples/pure-eval`, `examples/router-model`). `ergo test` runs laws too: each top-level law, and each member law against every provider the package declares ([#162](https://github.com/cevr/ergo/issues/162) law-run-1); a law of a generic operation type is checked and not run yet. A pure function compiles to a plain Go function with no hidden argument. The task runtime `runtime/rt` runs scopes, stops and traces. It keeps every defect, but not yet every typed failure: a child's failure that nobody waited for is dropped when its scope ends normally ([runtime](docs/spec/runtime.md)). Not built yet: the `tasks` package, resource tracking ([#197](https://github.com/cevr/ergo/issues/197)), virtual time, `import go` ([#202](https://github.com/cevr/ergo/issues/202)), laws inside machines, refinements, and `ergo inspect` (apart from `ergo inspect machine`) and MCP on the new front end ([#203](https://github.com/cevr/ergo/issues/203)).

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

./bin/ergo run examples/lookup/cmd/lookup          # prints: 192.0.2.1
./bin/ergo test examples/lookup/cmd/lookup         # prints: PASS (4 tests)
./bin/ergo test examples/lookup/...                # tests each package, dependencies first
./bin/ergo run examples/editions/sketch/main.ergo  # prints: hello, Ada / no friend named Bob
./bin/ergo fmt --check examples/editions/sketch/main.ergo
go run ./scripts/examples                          # builds and runs the example programs against their recorded cases
./scripts/gate.sh                                  # the merge gate: formatting, vet, tests and CLI checks
```

A source is one `.ergo` file, with no manifest. The parser is in `internal/sketch/syntax`, the checker in `internal/sketch/check`, lowering in `internal/sketch/lower`, the task runtime in `runtime/rt` and the CLI in `cmd/ergo`.
