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

A law is checked and is not run: `ergo test` does not run laws yet ([#162](https://github.com/cevr/ergo/issues/162)). A law does not change what `ergo test` prints. The [support table](spec/support.md) lists what each build lowers.

## Example programs

Each program of `examples/` that declares tests runs them in the gate: `go run ./scripts/examples` runs `ergo test <program>/main.ergo` and fails the program when a test fails ([examples/README.md](../examples/README.md)).
