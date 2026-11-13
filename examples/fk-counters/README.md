# fk-counters

A list of counters: add a row, remove one, and click each row's `+` and `-`, which change that row only. It is a port of Foldkit's `counters` example (`conformance/upstream/foldkit-d21db423/examples/counters`, MIT), stage a of FK2: the model, with no view yet. The program runs a script of clicks and shows the list after each one.

Tag: `default`.

## How it works

- Package `counter` is one counter: the pure machine `Counter`, with state `Count{N}` and signal `Increment | Decrement`. It knows nothing of the list.
- Package `rows` is the list: the machine `Counters`. Its state is `Rows{Items []Row, Next int}`, where a `Row` is a key and a `counter.Count`. Its signal is `Add | Remove{Key} | At{Key, Sig counter.Signal}`. The parent's step routes `At` by calling `counter.Step` on that row alone. A pure child needs no actor, because it is function composition. Keys are never reused, so a signal for a removed row finds no row and is ignored.
- Package `script` reads `add`, `remove N`, `inc N` and `dec N` lines, where N is the number of a row named `counter-N`.

Upstream joins the child with `Update.foldChild({update, read, write, toParentMessage})` and a hand-written `GotCounterMessage` variant (`main.ts` 30 to 57). Here the variant is `At{Key, Sig}`, and the join is one `match` arm that steps the matching row through `slices.Transform`. The blocks design's Binding is not used (owner, 2026-11-12).

## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files.

## Laws

- `RowSignalTouchesOnlyItsRow`: a counter's signal steps that row's counter, and every other row and the order of the rows stay as they were.
- `RemoveKeepsTheOthers`: a remove drops the row with the key and keeps the others, in order, with their counts.
- `KeysAreUnique`: no two rows share a key, and every key is below `Next`, so an add never gives a key that a row had before.
- `Counter.UndoesItself` (package `counter`): a decrement undoes an increment, and the reverse.

Each law of `rows` runs over every state that generated adds, removes and clicks reach, including clicks for keys with no row. The mutants these kill are in `testdata/mutants.json`.

## Cases

- `rows`: clicks routed to their rows, an add with the next key, a remove that keeps the others, a click for a removed row, keys not reused after the last row goes, and an empty list.
- `badscript`: a row number that is not a number; the program says which line and exits 1.
