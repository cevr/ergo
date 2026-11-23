# Ergo

**Failures handled. Services supplied. Laws tested. ∴ ship it.**

What Go leaves to convention, Ergo checks. Ergo reads like Go and compiles to Go. Every failure, every service and every law is written in the signature; the compiler checks them, then erases them, so the program costs what the Go you would write by hand costs.

## The compiler refuses the mistake

<!-- quote site/examples/checked/lookup/lookup.ergo 19-31 -->
```go
func Lookup(name string) Answer fails NotFound | timeout.Expired needs Resolver | Scheduler {
	timeout.After(2s) { Resolver.Resolve(name) }
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

Forget the timeout, or call a service the signature does not name, and the program does not build:

<!-- shows site/examples/refused/no-timeout site/examples/refused/no-resolver -->
```console
$ ergo diagnostics lookup.ergo        # case timeout.Expired deleted
lookup.ergo:24:28: error S103: this catch does not handle {timeout.Expired}; a catch without try handles every error of the row (rows.md, Catch 4)
$ ergo diagnostics lookup.ergo        # needs Resolver deleted
lookup.ergo:20:2: error S102: this call requires Resolver, which is not in the capability row of function Lookup and no with provides it; write: func Lookup(name string) Answer fails NotFound | timeout.Expired needs Scheduler | Resolver
```

A `match` names every case, and a law runs on generated cases when the file is checked or tested:

<!-- quote site/examples/checked/readme/counter/counter.ergo -->
```go
type Op = Inc{} | Dec{} | Reset{}

// Step moves a count. Dec stops at zero.
func Step(count int, op Op) int {
	match op {
	case Inc:
		count + 1
	case Dec:
		if count > 0 { count - 1 } else { 0 }
	case Reset:
		0
	}
}

// A count is 0 to 1000.
type Count int where 0 <= it && it <= 1000

// No step takes a count below zero.
law NeverNegative(count Count, op Op) {
	Step(int(count), op) >= 0
}
```

<!-- shows site/examples/refused/no-reset site/examples/refuted/dec-below-zero -->
```console
$ ergo diagnostics counter.ergo       # case Reset deleted
counter.ergo:5:2: error S103: this match is not total: no case handles Reset; name every variant or add case _ (rows.md, Totality 2)
$ ergo test counter.ergo              # Dec written as count - 1
ok law CountWhere (100 cases) (evaluated)
FAIL law NeverNegative (case 23 of 100, seed 1, size 5: count = Count(0), op = Dec): law refuted at counter.ergo:20:2: the law is false (evaluated)
	law declared at counter.ergo:19:1
	replay: ergo test counter.ergo --law NeverNegative --seed 1
