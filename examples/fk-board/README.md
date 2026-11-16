# fk-board

A task board for an observatory's chores. Cards move between three columns by pointer or by keyboard, a screen reader says what happens, and a form adds cards. The board is kept between runs. It is a port of Foldkit's `kanban` example (`conformance/upstream/foldkit-d21db423/examples/kanban`, MIT) and of the model of its drag-and-drop component (`packages/ui/src/dragAndDrop`). This is stage a of FK11: the model, with no view yet. The program plays a script of acts from standard input, shows what each one does, and keeps the board in `board.json` in the working directory, in upstream's saved-board JSON.

Tag: `default`.

```sh
fk-board < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`:

- pointer acts: `press`, `move`, `release`, `escape`;
- keyboard acts: `lift`, the arrows, `next`, `previous`, `drop`;
- form acts: `add`, `type`, `submit`, `cancel`;
- `show`.

## The shape

- Package `sortkey` makes fractional keys. `Between(lo, hi)` is a key strictly between two others, so a move rewrites only the moved card's key. It is a smaller form of the fractional-indexing library upstream uses.
- Package `board` is the domain.
  - `Card` and `Column`.
  - `Reorder`, which removes a card and inserts it with a key between its new neighbours.
  - `Step`, which says where a keyboard move goes. Upstream asks the page for this; here the board answers.
  - The default board.
  - The operation type `Store` (`Load`, `Save`) with the law `LoadGivesTheLastSave`. `ergo test` runs that law on both providers:
    - `memory.Shelf` keeps the board for one run.
    - `file.Sheet` keeps it as JSON through std json's derived codec of the board's records (`Card.Key` has the codec key `sortKey`, as upstream names it), over any `fs.Files` and `fs.Writes`. The binary uses the host's, and the law uses an `fstest.Memory` for each case.
- Package `drag` is the drag machine `Dragger`.
  - Its phases are `Idle | Pending | Dragging | Lifted`. `Lifted` is a keyboard drag.
  - Its output is `[]Effect`: `Reordered` and `Cancelled` are upstream's OutMessage, and `Focus` and `Resolve` are its commands.
  - It knows no board, so it can serve any sortable list (SP1's `examples/kit/drag` when that lands).
- Package `kanban` is the program, the machine `Kanban`.
  - Its state is `Board`: the columns, the drag, the form, and what a screen reader says.
  - For a drag signal, the step runs the drag's step and folds its effects into the board. A `Reordered` reorders the board, says the drop and saves; a `Cancelled` says which card went back; a `Focus` or a `Resolve` becomes a command for the host.
  - Upstream does the same with `Update.foldChild` and `foldOutMessage`.
- Package `script` is the host of stage a. It answers each command: an id `new-N`, a save to the `board.Store` in the context, a focus, or where a keyboard move goes.
- `cmd/fk-board` binds `file.NewSheet` on the host's file system. It is the only place that chooses the store.

How the port differs from upstream:

- **A child machine instead of `foldChild`.** The drag's output is a value of the step, and the parent matches it.
- **Clamped positions.** `Insert` clamps a position to the column. Upstream appends a card placed past the end, but gives it a key as if the column were empty, so the order of its keys breaks.
- **The board answers keyboard moves.** Upstream counts DOM nodes for a keyboard move; here `board.Step` reads the columns.
- **No subscriptions yet.** The pointer, Escape, keyboard and auto-scroll subscriptions need the host (FK11b, H5g).

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 15 story tests and 7 scene tests. The examples runner checks it against the vendored files.

No story is an Ergo test, because a law or a case already kills every mutant that a test of the story would kill:

- The 5 form stories are `NewCardFollowsTheForm`, which checks drawn clicks, titles (blank ones too), submits and cancels against a reference of the form.
- The 4 drag stories are `KeyboardDropEqualsPointerDrop`: both paths, to every spot, give the reference board and save once.
- The 2 cancel stories are `CancelRestoresTheBoard`.
- The 4 `Column.reorder` stories are `ReorderIsAPermutation` and `SortKeysStrictlyIncrease`.
- The `tour` case runs each story's acts by script.

The 7 scene tests need a view and are stage b.

## Laws

- `sortkey`: `BetweenIsStrictlyBetween`, over lists grown by drawn inserts.
- `board`:
  - `ReorderIsAPermutation` checks drawn reorders against a reference on lists of ids, including unknown cards, an unknown column and positions past either end.
  - `SortKeysStrictlyIncrease`.
  - `Store.LoadGivesTheLastSave`, on `Shelf` and `Sheet`.
- `drag`: `DragStartsPastTheThreshold`.
- `kanban`: `NewCardFollowsTheForm`, `CancelRestoresTheBoard` and `KeyboardDropEqualsPointerDrop`.
- `file`: `AForeignLineIsNamed`.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`: starts from the default board.
  - Pointer drags: a press with no move, a move under the threshold, a drop within a column and one across, a release over nothing, and an Escape.
  - Keyboard drags: within a column, past either end, across columns past the last one and back, and one given up.
  - The form: a card added, a blank title, and a cancel.
- `persisted`: a saved board with an empty column. A card is moved into that column and another is added.
- `unreadable`: the saved board has a foreign line. The program says which line and starts from the default board.
- `badscript`: names a card the board does not have. The program says which line and exits 1.
