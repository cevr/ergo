# mdhtml

A converter from a subset of Markdown to HTML. `mdhtml` reads a file, or standard input when no file is named, and writes the HTML on standard output. `-title T` wraps the HTML in a whole page with the title `T`; `-highlight` marks Go's keywords in code blocks of language `go`.

```text
$ printf '# Hi *you*\n\n- a `b`\n- [go](https://go.dev)\n' | mdhtml
<h1>Hi <em>you</em></h1>
<ul><li>a <code>b</code></li><li><a href="https://go.dev">go</a></li></ul>
```

The subset: ATX headings (one to six `#` and a space), paragraphs (a blank line ends one), bullet lists (`- `, `* `, `+ `) and numbered lists (`3. `, kept as `start="3"`), a plain line after an item continues it, fenced code with an info string (an unclosed fence runs to the end, as in CommonMark), rules (`---`, `***`, `___`), and inside a line code spans, `**strong**`, `__strong__`, `*em*`, `_em_`, `[text](url)` links and a backslash before a marker. Text is escaped by type, and a link whose scheme is not `http`, `https` or `mailto` goes to `#`.

What it leaves out of CommonMark: nested lists and blocks inside items, block quotes, setext headings, indented code, HTML blocks, reference links, images, and the delimiter-run rules of emphasis (`*a **b** c*` is three emphases, and `2 * 3 * 4` has one). A bullet of another marker continues a bullet list. A link's URL ends at the first `)`.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and then checked line by line by hand against the rules above.

## Layout

The program is a module of packages (`ergo.mod`: `module mdhtml`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `html/` | The output tree. `node.ergo` (the union `Node`; `Element` has unexported fields, so only this package builds one; `El`, `ElAttrs`, `A`, the link target `Url` that only `Href` builds; `Render`, `TextOf`, `Escape`, `Unescape`), `laws.ergo` (`EscapeRoundTrip`, `EscapeHasNoMarkup`, `TextSurvivesRendering`, `HrefIsSafe`), `tests.ergo`. |
| `markdown/` | The reader. `line.ergo` (the union `Line` and `Classify`, what one line is), `blocks.ergo` (the union `Block`, the machine `Blocks` over the union `Mode`, `Step`, `Read`), `inline.ergo` (`Inline`, the spans of a line), `render.ergo` (the operation type `Highlighter` with the law `KeepsTheText`, its providers `NoColor` and `Keywords`, `Node` and `Render`), `laws.ergo` (`EndIsDone`, `BlankSeparates`, `FenceKeepsLines`, `PlainTextStaysText`, `EscapedMarkersAreText`), `tests.ergo`. |
| `cmd/mdhtml/` | The binary. `main.ergo` reads the flags and binds the host's streams and files and the `Highlighter` that `-highlight` chooses. Its tests run `run` over an `io.Input`, an `io.Recorder` and an `fstest.Memory` of one file. |

## What it shows

- **A machine for block state.** `markdown.Blocks` is `machine[Mode, Signal]`: the modes `Idle`, `InPara`, `InList` and `InCode`, the signals `Got{Raw}` and `End`. `Step` matches the signal, then the mode, then the kind of line, each exhaustively with no `case _`: adding a kind of line fails to check until every mode decides it. The decisions say what happened: `Enter` when a block opens, `Stay` when it continues, `Emit` with the blocks a line closes, `Ignore` for a blank line in `Idle`, `Done` with the open block at the end. `Read` runs the machine with `machines.Run` and joins what it emitted.
- **Escaping decided by types.** `html.Element`'s fields are unexported, so a program can only build HTML with `El`, `ElAttrs` and `A`. `Render` escapes every `Text` and every attribute value. `ElAttrs` drops `href` and `src`, and `A` takes a `Url`, whose one field is unexported, so every link passed through `Href`'s scheme check. The law `HrefIsSafe` states it.
- **An operation type with a law.** `markdown.Highlighter { Highlight(info, code) []html.Node, law KeepsTheText }`: whatever a highlighter marks up, the text of the code is unchanged. `Node` has the row `needs Highlighter` and no other, so `Render` uses that one capability and nothing else; `main` binds `NoColor` or `Keywords` from the flag, and the tests bind each.
- **Laws over the reader.** `EndIsDone` (the end is `Done` with the closed blocks, in every mode), `BlankSeparates` (outside a fence, a blank line between two texts reads as the two texts read apart), `FenceKeepsLines`, `PlainTextStaysText`, `EscapedMarkersAreText`, and the `html` laws. `ergo test` runs each. The laws over documents build them from a pool of lines of every kind, picked by generated ints (`Doc`).
- **Typed failures at the edge.** `source` reads the file or standard input and catches `fs.NotFound`, `fs.Denied`, `fs.Failed` and `io.ReadFailed` by name, each to an `os.Exit` with its own message.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
printf '# Hi\n' | ./bin/ergo run examples/mdhtml/cmd/mdhtml -- -title Hi
./bin/ergo test examples/mdhtml/markdown
go run ./scripts/examples mdhtml
```

## Cases

`document` (every block and span, escapes, a link with a bad scheme, a seven-`#` line, an unclosed fence), `page` (`-title` with characters to escape, and `-highlight` on a `go` and an `sh` block), `file` (a named file; standard input is not read), `missing` (a file that is not there: status 1), `help` (`-help`, status 0) and `usage` (two files: status 2).

## Limits found

Each gap has its repro in the run dir's `examples-wave/gaps-ex-a.md`.

- **G-exup-2 (new).** `machines.Next(Idle, d)` with `d` a `Decision[Mode, []Block]` is S108 ("does not match Decision[S, O]"): inference takes `S` from the variant `Idle`, not the union `Mode` that `d` names, so `moved` writes `machines.Next[Mode, []Block](Idle, d)`.

Closed in batch 12: **G-ex-a-9** (V5: a step `Emit`s the blocks it closes, so no `Mode` carries the closed blocks and no step copies them; `Read` runs the machine with `machines.Run`, V2); **G-ex-a-10** (S2 `slices.Concat`, `slices.Slice`; with the value rule, `plus`, `appended` and `continued` are gone); **G-ex-a-3** here (`Read` has no end match to fill in: its impossible outcomes are `Unreachable`, K5); **G-ex-a-7 and G-ex-a-8** (T1: `ergo run DIR -- ARGS` passes the flags); the property tests that restated the laws (V1 runs them; the steering moved into the laws, and T4's strings reach the markup characters the old tests added by hand); the hand `fs.Files` of `cmd/mdhtml`'s tests (U1 `fstest.NewMemory`).
