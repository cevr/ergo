# keypad

An on-screen keyboard for a form of several fields. A script on standard input plays the screen: its first line opens a form, and each other line is one key press or one change of the form. The program tells what the board does: each new text of a field, a submit, a greyed key, a refusal, and every field's text when the board closes.

Tag: `default`. It is an original program, a neutral re-creation of a common pattern (a class A machine: a pure step, no timers and no work). Its goldens were made by running the program and then checked line by line against the rules below by hand.

## Rules

- A form is a list of fields. Each field has an ID, a layout (`digits`, `letters` or `any`), a `Min` length that Return needs and a `Max` length (0 is no limit). One field has the focus.
- A key cap is one character. A field takes a key cap when its layout takes it and the field is not full; any other key is greyed and does nothing. Backspace removes the last character, and is greyed in an empty field.
- Shift once: the next letter is upper case. Shift twice within one second: Caps, every letter is upper case until the next Shift. A second Shift after more than a second goes back to lower case. A greyed key does not use up a Shift.
- Return submits the focused field's text when it has at least `Min` characters, and is refused otherwise.
- `focus ID` moves the focus; `add ID LAYOUT MIN MAX` adds a field; `remove ID` removes one. A removed focused field passes the focus to the field that takes its place, or to the one before it. The last field cannot be removed; an ID is unique; `Min` and `Max` are not negative, and `Min` is at most a `Max` that is not 0.
- `close`, or the end of the input, closes the board and prints every field. Lines after `close` are not read.

Script lines: `key C`, `space`, `back`, `shift`, `return`, `wait MS` (the script's clock moves on MS milliseconds), `focus ID`, `remove ID`, `add ID LAYOUT MIN MAX`, `close`; blank lines and lines that start with `#` do nothing. A line the script cannot read is told with its number and changes nothing.

## Layout

The program is a module of packages (`ergo.mod`: `module keypad`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `keypad/` | The domain package. `field.ergo` (`Layout`, `Spec`, `Field`, `Takes`, `CanType`), `shift.ergo` (the `Shifter` machine of the shift mode), `board.ergo` (`Key`, `Signal`, `Output`, the `Pad` state, the typed refusals, `Open`, `CanPress`, `Step`, the `Keypad` machine), `forms.ergo` (`Forms`, the contract of a store of forms, with two laws), `laws.ergo`, `tests.ergo`. It is pure. |
| `memory/` | `Fixed`, the in-memory provider of `keypad.Forms`: the forms `login`, `address` and `code`. |
| `script/` | The line protocol: `Parse` (fails `BadLine`) and `Run`, the only function with capabilities (`Console`, `io.Stdin`, `keypad.Forms`). |
| `cmd/keypad/` | The binary. `main.ergo` binds `let Live = context { keypad.Forms: memory.Fixed }` and the host's standard input, and is the only place that chooses them. |

## What it shows

- **A machine with a machine inside it.** `type Keypad machine[Pad, Signal]` is the board. Its state holds the shift mode, which is a machine of its own, `type Shifter machine[Mode, Tap]`, and the board's step steps it with `machines.Next(b.Mode, ShiftStep(b.Mode, tap))`. This is the region pattern (M56) until Ergo has regions. The law `SecondShiftLocksWithinTheWindow` runs the inner machine alone with `machines.Run(Shifter{}, taps)`.
- **Signals carry their time.** A key press is `Pressed{Key, At}`, so the one-second Caps window is a rule on values and the step stays pure: no clock, no timer.
- **Exhaustive `match`.** Every rule is a `match` that names every variant: `Key`, `Signal`, `Mode`, `Tap`, `Layout`, `Output` and `Decision`. The only `case _` arms are over a script word or a form name, which are strings.
- **Typed refusals.** `Step` fails `NoSuchField | LastField | Duplicate | BadSpec | TooShort`. `script.turn` catches each by name and tells it, and the board stays as it was. A greyed key is not a failure: it is `Ignore`.
- **Decisions.** `Stay` for typing, Backspace, Shift, Return, Add and a Remove that keeps the focus; `Enter` when the focus moves to another field; `Ignore` for a greyed key or a focus on the focused field; `Done` with `Closed{Fields}` on Close.
- **Laws.** Eight top-level laws in `laws.ergo`, run by `ergo test` (#162): `TypeThenBackspace`, `ShiftAppliesOnce`, `CapsHolds`, `SecondShiftLocksWithinTheWindow`, `LayoutHolds`, `FocusKept`, `GreyedKeysAreIgnored` (the screen's `CanPress` is false exactly for the keys `Step` ignores) and `ReturnNeedsMin`. The contract `keypad.Forms` has two member laws, `EveryFormOpens` and `UnlistedIsNoSuchForm`.
- **Tests.** The laws are the tests. `keypad/tests.ergo` has one run of the machine, `CodeFormRun`, which checks every output of a short session and runs the same form with `machines.From`. `memory/tests.ergo` calls the two `Forms` laws against `Fixed` (see Limits found).

## Limits found

- **No Emit (q28; M20).** A step cannot give an output and go on, so the state carries an outbox, `Out Option[Output]`, that the step sets and the run loop reads after each `Enter` or `Stay`. `Ignore` and `Reject` keep the earlier state, and with it the earlier step's output: a reader that reads the outbox after them repeats a stale output (`CodeFormRun` found this in the first draft of its own reader). `machines.From` keeps only the last state, so a run loses every output but the last. The Emit decision (lane V, V5) replaces the field.
- **One-variant state (M20).** Every keypad state holds the same fields, so `Pad` is a union of one variant, `Board`, and each function first matches it (`BoardOf`).
- **No machine input (V4; M20 g-5).** `initial` is a value, so the machine starts at the `login` form. A run of another form starts with `machines.From(Keypad{}, Open(form), signals)`.
- **No `Can` query (M51).** The screen greys keys with a hand-written `CanPress`; the law `GreyedKeysAreIgnored` holds it equal to what `Step` does.
- **No rune classes (M18).** A letter is a text that `strings.ToUpper` or `strings.ToLower` changes, so `ß`, which has no one-character upper case, is not a letter here. Key caps are kept as strings, one per key, so Backspace removes one whole character with no rune decoding.
- **No regions (M56).** The shift mode is a machine held in a field and stepped by hand.
- **Machine laws stand at top level (M55).**
- **`==` on a state that holds slices (M29).** The laws compare texts through `Texts`, not states.
- **The examples runner runs a package's laws only when the package declares a `test` (G-ma-1).** So `keypad/` keeps one test, `CodeFormRun`.
- **A law of an imported operation type does not run against another package's provider (G-ma-2).** `ergo test keypad/keypad` prints `skip law Forms.EveryFormOpens: no type of the package implements Forms`, so `memory/tests.ergo` calls the laws' functions with `Fixed` bound.
- **Inference.** `machines.Next[Pad, Output](b, d)` needs its type arguments when `b` is a `Board`, the variant, and the state type is `Pad` (lane V follow-up, K3 union join).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/keypad/cmd/keypad < examples/keypad/cases/login/stdin
./bin/ergo test examples/keypad/keypad
go run ./scripts/examples keypad
```

## Cases

`login` (one Shift, Caps by two Shifts and its end, greyed keys, a short Return, every refusal, a focus moved by a remove, and a line after `close` that is not read), `address` (another form, a Shift on a text field, a greyed digit in a letters field, lines the script cannot read, and the end of the input) and `noform` (a form the store does not hold: status 1).
