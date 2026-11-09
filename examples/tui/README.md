# tui

A list picker in plain ANSI, in the manner of fzf: the arguments are the items (none picks from a list of languages that compile to another language), the keys come from standard input, and the picked item is printed.

| key | Browsing | Filtering |
| --- | --- | --- |
| Up / Down (or `k` / `j` while browsing) | move the cursor | move the cursor |
| `/` | start filtering | types `/` |
| a character | ignored (`q` cancels) | adds to the filter; the list narrows as you type |
| Backspace | ignored | removes the filter's last character |
| Enter | picks the item under the cursor (an empty list rejects it) | back to browsing, filter kept |
| Esc | cancels | back to browsing, filter cleared |
| Ctrl-C | cancels | cancels |

```sh
printf '/el\r\033[B\r' | tui            # prints: Elm
printf 'jj\033[A\r' | tui -frames Go Rust Zig   # draws each frame, then: Rust
```

A cancel exits 1, and keys that end before a pick exit 2. Ergo has no raw mode and no read of one key yet (G-ex-c-15), so the program reads its keys to the end of input and then plays them: it runs on a script of keys, not on a live terminal. Frames are drawn when standard output is a terminal or with `-frames`.

Tag: `default`. It is an original program, not a port.

## Layout

The program is a module of packages (`ergo.mod`: `module tui`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `keys/` | `key.ergo`: `Key` (a union: `Up`, `Down`, `Return`, `Backspace`, `Escape`, `Interrupt`, `Char`), `Decode` of a terminal's bytes, `Encode`, `Script`, `Typed`; `laws.ergo` (`RoundTrip`, `DecodeTakesEveryText`), `tests.ergo`. |
| `picker/` | The domain. `model.ergo`: `View`, `Mode = Browsing | Filtering`, `Choice = Picked | Cancelled`, `Step`, the machine `Picker`, `Visible`; `run.ergo`: `Run`, which keeps every mode as a frame and counts the rejected keys; `laws.ergo` (five laws), `tests.ergo` (scripted sessions). |
| `screen/` | `frame.ergo`: `Frame`, `Header`, `Marked` (ANSI: clear, reverse video, CR LF); `laws.ergo` (`OneLineIsMarked`), `tests.ergo`. |
| `cmd/tui/` | The binary: flags, the host's streams, and the exit codes. |

## What it shows

- **A machine for the modes.** `type Picker machine[Mode, keys.Key] { initial: Start, step: Step }`: its input is the items, and the binary starts it with `machines.Start(picker.Picker{}, items)`. `Step` matches on `(Mode, Key)` and names every key in every mode, so a new key or mode stops the build until each pair is written. It uses five of the six decisions (it never emits): `Stay` (the cursor or the filter moves, the mode stays), `Enter` (the mode changes), `Ignore`, `Reject` (Enter on an empty list) and `Done` (a pick or a cancel).
- **Laws of the picker.** `CursorStaysOnTheList` (after any keys), `VisibleIsTheMatches`, `TypeThenDeleteIsNoChange`, `APickIsVisible` and `InterruptCancels` (in every mode).
- **A key decoder with laws.** `RoundTrip` (the bytes of keys decode to the keys) and `DecodeTakesEveryText` (any bytes decode, and no `Char` is a control byte).
- **A frame law.** `OneLineIsMarked`: one line in reverse video, the item under the cursor, and one line per visible item.
- **A scripted-input test.** The keys are bytes, so a test is a script: `ran([]string{"-frames", "a", "b"}, "j\r")` checks every byte of every frame.

`ergo test` runs each law, and each has a mutant that the law kills (`tui-mutants.json` of the batch-12 ex-upgrade unit, 8 of 8, seeds 1-3; from `ex-c/tui/mutants.ts`). Generated keys seldom filter and generated filters seldom match, so the laws also run on a fixed list, on keys that narrow it, and with no filter.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/tui/cmd/tui -o /tmp/tui
printf '/el\r\033[B\r' | /tmp/tui
for p in keys picker screen cmd/tui; do ./bin/ergo test examples/tui/$p; done
go run ./scripts/examples tui
```

## Cases

`pick` (filter `el`, down, pick: `Elm`), `frames` (`-frames` over three items: every frame's bytes), `cancel` (Ctrl-C while filtering: status 1) and `ended` (the keys end first: status 2).

## Limits found

Each limit is recorded with a repro in the lane's gap file (`gaps-ex-c.md`, run dir).

- **G-ex-c-15: no terminal.** No raw mode, no terminal size and no read of one key, and a user extern cannot bind `golang.org/x/term`: the picker runs on a script of keys.
- **G-ex-c-8: no read of a given length.** The keys are read to the end of input.
- **G-ex-c-16: a machine's state must be a union, with no context.** Each mode carries the shared `View`, and `ViewOf` reads it back.
- **G-ex-c-17: an `os.Exit` code outside 1..125 is found only on the host.** fzf exits 130 on Ctrl-C; this program exits 1, because the checker passed 130 and the host refused it.
- **G-ex-c-11: prelude names.** The key `Enter` cannot be declared (the prelude's `Decision` has `Enter`), so it is `Return`.

Closed in batch 12: G-ex-c-10 (K3: `let placed = match …`); the machine's fixed start and hand stepping (V2 and V4: its input is the items, `machines.Start`, `machines.Next`; `picker.Run` stays because it keeps every frame); G-ex-a-1, which this program met too without citing it (S3 and L4: `keys.Decode` takes a character's size from `utf8.DecodeRuneInString`, `Typed` and `dropLast` loop over runes, where they read UTF-8 lead and continuation bytes by hand); `slices.Equal` and the test helper `join` (W3 `==` on key lists, S2 `slices.Concat`); and the property tests that restated the laws (V1: the laws run, with the tests' steering moved into them).