FAIL (1 of 2 tests)
```

A file's text is read in place, with no copy, and it cannot be read after the file is closed:

<!-- shows site/examples/refused/read-after-close -->
```console
$ ergo diagnostics lines.ergo         # file.Close() before the loop
lines.ergo:9:18: error S108: text is a text of file, which was handed off at 7:2 (Close takes it): a text is a borrow of its Mapped, so a use of text is a use of file (types.md, Borrows 8); use text before file is handed off, or copy it with fs.Clone
lines.ergo:10:6: error S108: text is a text of file, which was handed off at 7:2 (Close takes it): a text is a borrow of its Mapped, so a use of text is a use of file (types.md, Borrows 8); use text before file is handed off, or copy it with fs.Clone
```

## At the cost of hand-written Go

Instructions per unit of work, Ergo's against Go written by hand with the same algorithm, counted rather than timed, so the numbers hold on a loaded machine. Each row is reproduced by `go run ./scripts/opcount -control WORKLOAD`, and its receipt is in [site/receipts](site/receipts).

<!-- receipts -->
| Workload | What it does | Ergo ÷ hand Go | Held by |
| --- | --- | --- | --- |
| `station-stats` | min, mean and max per station over rows of `station;temperature` | **0.91** | `TestStationStatsCountsAsHandGo` (fails above 0.95) |
| `lru` | a least-recently-used cache | **0.97** | `TestLRUCountsAsHandGo` (fails above 1.00) |

## Go is the platform

Ergo compiles to Go: Go's runtime, Go's garbage collector, one Go binary. A Go module stays a Go module: `go.mod` and `go.sum` sit beside `ergo.mod`, `ergo get` runs `go get`, and `import go` makes a Go package's exported declarations callable, its errors arriving as failures the signature must list. [admit](examples/admit) uses `golang.org/x/sync/semaphore`:

<!-- quote examples/admit/semaphore/semaphore.ergo 8,44-49 -->
```go
import go golang.org/x/sync/semaphore
…
func Wait(g Gate, n int) fails jobs.Stopped needs go.Foreign {
	g.w.Acquire(int64(n)) catch {
	case go.Error:
		return jobs.Stopped
	}
}
```

`ergo bind go golang.org/x/sync/semaphore` writes the binding the checker reads. Drop the `catch`, and Go's error is caught at build time:

<!-- shows site/examples/refused/go-error -->
```console
$ ergo diagnostics semaphore
semaphore/semaphore.ergo:45:2: error S101: {go.Error} escapes function Wait, whose error row {jobs.Stopped} does not list it; write: func Wait(g Gate, n int) fails jobs.Stopped | go.Error needs go.Foreign
```

## TypeScript reads Ergo types

A TypeScript file imports an `.ergo` file. TypeScript 7.1's content mappers (7.1 is in beta) run `ergo tsmap`, which hands the compiler the checked contract as Effect schemas, in memory: no generated file in the tree. [limiter](examples/limiter) declares its contract once, in Ergo:

<!-- quote examples/limiter/limits/limits.ergo 18,28-29,45 -->
```go
type BadCost { Cost int, Capacity int }
…
type Limiter {
	Take(key string, cost int) Grant fails Limited | UnknownBucket | BadCost,
	…
}
```

and its TypeScript gateway calls it through a stock Effect `RpcClient`:

<!-- quote examples/limiter/ts/gateway.ts 11-13,47< -->
```ts
import { Limiter } from "../limits/limits.ergo"

const makeClient = RpcClient.make(Limiter)
…
Effect.catchTag("BadCost", e => Effect.succeed(text(`a take costs 1 to ${e.capacity}, not ${e.cost}`, 400))),
```

Rename `Capacity` to `Max` in the Ergo file, and `tsc` fails at the TypeScript line:

<!-- case examples/limiter/cases/tsc-drift -->
```console
$ tsc --runExternalCode -p ts
ts/gateway.ts(47,84): error TS2339: Property 'capacity' does not exist on type 'BadCost'.
ts/gateway.ts(60,84): error TS2339: Property 'capacity' does not exist on type 'BadCost'.
```

## Look at some examples

- [station-stats](examples/station-stats): min, mean and max per station over a file of `station;temperature` rows, sized for a billion of them. Laws on `Merge` let the chunks be summed in any order; the file is mapped, never copied.
- [limiter](examples/limiter): a rate limiter served over Effect RPC to a TypeScript gateway; its laws run against the memory provider and again through the wire.
- [gointerop](examples/gointerop): `sum` and `check` print what `sha256sum` and `sha256sum -c` print, on Go's `crypto/sha256`.

[All examples](examples/README.md): servers, CLIs, a TUI, GUIs and games.

## Try it

You need Go 1.27 and Git.

```sh
git clone --depth 1 https://github.com/cevr/ergo.git && cd ergo
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/lookup/cmd/lookup   # prints: 192.0.2.1
```

## Learn more

[Why Ergo](docs/why.md) · [Specification](docs/spec/README.md) · [Tooling](docs/tooling.md) · [Examples](examples/README.md) · [North star](NORTH_STAR.md) · [Prior art](PRIOR_ART.md) · [Glossary](GLOSSARY.md)
