# fk-transfers

File transfers that a user starts, cancels one at a time or all at once, and restarts. It is a port of Foldkit's `interrupting-commands` example (`conformance/upstream/foldkit-d21db423/examples/interrupting-commands`, MIT), stage a of FK4: the model, with no view yet. The domain is file transfers, with neutral file names. The program runs a script of timed presses on a manual clock and shows the board after each one.

Tag: `default`.

## Two shapes

The plan asks for both of upstream's ways to hold work in flight, kept side by side and compared.

- **Shape A, state-owned (the idiom).** Package `transfer` is one transfer: the machine `Sender`, with state `Sending{MB} | Sent{MB} | Cancelled{MB}` and signal `Cancel | Restart | Arrived`. The `Sending` state owns its time: its entry's timer (the machine's `after`) gives `Arrived` after 100 ms per megabyte. A cancel enters `Cancelled`. That ends the entry, and with it the timer, so the transfer is interrupted and no outcome message is needed. A restart enters `Sending` again with a new timer. A cancel after arrival finds no entry to end and is ignored, so the port has no registry and no `NotFound` case. Package `board` holds one live actor per transfer (`actor.StartMachine`) by id. A press is a signal to one actor, and cancel all is a cancel to each actor that sends.
- **Shape B, emitted commands (upstream's).** Package `emitted` is one machine, `Uploader`. Its state holds every transfer's `Status`, and its steps `Emit` commands `StartUpload{ID, MB} | Interrupt{ID}`. The sink keeps the keyed registry of jobs in flight, as Foldkit's runtime does, and answers an interrupt with `Interrupted` or `NotFound`. `registry.ergo` is that sink as a pure model on a clock the caller moves (`Press`, `Wait`), so the shape runs as a fold.

The law `ShapesAgree` is the model-based test of shape B against shape A. Over generated starts, cancels, cancel-alls, restarts (also for ids with no transfer) and clock moves, both show the same transfers in the same states after every press and at the end.

Shape A is the idiom. Its model is 93 lines (`transfer` 47, `board` 46) against shape B's 138 (`emitted` 71, `registry` 67). Its rules are laws of one step of one transfer (`CancelledNeverBecomesDone`, `RestartOnlyFromCancelled`), so they hold for every transfer, while shape B's hold only through the registry. Cancellation is the end of an entry, not a protocol with two outcomes.

Two things the port found:

- **L3 has a form.** The board is not a machine. Its children are actors that belong to no parent entry, so a parent transition cannot stop them. The parent keeps no state beyond the actors, so nothing is lost.
- **A `Close` makes a record a resource.** The board's stop is called `Shutdown`, not `Close`. A written `Close` makes `Board` a resource (resources R1). `Start(Start(New()))` then releases the board the outer `Start` was given at the end of the statement, which stops the actors the new board shares (measured: the first transfer ended `Halted`).

A press at the very instant a transfer would arrive comes after the arrival. The live board moves the clock to the press's time first, so timers due by then have fired, and shape B's registry does the same (case `tie`). In `machines.RunTimed` the order is the other way: a signal at a timer's instant is stepped first (transfer law `ArrivesAfterTheWholeFile`).

## Upstream tests

Each title of upstream's `src/story.test.ts` and `src/scene.test.ts`, and what restates it. (FK0 moves this list to `origin/tests.txt`, which the examples runner checks.)

| Upstream title | Restated by |
| --- | --- |
| starting an upload appends an Uploading entry and fires UploadFile | `board` test `StartAppendsASendingTransfer` |
| cancelling an upload interrupts it and marks it Cancelled | `board` test `CancelEndsTheTransfer` |
| a cancel that lands after completion resolves NotFound and changes nothing | `board` test `CancelAfterArrivalChangesNothing` |
| cancelling one upload leaves the other running | `board` test `CancelOneLeavesTheOther` |
| a new upload can start while a cancellation is still pending | `board` test `StartWhileACancelIsPending` |
| restarting a cancelled upload reuses its id and file | `board` test `RestartKeepsItsIdAndFile` |
| cancel all interrupts every running upload and only those | `board` test `CancelAllStopsOnlyTheSending` |
| initial view shows the start button and an empty state | not restated: stage a has no view (FK4b, after H4 and H4r) |
| starting an upload shows an Uploading row with a cancel button | not restated: stage a has no view (FK4b) |
| cancelling an upload marks it Cancelled and offers a restart | not restated: stage a has no view (FK4b) |
| the cancel all button appears only while an upload is running | not restated: stage a has no view (FK4b) |

## Laws

- `transfer`: `CancelledNeverBecomesDone` (no signal takes a cancelled transfer to sent; only a restart leaves cancelled), `RestartOnlyFromCancelled`, and `ArrivesAfterTheWholeFile` (timed runs: a transfer arrives at its whole file's time unless it is cancelled before).
- `script`: `ShapesAgree` (above), `CancelAllLeavesNoneRunning` (right after a cancel all, no transfer of the live board sends) and `IdsNeverReused` (ids are 0, 1, 2, … in the order of the starts, and a restart keeps its id).

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `transfers`: three transfers, a cancel and a restart, a cancel of a sent transfer, cancel all with one already sent, a restart while sending (ignored), a cancel of an id with no transfer, and every transfer at the end.
- `tie`: a cancel at the very time the transfer arrives; the arrival comes first.
- `badscript`: `cancel` with no id; the program says which line and exits 1.
