# vt

A minimal terminal emulator with no terminal: it plays recorded terminal output on a screen with scrollback and prints the screen. The parser is the DEC escape-sequence state machine (vt100.net's parser, as Ghostty runs it); it turns the byte stream into events (runs of text, controls, ESC, CSI, OSC, DCS and APC sequences), and the screen acts on them. The screen comes in two forms that keep one contract: a dense one in Ghostty's shape (packed cells in pages, interned and counted styles) and a plain reference. DOD track units DT1 (parser), DT2 (screen) and DT3 (the hand-Go control and its receipts); a view on the cell host comes in DT4.

```sh
vt gen 7 2000000 > out.bin     # 2 MB of generated terminal output from seed 7
vt replay < out.bin            # the history and screen, styles in braces, then the counts
vt replay -plain < out.bin     # the same on the reference screen
vt events < out.bin            # each event as a line, then the tally
vt events -tally < out.bin     # the tally only
```

## Layout

- `vt/`: the domain, pure Ergo.
  - `state.ergo` holds `State`, the 14 states of the DEC parser, which carry nothing. It also holds `Class`, the 22 byte classes, each a set of bytes that every state treats alike, and `Act`, Ghostty's 11 transition actions. `Next(State, Class) Transition` is the step. It is written from vt100.net's state descriptions, with Ghostty's three changes: a colon is a parameter separator in CSI, DEL prints in the ground state, and high bytes are data inside DCS and OSC strings.
  - `machine.ergo` holds the `Parser` machine. Its state is `Parse{At State, Counts}`: the parser's state and the collector's counts (parameters, intermediates, a parameter being read). The counts are in the state so that their bounds are laws of the machine. The parameter values and the intermediate bytes stay outside it, in the `Stream`.
  - `stream.ergo` holds `Stream`, `Feed`, `Finish` and `Take`, and the `Event` union handed to a screen. In the ground state, a run of printable ASCII becomes one `Text` event, a window of the input with no copy. Bytes of 0x80 and above are decoded as UTF-8 (the WHATWG decoder; a bad sequence is U+FFFD) and do not reach the machine; this is Ghostty's stream, which takes no 8-bit control in the ground state. A run of OSC data is likewise taken in one step. The collector keeps at most 24 parameters, each clamped to 65535, and at most 4 intermediates. A CSI with more parameters is dropped, and so is a CSI other than SGR with a colon, as in Ghostty. OSC data is kept up to 1024 bytes. DCS and APC data is counted, not kept.
  - `render.ergo` holds `Render`, which turns events into lines of text. Adjacent text is one line, so where a piece of input ends never shows.
  - `laws.ergo` holds the stream laws.
  - `screen.ergo` holds the contract the domain needs: the operation type `Screen` (print, line feed, the cursor moves, erases, inserts and deletes, scrolls, the region, the pen, the modes, the title, and the reads a test and replay make), with what each command does written on it. It also holds `Style` (two colours and a flag word, pointer-free and comparable) and `Config`, whose history is bounded in bytes and dropped a page (`PageRows` lines) at a time.
  - `apply.ergo` holds `Apply`, which gives a `Screen` each event, and `Sgr`, the pen after an SGR sequence (16, 256 and RGB colours, in semicolon and colon forms). What a screen does not take is counted in `Unhandled`, by name: data, not a failure.
  - `play.ergo` and `dump.ergo` play a stream on a screen in pieces and write a screen as text.
- `screen/`: the dense screen. A `Cell` is 8 bytes with no pointer: 21 bits of code point and 2 of part (single, wide head, wide tail) in one word, and a style id in the other. Every row's cells are in one slice: the alternate buffer's rows, then a ring of pages for the main buffer and its history. A line feed at the bottom of the whole main buffer moves no cell: the buffer starts a line later in the ring. When the history passes its limit, the oldest page is cleared (its styles released row by row, skipping rows with no styled cell) and is used again as the newest. A byte column holds each row's flags (wrapped, styled). The styles are a std `dense.Counted[vt.Style]` (`styles.ergo`): the pen's style is added once per SGR, each cell write moves a count, and a style whose count falls to 0 leaves the set and its id is reused. A cell holds a `dense.Opt` of the id, no id for the default style, which is never counted. The laws of `vt.Screen` run on `*screen.Screen` and `*plain.Screen` directly, built by each package's `NewScreen` (a borrow provider: types.md, Laws 10).
- `plain/`: the reference screen, the obvious form: a slice of lines, each a slice of records of a character, its part and its whole style. Nothing is interned, packed or paged, and most commands build the lines again.
- `width/`: the columns a character takes, from a two-stage table built at start from Markus Kuhn's list of wide ranges and the emoji blocks.
- `ghostty/`: the independent check. `testdata/parse_table.txt` is Ghostty's transition table, transcribed line by line from `src/terminal/parse_table.zig` at `cf3a8a6594ee`, with Ghostty's MIT notice. `table.ergo` reads it, and its law holds the step to it.
- `workload/`: `Gen(seed, n)`, n bytes of shell-like output. It mixes text runs, wide and multi-byte characters, SGR bursts, cursor addressing, scroll regions, erases, modes, OSC titles and C0 controls, plus malformed input: bad UTF-8, 8-bit controls, cancelled sequences, too many or too large parameters, misplaced colons, too many intermediates, DCS and APC strings, and an OSC past the limit. The last piece is cut at n bytes.
- `cmd/vt/main.ergo`: the binary, with `Command = Replay{Plain} | Events{Each} | Gen{Seed, Size}`. It is the one place that chooses the screen: `screen.New`, or `plain.New` with `-plain`.

## What it shows

- **A machine with payload-free states.** The parser's 14 states carry nothing, and the step is one exhaustive `match` per state over 22 classes, so a new state or class stops the build until each row is written. The data the sequence collects stays beside the machine (C8; Ghostty splits `Parser` from its stream the same way).
- **Laws over the whole domain.** `ghostty.StepMatchesTable` runs every state and every byte (3,584 cases) against Ghostty's table and is proved. `Parser.ParamsStayBounded` (`always`) is decided on the machine's whole graph, so the collector's arrays are never overrun. `Parser.ParamsReachTheLimit` and `Parser.IntermediatesReachTheLimit` (`sometimes`) show the bounds are reached, so they are not tighter than Ghostty's. `vt.SplitDoesNotMatter` shows that pieces of 1 to 16 bytes give the events of the whole stream: the UTF-8 decoder, the OSC string and the text runs carry across a cut. `vt.FastPathMatchesStep` shows that the fast paths give what the machine gives byte by byte.
- **A dense form held to a plain one.** `screen.ScreenMatchesPlain` plays the same generated stream, in the same pieces, on both screens of a small random size (1 to 12 rows, 2 to 41 columns, one to three pages of history) and compares every cell, style, wrap, the cursor, the pen and the title. `screen.StylesAreCounted` is Ghostty's integrity check as a law: each style's count is the cells that hold it plus the pen, and a free id holds nothing. `screen.ScrollbackIsBounded` plays a stream that fills the history, plays it again, and finds that the second time made no cell (the counted growth guard) and that the ring holds no more than the history's pages, the buffer's and one. `width.WidthTableMatchesRanges` checks the table at every code point. `screen.CellFieldsRoundTrip` reads back each field of the packed cell.
- **Mutants.** Each mutant in `testdata/mutants.json` names the law or case that kills it.

## Cases

- `replay-screen`: each command of the screen, written by hand: styles, wide characters and a wide character wrapped at the edge, insert, delete and erase of cells cutting wide characters, insert and delete of lines, a scroll region in origin mode, save and restore, the alternate buffer, autowrap off, scrolling into the history, cursor moves, and unhandled sequences.
- `replay-gen` and `replay-plain`: 60,000 generated bytes of seed 11 on each screen, with one golden.
- `events-sequences`: one of every event kind and every edge of the parser, written by hand.
- `events-gen`: 3,000 generated bytes.
- `events-tally`: 8,000 generated bytes of seed 4, tally only, with dropped sequences and a cut OSC.
- `gen`: 400 bytes of seed 1.
- `usage`, `bad-count`, `bad-flag`: exit 2.

The events goldens agree byte for byte with a Go transliteration of Ghostty's `Parser.zig` and `parse_table.zig`. That transliteration is the parser of the hand-Go control, `cmd/ergo/testdata/vt/control`.

## Control

`cmd/ergo/testdata/vt/control/main.go` is `vt replay` as a person would write it in Go, in the dense form. It has Ghostty's transition table with the decoder in front of it, and it calls the screen directly, with no event list. The screen is Ghostty's: 8-byte cells, a ring of pages, and a counted style set. `TestVTAgreesWithControl` (in `cmd/ergo`, full tier) plays 200 seeds of `vt gen` on the dense screen, the plain screen and the control, and requires the same bytes from all three. Every 25th seed is 1.5 MB, so the history drops pages; `ERGO_VT_SEED=n` replays one seed. The receipt counts per input byte at 2 and 16 MB:

- Dense Ergo is 1.9× the control's instructions and 2.6× its bytes (6.1× the bytes at DT4).
- Plain Ergo is 7.1× dense Ergo's instructions and 176× its bytes.

Since IO1, the runtime reads standard input into the text it hands over, with no 4 KiB pieces and no copy at the end. Since VC1, `Stream.Take` (`let got = p.out` then `p.out = []Event{}`) and std `dense.Counted.Add`'s `c.free = c.free[0:len(c.free) - 1]` move their slice with no copy (types.md, Bindings 2).

Since DT4, `clearRow`, `dropRow` and `moveRow`'s loops lower to Go's `copy` (types.md, Loops and len 8), and `PlayOn` reaches the screen through copies of `Play` and `Apply` whose screen parameter is the provider's adapter, so each screen call is static (types.md, Interface assignability 6). Neither touches the source. Most of the rest is the event list: each `Event` is a boxed value, because the union is over Go's register bound for a flat struct (union-repr.md §5).

## Gaps and choices

- The screen is 24 × 80 with 1 MiB of history (1,632 lines). There is no resize, so no reflow.
- Erased and scrolled-in cells take the default style: no background-colour erase.
- ED 3 (erase the history), tab stops other than every 8 columns, character sets, left and right margins, insert mode, and the modes other than 6, 7, 25 and 1049 are counted as unhandled.
- Only a scroll of the whole main buffer sends lines to the history, and a scroll by n moves at most the region's height.
- One saved cursor serves both buffers; mode 1049 saves into it on the way in.
- A combining mark or other character of no width takes a column: grapheme clusters are out of scope.
- The styles are one set per screen, not one per page as in Ghostty, so dropping a page releases its styles row by row.
- The cells stay one slice of 8-byte records, Ghostty's layout, rather than `dense` columns: a cell's two words are read together on every write.
- Input comes from standard input; there is no live child process (plan §9).
- A parameter of more than 255 digits is kept, clamped. Ghostty's 8-bit digit counter wraps there and drops the parameter.
- OSC data is handed over raw. The screen reads titles from OSC 0 and 2; Ghostty parses every OSC command in the parser.
