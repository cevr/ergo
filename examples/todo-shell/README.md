# todo-shell

A small interactive todo list. It reads one command per line from standard input and answers on standard output: `add`, `note` (an item with a note of several lines), `done`, `undo`, `drop`, `list`, `clear` (after a yes), `help` and `quit`. On a terminal it shows a prompt that names its mode.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and then checked line by line against the rules below by hand.

## Layout

The program is a module of packages (`ergo.mod`: `module todoshell`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `todo/` | The list, the domain package. `list.ergo` (`Item`, `List` with construction authority, `Add`, `AddNote`, `Complete`, `Reopen`, `Drop`, `At`, the typed failure `NoSuchItem`), `laws.ergo`, `tests.ergo`. It is pure. |
| `shell/` | The line protocol. `mode.ergo` (the `Mode` union and its prompts), `command.ergo` (what each mode reads, and the failures `Unknown`, `BadNumber`, `MissingText`), `events.ergo` (`Event`, `Decide`, `Evolve`), `machine.ergo` (`Signal`, `Step`, the `Shell` machine, `Apply`, `Next`), `say.ergo` (the text of each event), `run.ergo` (`Run`, the only function with capabilities: `Console`, `io.Stdin`, `io.Stdout`), `laws.ergo`, `tests.ergo`. |
| `cmd/todo-shell/` | The binary. `main.ergo` binds the host's `io.Stdin` and `io.Stdout` and is the only place that chooses them. |

## What it shows

- **A machine for the modes.** `type Shell machine[Mode, Signal] { initial: Start(), step: Step }`. `Mode = Listing | Composing | Confirming`: Listing reads commands, Composing reads the lines of a note until a line `.`, and Confirming waits for the answer to `clear`. `Signal = Typed{Line} | Ended{}`: the end of the input is a signal too, and `Step` makes it `Done` with the list in every mode. `Step` uses all five `Decision` variants: `Enter` a new mode, `Stay` (a list or the help changes only the output), `Ignore` (a blank line), `Reject` (a line that fails, in `Tried` and `Run`), `Done` (quit or the end).
- **Commands and events.** Each mode has its own reader (`ParseCommand`, `ParseNote`, `ParseAnswer`), so a mode reads only its own lines. `Decide` records past-tense events (`Added`, `Completed`, `NoteSaved`, `ClearAsked`, …); `Evolve` folds them into the mode and decides nothing; `Say` tells them. Every rule is a `match` that names every variant, with no `case _` except over the first word of a command line, a string.
- **Typed failures.** `Decide` fails `Unknown | BadNumber | MissingText | todo.NoSuchItem`. `Run`'s `turn` catches each by name, tells it, and the mode stays as it was.
- **Laws.** `todo` states five laws over the list (`AddCountsOne`, `CompleteIsIdempotent`, `ReopenUndoesComplete`, `DropRemovesOne`, `OpenAtMostCount`), and `shell` five over the machine (`BlankIsIgnored`, `QuitIsDone`, `EndIsDone`, `ClearAsksFirst`, `NoteLinesKeepTheList`). Each package's tests restate them as property tests.
- **Scripted input.** The tests drive `Run` with `io.Input{Text: script}` as `io.Stdin` and an `io.Recorder` as `Console` and `io.Stdout`: no host and no mock framework. `PromptsFollowTheMode` binds a `Stdout` provider that is a terminal, so the prompts show.
- **Values, not shared state.** A `Mode` holds its `List` and a note's lines as values; `appended` copies a note's lines, because `Append` may share an array (check-v1 Q2).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/todo-shell/cmd/todo-shell      # type commands; quit or ^D ends
./bin/ergo test examples/todo-shell/shell
go run ./scripts/examples todo-shell
```

## What waits

- Laws run as generated tests when #162 lands; then the restated property tests go. Laws move into the machine type when machines carry laws (#57).
- A runner from the actor library (#57, #101) replaces `Run`'s loop over `turn`.
- `Run` cannot catch a read failure of standard input: `Lines` is a `Seq`, which has no failure row (std-strengths I3).

## Cases

`session` (every command, each failure, a note, a kept and a done clear, and input after `quit` that is not read) and `eof` (the input ends while a note is being written: the note is not saved, and the list is returned).
