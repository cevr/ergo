# fk-counters

A list of counters: add a row, remove one, and click each row's `+` and `-`, which change that row only. It is a port of Foldkit's `counters` example (`conformance/upstream/foldkit-d21db423/examples/counters`, MIT), stage b of FK2: the model and its view, on std ui's test host. The program runs a script of clicks on the screen and shows the screen after each one.

Tag: `default`.

## How it works

- Package `counter` is one counter: the pure machine `Counter`, with state `Count{N}` and signal `Increment | Decrement`, and its view `View[E](f, c, to)`: `-`, the count, `+`. The view writes into a frame of any signal: `to` makes the parent's signal from the counter's own. The counter knows nothing of the list.
- Package `rows` is the list: the machine `Counters`. Its state is `Rows{Items []Row, Next int}`, where a `Row` is a key and a `counter.Count`. Its signal is `Add | Remove{Key} | At{Key, Sig counter.Signal}`. The parent's step routes `At` by calling `counter.Step` on that row alone. A pure child needs no actor, because it is function composition. Keys are never reused, so a signal for a removed row finds no row and is ignored. `CountersFrom` is the same machine started from given counts (`Given`), as a scene's `given`.
- Package `screen` is the list's view: the heading, then `ui.Each` over the rows keyed by key. Each row is a `Same` keyed by its key and count, holding a group named `counter-N` with `counter.View(f, row, func(s) { At{Key, s} })` and a Remove button. Then comes the button `+ Add Counter`.
- Package `script` plays lines on `ui.NewTestHost`:
  - `given C...` starts again from rows with those counts;
  - `add` clicks the add button;
  - `inc K`, `dec K` and `remove K` click `+`, `-` or Remove within the group `counter-K` (`ui.Within`);
  - `press LABEL` clicks the one button with that name, and `press first LABEL` the first one (`ui.First`);
  - `stats` prints the work of the click before it (`ui.Counts`).

  After each line it prints the screen in screen order: texts, `[button]`s, and `counter-N{...}` for each row. A click whose locator finds no node, or more than one (NS7's `Ambiguous`), clicks nothing and says so.

Upstream joins the child with `Update.foldChild({update, read, write, toParentMessage})` and a hand-written `GotCounterMessage` variant (`main.ts` 30 to 57). Its view embeds the child with `h.submodel({view, toParentMessage})`. Here the variant is `At{Key, Sig}`, and the join is one `match` arm that steps the matching row through `slices.Transform`. The view's join is the function argument `to`: a `ui.Frame[rows.Signal]` is passed to the child's view as it is, with no frame of the child's signal and no mapping of one frame into another. The blocks design's Binding is not used (owner, 2026-11-12).

## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files. The five scene tests are the case `scenes`. Upstream's `nth(all.role('button', {name: '+'}), 1)` becomes the `+` within the row's group, because std ui has `First` and `Within` but no `Nth`.

## Laws

- `RowSignalTouchesOnlyItsRow`: a counter's signal steps that row's counter, and every other row and the order of the rows stay as they were.
- `RemoveKeepsTheOthers`: a remove drops the row with the key and keeps the others, in order, with their counts.
- `KeysAreUnique`: no two rows share a key, and every key is below `Next`, so an add never gives a key that a row had before.
- `Counter.UndoesItself` (package `counter`): a decrement undoes an increment, and the reverse.

Each law of `rows` runs over every state that generated adds, removes and clicks reach, including clicks for keys with no row. The mutants these kill are in `testdata/mutants.json`. The view's mutants (a row's signal sent to another row, a `-` that sends `Increment`, a `Same` key that leaves out the count) are killed by the cases.

## Cases

- `rows`: clicks routed to their rows, an add with the next key, a remove that keeps the others, a removed row with no buttons left, keys not reused after the last row goes, and an empty list.
- `scenes`: upstream's five scene tests, one `given` each, and a bare `+` among two rows that is ambiguous.
- `badscript`: a row number that is not a number; the program says which line and exits 1.
