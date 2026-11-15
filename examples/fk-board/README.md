# fk-board

A task board for an observatory's chores. Cards move between three columns by pointer or by keyboard, a screen reader says what happens, and a form adds cards. The board is kept between runs. It is a port of Foldkit's `kanban` example (`conformance/upstream/foldkit-d21db423/examples/kanban`, MIT) and of the model of its drag-and-drop component (`packages/ui/src/dragAndDrop`). This is stage b of FK11: the model and its screen, on std ui's test host. The program plays a script of pointer, keyboard and form acts on the screen from standard input, shows what each one does, and keeps the board in `board.json` in the working directory, in upstream's saved-board JSON. The desktop host is stage c.

Tag: `default`.

```sh
fk-board < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`:

- pointer acts: `press`, `move`, `release`, `escape`;
- keyboard acts: `lift`, the arrows, `next`, `previous`, `drop`;
- form acts: `add`, `type`, `submit`, `cancel`;
- `show` (the columns as the screen shows them), `screen` (every node of the screen) and `stats` (what the last pass did).

A pointer act names a card or a column, not a point. The test host lays nothing out, so the script lays out the board itself and hit-tests it: column c is 300 pixels wide at x = 300c, and its card i is 40 pixels high at y = 60 + 50i.

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
- Package `screen` is the view, `Screen(f, board)`.
  - One `Area`, "Board", takes the pointer. Each column and each card is a `Zone` keyed by its id (H5g), so a pointer event says which card or column it is over and where in it. Over a card's upper half the drop goes before it, over its lower half after it, and below the cards at the column's end.
  - While a pointer drag is over a spot, the column shows the placeholder "(drop here)" there. The held card shows "(dragging)", and a card lifted by keyboard shows where it would go.
  - Each card is a button named by its title. Pressing it lifts the card for a keyboard drag. Then the arrows move it, Tab and Shift+Tab change its column, Space or Enter drops it, and Escape puts it back. Escape also gives up a pointer drag and shuts the form.
  - Each column has "+ Add card", or the form when it is open there: the input "New card title", Cancel and Add.
  - The live region (`Announce`) says what the program last said.
  - Each card's body is a `Same` keyed by everything it shows and sends. A pass therefore runs only the cards that changed, and a pointer move runs none of them.
- Package `script` is the host. It opens the program on std ui's `TestHost`, plays each line on the screen and answers each command: an id `new-N`, a save to the `board.Store` in the context, a focus, or where a keyboard move goes. The actor's output goes to a queue, and the host answers from it until the program asks nothing more.
- `cmd/fk-board` binds `file.NewSheet` on the host's file system. It is the only place that chooses the store.

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

## Laws

- `sortkey`: `BetweenIsStrictlyBetween`, over lists grown by drawn inserts.
- `board`:
  - `ReorderIsAPermutation` checks drawn reorders against a reference on lists of ids, including unknown cards, an unknown column and positions past either end.
  - `SortKeysStrictlyIncrease`.
  - `Store.LoadGivesTheLastSave`, on `Shelf` and `Sheet`.
- `drag`: `DragStartsPastTheThreshold`.
- `kanban`: `NewCardFollowsTheForm`, `CancelRestoresTheBoard` and `KeyboardDropEqualsPointerDrop`.
- `file`: `AForeignLineIsNamed`.
- `script`, on the test host:
  - `PointerDropLandsWhereShown`: a drawn card is pressed, moved over a drawn half of a drawn card or below a column's cards, and released. The placeholder shows where the reference says the card will drop, and after the release the screen shows the reference board, which was saved once. The live region says where the card was dropped.
  - `AMoveRunsNoCard`: during a pointer drag, a move from one drawn spot to another keeps every card's `Same` without running its body.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`: starts from the default board.
  - Pointer drags: a press with no move, a move under the threshold, a drop within a column and one across, a release over nothing, and an Escape.
  - Keyboard drags: within a column, past either end, across columns past the last one and back, and one given up.
  - The form: a card added, a blank title, a cancel, and an Escape.
- `scenes`: upstream's 7 scene tests on upstream's test board (see "Upstream tests").
- `persisted`: a saved board with an empty column. A card is moved into that column and another is added.
- `unreadable`: the saved board has a foreign line. The program says which line and starts from the default board.
- `badscript`: names a card the board does not have. The program says which line and exits 1.
