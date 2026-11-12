# editor-gui

A text editor with undo: a piece table over an append-only added buffer, a line index, a caret, a selection, find and replace, and undo and redo as a log of edits. This is the first stage of the program (corpus wave GP2a): the model and a headless driver. The view on `ui.TestHost` (stage b) and the desktop window (stage c) come when the `ui` host units land.

```sh
editor-gui gen [-seed S] [-n N]   # write a text of N lines of words, some beyond ASCII
editor-gui edit FILE              # play the script on standard input over FILE
```

A script is one command a line: `show` writes the frame a window would show, as text (a status line, then the caret's line and two lines on each side); `type TEXT` and `enter` type; `back` and `del` erase a rune; `left N`, `right N`, `up N`, `down N`, `home`, `end`, `top` and `bottom` move; `select` anchors a selection that the next letter or erase replaces; `find`, then `type`, `back`, `next`, `replace TEXT` and `escape`, search; `undo`, `redo` and `save`. A line that is no command writes `? "LINE": want ...` and the script goes on.

## What it shows

- **A machine with three modes.** `edit.Editor` is `machine[State, Signal]`, `State = Editing | Selecting | Searching`. The step matches the mode and the signal exhaustively, and a signal a mode has no use for is `Reject`. Save is the machine's output (`Emit` of `Write`), so the step stays pure: the driver performs the write on the `Store` and answers `Saved`. Laws: `CaretWithinText`, `UndoRedoRoundTrips` (undo everything, the loaded text; redo everything, the edited text), `ReplaceIsReplaceAll` (one change, the text `strings.ReplaceAll` gives), `SelectAndBack` and `SearchAndBack` (an escaped selection or search leaves the editor as it was).
- **A piece table under value semantics.** `piece.Buffer` keeps the original text, an added buffer that only grows (`AddedOnlyGrows`) and a slice of pieces; an edit appends and splits, and copies no text. Undo is a log of `Edit = Inserted | Deleted` values with their inverses, never a snapshot. Laws hold it to a plain string: `BufferEqualsModel`, `LinesEqualModel` (the line index finds the lines `strings.Split` finds), `InsertThenDeleteRestores`.
- **Typed failures.** Opening a document fails `Unreadable` or `NotText{Offset}` (not UTF-8); a missing document is a new, empty one. The `Store` contract's `Save` fails `Unwritable`, and the driver says so and keeps the edits.
- **Contracts and providers.** `edit.Store` (law `SaveThenLoad`): `files` keeps documents on the host's disk (`fs.WriteFile` writes a new file and renames it over the old), `memory` holds them in a `sync.Ref` for tests. `cmd/editor-gui/main.ergo` alone chooses them.

## Layout

- `piece/`: the piece table and its line index.
- `edit/`: the domain. `doc.ergo` (a document, edits, undo and redo, rune and line moves), `editor.ergo` (the machine's step), `laws.ergo` (the machine and its laws), `frame.ergo` (a frame as text), `store.ergo` (the contract, its failures, opening a document), `gen.ergo` (the seeded workload).
- `script/`: the headless driver: a line to a command (`LineParsesBack`), a script played on the machine, the save performed.
- `files/`, `memory/`: the providers of `Store`.
- `cmd/editor-gui/main.ergo`: binds `files` over `fs.NewHost()`.

## The Go control

`cmd/ergo/testdata/editor-gui/control` is the same editor in hand-written Go: a piece table over byte slices, an undo log, frames written with one buffer. `TestEditorGuiAgreesWithControl` (cmd/ergo, full tier) runs both on seeded texts and scripts and checks that each prints what a plain string model in the test prints, and that the saved file is the model's text. The receipts per keystroke, per undo and per find (`scripts/opcount`) wait for unit C0.
