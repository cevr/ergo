# task-board and its original

What `examples/task-board` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `kanban` example (`conformance/upstream/foldkit-d21db423/examples/kanban`, MIT) and of the model of its drag-and-drop component (`packages/ui/src/dragAndDrop`).
How the port differs from upstream:

- **A child machine instead of `foldChild`.** The drag's output is a value of the step, and the parent matches it.
- **Clamped positions.** `Insert` clamps a position to the column. Upstream appends a card placed past the end, but gives it a key as if the column were empty, so the order of its keys breaks.
- **The board answers keyboard moves.** Upstream counts DOM nodes for a keyboard move; here `board.Step` reads the columns.
- **Zones instead of subscriptions.** Upstream subscribes to the document's pointer and key events and asks the DOM which item is under the pointer. Here the Area takes the pointer, the zone path says what is under it, and `OnKey` takes the keys. Auto-scroll at the edges waits for a host that scrolls (stage c).
- **The live region is read from the screen.** `ui.Announced` misses what the last pass of `Settle` says (gap FK11-g5), so the script reads the live region's node.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 15 story tests and 7 scene tests. The examples runner checks it against the vendored files.

No story is an Ergo test, because a law or a case already kills every mutant that a test of the story would kill:

- The 5 form stories are `NewCardFollowsTheForm`, which checks drawn clicks, titles (blank ones too), submits and cancels against a reference of the form.
- The 4 drag stories are `KeyboardDropEqualsPointerDrop`: both paths, to every spot, give the reference board and save once.
- The 2 cancel stories are `CancelRestoresTheBoard`.
- The 4 `Column.reorder` stories are `ReorderIsAPermutation` and `SortKeysStrictlyIncrease`.
- The `tour` case runs each story's acts by script.

The 7 scene tests are the case `scenes`, on upstream's test board: the first screen with each column's name, count and cards; the form opened in To Do, with a typed title in its input; the card added after a submit; and the add button back after a cancel. Upstream finds a column by its heading and the form within it. std ui has no heading role and no `within` locator, so the case prints the screen and shows each node in its column's zone.

## Comparisons moved from the README

- Upstream asks the page for this; here the board answers.
- Upstream does the same with `Update.foldChild` and `foldOutMessage`.
