# Testing

`ergo test FILE [--seed N]` builds the tests of one `.ergo` file and runs them. A test is a top-level declaration of the file, beside its functions. The rules are in [grammar.md](spec/grammar.md#declarations) (declarations 11) and [runtime.md](spec/runtime.md); this page shows how to use them.

## Tests

A test has a name and a body. `assert` takes a `bool` expression and stands only in a test or a law body (S108 elsewhere).

```go
func double(n int) int { n + n }

test DoubleOfTwo {
	assert double(2) == 4
}
```

A test with parameters is a property. The runner draws 100 cases from a seed, and the test must hold for each:

```go
test DoubleIsEven(n int) {
	assert double(n) % 2 == 0
}
```

A test can use a context, so it swaps providers with no mock framework. The README's opening program ([examples/checkout](../examples/checkout/cmd/checkout/main.ergo)) tests the same `Checkout` with a provider that pays and one that declines:

```go
test DeclineIsHandled {
	assert with Declining { payments.Purchase(100) } == "declined"
}
```

A test declares no `needs` row (S102): the services it uses come from a `with`.

## Running

```sh
./bin/ergo test examples/checkout/cmd/checkout
```

The runner prints one line per test, in source order, then a summary:

```text
ok ChargeSucceeds
ok DeclineIsHandled
PASS (2 tests)
```

A property prints its case count, `ok DoubleIsEven (100 cases)`. A failed assert prints its position and its text, and a failed comparison also prints its two operands as Ergo literals (`left`, `right`; a line diff for two multi-line strings). A refuted property also prints the case number, the seed, the size (the case number over four, which bounds generated collections and strings; [runtime](spec/runtime.md#tests), Tests 6) and each argument as an Ergo literal of its type, so the case can be pasted into a test:

```text
FAIL DoubleIsSmall (case 3 of 100, seed 1, size 0: n = -6039945012424161084): assert failed at tests.ergo:12:2: double(n) < 10: left 6366854048861229448, right 10
FAIL (1 of 3 tests)
```

`--seed N` (a decimal `uint64`, default 1) selects the cases, so a failure reproduces with the same seed.

## Exit status

| Status | Meaning |
| --- | --- |
| 0 | every test passed |
| 1 | at least one test failed; the other tests still ran |
| 2 | a defect (a Go panic, for example a division by zero) stopped the run |

A defect is not a failed assert. The runner prints the test's `FAIL` line, `FAIL Divides: a defect, which stops the run`, then the runtime's report of every reason with the Ergo frames, and stops: no later test runs.

## Builds

`ergo test` builds a test binary apart from the program's. `ergo build` and `ergo run` contain no Go from a test, so tests add nothing to the program. `-work` prints the directory of the generated Go module on standard error, as `WORK=DIR`.

## Laws

`ergo test` runs every law of the package after its tests ([#162](https://github.com/cevr/ergo/issues/162) law-run-1; [types.md](spec/types.md#laws) rule 10). A top-level law runs as a property does, over 100 generated cases. A member law runs once against each provider of its operation type, with a fresh provider value per case, wherever the provider is declared: the laws that a domain package's `Store` declares run in the `memory` package that provides it, when `ergo test` tests that package (the least package that sees the law and the provider runs it; law-xpkg). A law whose parameter is an operation type runs once per provider of that type too. A helper that `needs` the type runs against that provider:

```text
ok law Store.PutCountsOne for Tally (100 cases)
FAIL law Store.PutCountsOne for Stuck (case 1 of 100, seed 7, size 0: provider = Stuck{at: 3}, n = -9): law refuted at main.ergo:4:2: the law is false
skip law Box.Keeps: its parameter v has no generator: T is not a generatable type
```

A law that cannot run prints a `skip` line with the reason, and the summary counts it: `PASS (2 tests, 1 skipped)`. A provider whose state is a std handle, such as a `sync.Ref`, has no generator; declare its constructor `func NewTally() Tally` (no rows) and each case builds the provider with it; a provider with a constructor is always built by it. The package of an operation type with no provider of its own prints `law Store.PutCountsOne runs where Store has a provider: none is in this package or its imports`. In `examples/`, a skip line that the program's `expected-skips` file does not list fails the gate. A law of a generic operation type is checked and not run yet. So a program need not restate a law as a property test. `ergo run` and `ergo build` contain no Go from a law. The [support table](spec/support.md) lists what each build lowers.

## Example programs

Each program of `examples/` runs its tests and laws in the gate: `go run ./scripts/examples` runs `ergo test <program>/main.ergo` for a program of one folder that declares a test or a law, and `ergo test <folder>` for every package folder of a program of packages, and fails the program when a test fails ([examples/README.md](../examples/README.md)).
