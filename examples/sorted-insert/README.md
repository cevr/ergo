# sorted-insert

Inserts its arguments one by one into an ascending list and prints the list after each insert.

```sh
ergo run examples/sorted-insert/cmd/sorted-insert 5 1 3
# 5
# 1 5
# 1 3 5
```

The point is the property in `ordered/tests.ergo`. `InsertKeepsOrder` is a plain test whose body draws its data with std `gen`: `sortedInts` is a plain function that `needs gen.Draws`, and the test asserts on what it drew. `gen.Draws` declares the test harness `test Property` (runtime.md, Tests: harnesses), so `ergo test` runs the body under `gen.Property`: 100 cases seeded by the run's `--seed` and the test's name. When a case fails, `gen.Property` shrinks it on its tape of draws, and the test fails with the failed assert, the seed, each named draw (`xs = …`, `x = …`), the draws it was shrunk from, a `gen.Replay(gen.Tape{…}, body)` call, the test's site and the `ergo test` command that replays the run. With the ledger's planted mutant (`testdata/mutants.json`, `insert-before-equal-or-one-less`: x goes after an element one larger), `ergo test examples/sorted-insert/ordered` fails with:

```text
FAIL InsertKeepsOrder: a failure escaped the test at tests.ergo:32:1: Failed{Message:assert failed at tests.ergo:36:2: IsSorted(got)
refuted (case 22 of 100, seed 1, size 5; 340 shrinks):
	xs = []int{1}
	x = 0
	shrunk from: xs = []int{7, 12, 21, 25, 25, 34, 41, 47, 56, 60, 64, 73}, x = 11
	replay: gen.Replay(gen.Tape{Size: 5, Draws: []uint64{1, 1, 0, 0}}, body)
	at tests.ergo:32
	replay: ergo test --run InsertKeepsOrder --seed 1}
```

`--seed 7` draws another stream (case 10, shrunk from `xs = []int{1, 10, 15, 15, 22}, x = 9`) to the same `xs = []int{1}, x = 0`.

The mutant fails only when x is one below an element, so the shrink stops at a local minimum: lowering either draw alone passes. `gen` promises a failing, never larger case at a fixpoint of its passes, not a global minimum.
