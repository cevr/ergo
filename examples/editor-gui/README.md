# editor-gui

A text editor with undo: a piece table over an append-only added buffer, a line index, a caret, a selection, find and replace, and undo and redo as a log of edits. Stage a (corpus wave GP2a) is the model and a headless driver; stage b (GP2b) is the window's view on `ui.TestHost`. The desktop window (stage c) comes with the MyGo host.

```sh
editor-gui gen [-seed S] [-n N]   # write a text of N lines of words, some beyond ASCII
editor-gui edit FILE              # play the script on standard input over FILE
editor-gui scene FILE             # play it on the window's view, on ui.TestHost
```

A script is one command a line: `show` writes the frame a window would show, as text (a status line, then the caret's line and two lines on each side); `type TEXT` and `enter` type; `back` and `del` erase a rune; `left N`, `right N`, `up N`, `down N`, `home`, `end`, `top` and `bottom` move; `select` anchors a selection that the next letter or erase replaces; `find`, then `type`, `back`, `next`, `replace TEXT` and `escape`, search; `undo`, `redo` and `save`; `copy` and `cut` put a selection on the clipboard and `paste` types it; `click ROW COL` puts the caret at column COL of the frame's row ROW, as a press on the window's page does. A line that is no command writes `? "LINE": want ...` and the script goes on.

## What it shows

- **A machine with three modes.** `edit.Editor` is `machine[State, Signal]`, `State = Editing | Selecting | Searching`. The step matches the mode and the signal exhaustively, and a signal a mode has no use for is `Reject`. Save is the machine's output (`Emit` of `Write`), so the step stays pure: the driver performs the write on the `Store` and answers `Saved`. Laws: `CaretWithinText`, `UndoRedoRoundTrips` (undo everything, the loaded text; redo everything, the edited text), `ReplaceIsReplaceAll` (one change, the text `strings.ReplaceAll` gives), `SelectAndBack` and `SearchAndBack` (an escaped selection or search leaves the editor as it was).
- **A piece table under value semantics.** `piece.Buffer` keeps the original text, an added buffer that only grows (`AddedOnlyGrows`) and a slice of pieces; an edit appends and splits, and copies no text. Undo is a log of `Edit = Inserted | Deleted` values with their inverses, never a snapshot. Laws hold it to a plain string: `BufferEqualsModel`, `LinesEqualModel` (the line index finds the lines `strings.Split` finds), `InsertThenDeleteRestores`.
- **Find reads the text in place.** `piece.Index`, `Matches` and `Count` search each piece's bytes where they lie, and a match across pieces in a window under twice the query's length around a piece's end; no search builds the text (`SearchEqualsModel`: the same matches as `strings.Index` on the plain string, with queries drawn from the text and overlapping ones).
- **Pointing and the clipboard.** `Goto{At}` puts the caret on the rune boundary at or before a pressed offset (`GotoLandsOnARune`). `Copy` and `Cut` are outputs, like a save (`CopyIsTheSelection`, `CutThenPasteRestores`). The driver writes `ui.Clipboard`, and a paste is the driver typing what the clipboard holds.
- **Typed failures.** Opening a document fails `Unreadable` or `NotText{Offset}` (not UTF-8); a missing document is a new, empty one. The `Store` contract's `Save` fails `Unwritable`, and the driver says so and keeps the edits.
- **One view for every host.** `screen.Viewer(store, clip)` is the window:
  - a toolbar (undo, redo, save, select, find, next, copy, cut, paste, and a replacement field with its replace button, `ui.Field` and `ui.Submit` over view-local state);
  - the text input a host commits typed text to;
  - the status line and the lines around the caret;
  - a page: a `ui.Area` whose press puts the caret under the pointer (`ui.OffsetAt`), over a `ui.Canvas` that paints the selection and the caret where the text layout puts them (`ui.LayoutText`, `ui.SelectionRects`, `ui.CaretAt`).

  Keys move, erase, break lines, escape, and with Ctrl undo, save, find, copy, cut, paste and go to the ends (`screen.Keyed`). `screen.App` holds the editor, its save in flight and its clipboard task in flight. The editor's `Write` output makes the view ask the host for the write (`ui.Load`, keyed by the save's number), and its end comes back as `Wrote` or `WriteFailed` (laws `WroteIsSaved` and `FailedSaveKeepsEdits`). A copy's text and a paste's read are host tasks the same way, ending in `ClipWritten` or `Clipped` (laws `CopyPutsTheSelection`, `PasteTypesTheClipboard`). The step stays pure, and the store and the clipboard are the ones `main` chooses.
- **A scene is the script on the window.** `script.Scene` plays `edit`'s script language on the window, as a user would. A move of N is N key presses, so `up 3` keeps the column of each line it leaves. A click presses the page where that row and column's caret is drawn, and the view's hit test takes it back to the offset. `TestEditorGuiAgreesWithControl` holds what the scene shows and saves to the reference of the script as a window plays it.
- **Contracts and providers.** `edit.Store` (law `SaveThenLoad`): `files` keeps documents on the host's disk (`fs.WriteFile` writes a new file and renames it over the old), `memory` holds them in a `sync.Ref` for tests. `cmd/editor-gui/main.ergo` alone chooses them.

## Layout

- `piece/`: the piece table and its line index.
- `edit/`: the domain. `doc.ergo` (a document, edits, undo and redo, rune and line moves), `editor.ergo` (the machine's step), `laws.ergo` (the machine and its laws), `frame.ergo` (a frame as text), `store.ergo` (the contract, its failures, opening a document), `gen.ergo` (the seeded workload).
- `screen/`: the window: the app machine (`app.ergo`) and the view (`view.ergo`).
- `script/`: the drivers: a line to a command (`LineParsesBack`), a script played on the machine with the save performed (`script.ergo`), or on the window on `ui.TestHost` (`scene.ergo`).
- `files/`, `memory/`: the providers of `Store`.
- `cmd/editor-gui/main.ergo`: binds `files` over `fs.NewHost()`.

## The Go control

`cmd/ergo/testdata/editor-gui/control` is the same editor in hand-written Go: a piece table over byte slices, an undo log, frames written with one buffer. `TestEditorGuiAgreesWithControl` (cmd/ergo, full tier) runs both on seeded texts and scripts and checks that each prints what a plain string model in the test prints, and that the saved file is the model's text. The receipts per keystroke, per undo and per find (`scripts/opcount`) wait for unit C0.
