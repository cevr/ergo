# sorted-insert

Inserts its arguments one by one into an ascending list and prints the list after each insert.

```sh
ergo run examples/sorted-insert/cmd/sorted-insert 5 1 3
# 5
# 1 5
# 1 3 5
```

The point is the property in `ordered/tests.ergo`. `InsertKeepsOrder` draws its data in the body with std `gen`: `sortedInts` is a plain function that `needs gen.Draws`, and `gen.Check` runs the body over 100 seeded cases. When a case fails, `gen.Check` shrinks it on its tape of draws and reports the seed, each named draw (`xs = …`, `x = …`), the draws it was shrunk from, and a `gen.Replay(gen.Tape{…}, body)` call that replays the shrunk case. With the ledger's planted mutant (`testdata/mutants.json`, `insert-before-equal-or-one-less`), the failure reads:

```text
refuted (case K of 100, seed 1, size N; S shrinks):
	xs = []int{0}
	x = -1
	shrunk from: xs = …, x = …
	replay: gen.Replay(gen.Tape{Size: N, Draws: []uint64{…}}, body)
```
