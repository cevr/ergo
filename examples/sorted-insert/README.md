# sorted-insert

Inserts its arguments one by one into an ascending list and prints the list after each insert.

```sh
ergo run examples/sorted-insert/cmd/sorted-insert 5 1 3
# 5
# 1 5
# 1 3 5
```

The point is the property in `ordered/tests.ergo`. `InsertKeepsOrder` draws its data in the body with std `gen`: `sortedInts` is a plain function that `needs gen.Draws`, and `gen.Check` runs the body over 100 seeded cases. When a case fails, `gen.Check` shrinks it on its tape of draws and reports the seed, each named draw (`xs = …`, `x = …`), the draws it was shrunk from, and a `gen.Replay(gen.Tape{…}, body)` call that replays the shrunk case. With the ledger's planted mutant (`testdata/mutants.json`, `insert-before-equal-or-one-less`: x goes after an element one larger), `ergo test examples/sorted-insert/ordered` fails with this report in the assert's diff:

```text
refuted (case 32 of 100, seed 1, size 7; 377 shrinks):
	xs = []int{6}
	x = 5
	shrunk from: xs = []int{9, 12, 17, 19, 22, 26, 34, 41, 48, 54, 61, 61}, x = 18
	replay: gen.Replay(gen.Tape{Size: 7, Draws: []uint64{1, 6, 0, 9}}, body)
```

The mutant fails only when x is one below an element, so the shrink stops at a local minimum: lowering either draw alone passes. `gen` promises a failing, never larger case at a fixpoint of its passes, not a global minimum.
