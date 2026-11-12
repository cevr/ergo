# stars-gui

A table of stars that a window will show: 100,000 rows of a generated catalogue (name, constellation, magnitude, distance, class, right ascension, declination, planets). The user scrolls, sorts by any column, filters as they type, selects a range and edits a cell. This is the first stage of the program (corpus wave GP1a): the model and a headless driver. The view on `ui.TestHost` (stage b) and the desktop window (stage c) come when the `ui` host units land.

```sh
stars-gui gen [-seed S] [-n N]     # write a catalogue of N stars, one a line, tab-separated
stars-gui run [-rows H] CATALOG    # play the script on standard input, H rows a window
```

A script is one command a line: `show` writes the frame a window would show, as text; `down N`, `up N`, `pagedown`, `pageup`, `home`, `end` move; `sort COLUMN` sorts, a second `sort` of the same column reverses; `filter`, `type TEXT`, `backspace`, `enter` and `escape` filter; `mark` and `extend` select from the anchor to the cursor; `edit COLUMN`, `type`, `backspace`, `enter` and `escape` edit a cell.

## What it shows

- **A machine with three modes.** `catalog.Table` is `machine[State, Signal]`, where `State = Browsing | Filtering | Editing`, each holding the `Grid` (the catalogue, the view's ids, the sort key, the filter, the window, the selection). The step matches the mode and the signal exhaustively. Its laws: `ReachesEditing` and `FilterAndBack` (searched), `WindowWithinBounds` (after every signal, the cursor is on a row and the window holds it, inside the view), `SelectionSurvivesSort` and `ViewIsOrder` (whatever the script, the view equals a fresh filter and sort).
- **Laws justify the fast paths.** Ties sort by id, so the order is total. `FilterThenSortCommutes` lets a sort reorder only the shown ids; `NarrowIsOrder` lets a typed letter filter the shown list in place, with no sort, when the old filter is a prefix of the new one and both are valid UTF-8 (`Narrows`). `SortIsPermutation` states that a sort loses and adds no row.
- **Cells are a union.** `Cell = Text | Fixed | Hours | Count`; `CellOf`, `CellText` and `CompareCells` match it exhaustively per column. Numbers are integers in the column's unit (hundredths of a magnitude, tenths of a light year), so every text is exact.
- **A refused edit is a typed failure.** `ParseCell` fails `Refused{Column, Text, Reason}`, `Reason = Blank | Separator | NotANumber | OutOfRange`; the step's row carries it, and the driver writes it and keeps the draft. The catalogue's reader uses the same function (`CellTextReadsBack`, `FormatParsesBack`), so a file and an edit accept the same texts.
- **Contracts and providers.** `catalog.Shelf` (law `Repeatable`) is where a catalogue's text comes from: `files` reads the host's disk, `memory` holds texts for tests. `cmd/stars-gui/main.ergo` alone chooses them.

## Layout

- `catalog/`: the domain. `star.ergo` (stars, columns, cells), `edit.ergo` (reading a cell, the refusal), `view.ergo` (filter and order), `table.ergo` (the machine), `frame.ergo` (a frame as text), `shelf.ergo` (the contract and the catalogue's form), `gen.ergo` (the seeded workload).
- `script/`: the headless driver: a line to a command (`LineParsesBack`), a script played on the machine.
- `files/`, `memory/`: the providers of `Shelf`.
- `cmd/stars-gui/main.ergo`: binds `files` over `fs.NewHost()`.

## The Go control

`cmd/ergo/testdata/stars-gui/control` is the same model in hand-written Go: a column store with one slice per column, ids sorted with `slices.SortFunc`, a filter that narrows in place, frames written with one buffer. `TestStarsGuiAgreesWithControl` (cmd/ergo, full tier) runs both on seeded catalogues and scripts and checks that each prints what a plain reference model in the test prints. The receipts per operation (`scripts/opcount`) wait for unit C0.
