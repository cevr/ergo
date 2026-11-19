# transfers

File transfers that a user starts, cancels one at a time or all at once, and restarts. The domain is file transfers, with neutral file names. The program runs a script of timed presses on a manual clock, clicks each press's button on the screen, and shows the screen after each one.

Tag: `default`.

## Two shapes

The plan asks for two ways to hold work in flight, kept side by side and compared.

- **Shape A, state-owned (the idiom).** Package `transfer` is one transfer: the machine `Sender`, with state `Sending{MB} | Sent{MB} | Cancelled{MB}` and signal `Cancel | Restart | Arrived`. The `Sending` state owns its time: its entry's timer (the machine's `after`) gives `Arrived` after 100 ms per megabyte. A cancel enters `Cancelled`. That ends the entry, and with it the timer, so the transfer is interrupted and no outcome message is needed. A restart enters `Sending` again with a new timer. A cancel after arrival finds no entry to end and is ignored, so the port has no registry and no `NotFound` case. Package `board` holds one live actor per transfer (`actor.StartMachine`) by id. A press is a signal to one actor, and cancel all is a cancel to each actor that sends.
- **Shape B, emitted commands.** Package `emitted` is one machine, `Uploader`. Its state holds every transfer's `Status`, and its steps `Emit` commands `StartUpload{ID, MB} | Interrupt{ID}`. The sink keeps the keyed registry of jobs in flight, and answers an interrupt with `Interrupted` or `NotFound`. `registry.ergo` is that sink as a pure model on a clock the caller moves (`Press`, `Wait`), so the shape runs as a fold.

The law `ShapesAgree` is the model-based test of shape B against shape A. Over generated starts, cancels, cancel-alls, restarts (also for ids with no transfer) and clock moves, both show the same transfers in the same states after every press and at the end.

Shape A is the idiom. Its model is 94 lines (`transfer` 47, `board` 47) against shape B's 138 (`emitted` 71, `registry` 67). Its rules are laws of one step of one transfer (`CancelledNeverBecomesDone`, `RestartOnlyFromCancelled`), so they hold for every transfer, while shape B's hold only through the registry. Cancellation is the end of an entry, not a protocol with two outcomes.

Two things the port found:

- **L3 has a form.** The board is not a machine. Its children are actors that belong to no parent entry, so a parent transition cannot stop them. The parent keeps no state beyond the actors, so nothing is lost.
- **The board is a declared root.** `Board` implements `Closer`: its `Close` stops the actors, and a board's scope releases it (resources R1). A board that builds a new board from its items, as `Start(b Board) Board` did, would make two owners of the actors, and the release of the old board would stop the actors the new one shares (measured before EXH1: the first transfer ended `Halted`). The checker refuses that copy (a handle leaves its declared root only with the whole root, resources R17, R39), so `Start(b *Board)` writes the one board in place. The first port named its release `Shutdown`, so that `Board` was no resource; EXC1 and EXH1 made the declared form safe.

A press at the very instant a transfer would arrive comes after the arrival. The live board moves the clock to the press's time first, so timers due by then have fired, and shape B's registry does the same (case `tie`). In `machines.RunTimed` the order is the other way: a signal at a timer's instant is stepped first (transfer law `ArrivesAfterTheWholeFile`).

## The view

A view reads one state, and the board of shape A is actors, not a state. So the view has its own model of the board:

- Package `desk` is the board as one machine, `Desks`. Its rows each hold a `transfer.Transfer`, stepped by `transfer.Step` as a parent steps pure children. The signals are:
  - from the view: `Begin`, `At{ID, Sig}` (a transfer's own `Cancel` or `Restart`) and `CancelEvery`;
  - from the work: `Timed` and `Arrived`.
- The desk's work owns the time of the sending rows. Each entry's work gives each sending row that has no due time a due time: the clock's now plus the whole file (`Timed`). Then it sleeps until the first due time and sends that row's `Arrived`.
- Every change of a transfer enters the desk again. The entry's end stops the work, and the next entry's work sleeps for the rows that still send. A cancelled row is not one of them, so its arrival never comes. As in shape A, a cancel is the end of a state; the due times are the rows' data, not a registry of work in flight.
- A row's `ui.Load` that sleeps for the file would be the shorter form. It cannot run on the test host: `ui.Settle` waits for every fetch in flight, so a fetch that sleeps on the manual clock holds it forever (gap FK4-g2).
- Package `screen` is the view:
  - the heading `File Uploads`;
  - `Upload a file`, and `Cancel all` while a transfer sends;
  - the line `Nothing here yet. Start an upload.` while there is no transfer;
  - each transfer as a group `Upload ID` with its file, its size, its status (`Uploading`, `Done`, `Cancelled`) and its button (`Cancel upload ID` or `Restart upload ID`).
- `script/host.ergo` plays the script on `ui.NewTestHost`. For each line it moves the clock and settles, clicks the line's button by role and name, and reads the screen. A press whose button is not on the screen clicks nothing.
- `ShapesAgree` now holds three forms equal: the screen's rows, read back to shape A's words, after every press. So the cases print the screen, and the live board and the registry stay held by the law.

## Laws

- `transfer`: `CancelledNeverBecomesDone` (no signal takes a cancelled transfer to sent; only a restart leaves cancelled), `RestartOnlyFromCancelled`, and `ArrivesAfterTheWholeFile` (timed runs: a transfer arrives at its whole file's time unless it is cancelled before).
- `script`: `ShapesAgree` (above: shape A, shape B and the screen), `CancelAllLeavesNoneRunning` (right after a cancel all, no transfer of the live board sends) and `IdsNeverReused` (ids are 0, 1, 2, … in the order of the starts, and a restart keeps its id).

The mutants these kill are in `testdata/mutants.json`.

## Cases

Each case prints the screen: `@MS WHAT`, the line above the list, and `ID NAME WORD [BUTTON]` per transfer. `-stats` also prints the work of each press (`ui.Counts`).

- `transfers`: three transfers, a cancel and a restart, a cancel of a sent transfer, cancel all with one already sent, a restart while sending (ignored), a cancel of an id with no transfer, and every transfer at the end.
- `tie`: a cancel at the very time the transfer arrives; the arrival comes first.
- `scenes`: the four scene tests, in order on one screen.
- `badscript`: `cancel` with no id; the program says which line and exits 1.
