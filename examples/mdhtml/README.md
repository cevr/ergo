# mdhtml

A [CommonMark 0.31.2](https://spec.commonmark.org/0.31.2/) converter written in Ergo. `mdhtml` reads a file, or standard input when no file is named, and writes the HTML on standard output. It prints every example of the specification as the specification does.

```sh
mdhtml [-unsafe] [-highlight] [-title T] [FILE]
mdgen -seed S -n N       # the receipts' workload: N sections for seed S
mdgen -blocks FILE       # how many blocks FILE reads as
```

```text
$ printf '# Hi *you*\n\n- a `b`\n- [go](https://go.dev)\n' | mdhtml
<h1>Hi <em>you</em></h1>
<ul>
<li>a <code>b</code></li>
<li><a href="https://go.dev">go</a></li>
</ul>
```

- **Safe by default, as cmark is.** Raw HTML (blocks and tags in a line) is written as `<!-- raw HTML omitted -->`. A `javascript:`, `vbscript:` or `file:` link, or a `data:` link that is not a png, gif, jpeg or webp image, gets an empty `href`. `-unsafe` trusts the author and keeps both, which is what the specification's examples expect.
- `-title T` wraps the HTML in a whole page with the title `T`.
- `-highlight` marks Go's keywords in code blocks of language `go`.

Tag: `default`. It is an original program, not a port. Its design is commonmark.js's: the same two phases, the same block starts in the same order, the same delimiter and bracket stacks. Its goldens were made by running the program, and the Go control prints the same.

## What it reads, and how

- **The whole grammar:**
  - blocks: ATX and setext headings, paragraphs, block quotes, bullet and ordered lists (tight and loose, nested), fenced and indented code, the seven kinds of HTML block, thematic breaks and link reference definitions;
  - inlines: code spans, emphasis and strong emphasis by CommonMark's delimiter-run rules, links and images (inline, full, collapsed and shortcut references), autolinks, raw HTML, hard and soft line breaks, backslash escapes, and entity and numeric character references (all 2125 HTML5 names);
  - tabs as tab stops of 4, and lazy continuation lines.
- **Blocks in one slice.** `markdown.parseBlocks` keeps every block as a record in one slice, linked by ids: parent, first and last child, siblings. Each line first continues the open blocks it can, then tries the eight block starts, then adds its text to the deepest open block. A list is tight when no item and no block in an item ends with a blank line before its next sibling.
- **Inlines in one slice.** `markdown.parseInlines` reads each paragraph's and heading's text into one shared slice of inlines, linked the same way. It has CommonMark's two stacks: the delimiter stack of `*` and `_` runs, matched by `processEmphasis` with its rule of three, and the bracket stack of `[` and `![`, which a `]` matches against an inline target or a reference.
- **The HTML tree.** `markdown.Nodes` turns the blocks and inlines into a tree of `html.Node`, and `html.RenderAll` writes it with CommonMark's line breaks.

## What it shows

- **A machine.** `markdown.HtmlBlock` is `machine[HtmlEnd, Seen]`. Its state is the kind of an open HTML block:
  - `RawText`, `Comment`, `Instruction`, `Declaration` and `CData` end at a line that holds their end marker, together with that line;
  - `TagBlock` and `LoneTag` end at a blank line, before it.

  `HtmlStep` matches the line, then the kind, each exhaustively with no `case _`. Its laws are `TagsEndAtABlankLine`, `MarkersEndWithTheLine` and `NoMarkerStays`.
- **Escaping decided by types.** `html.Element`'s fields are unexported, so a program can only build HTML with `El`, `ElAttrs`, `A` and `Img`:
  - `Render` escapes every `Text` and every attribute value;
  - `ElAttrs` drops `href` and `src`;
  - `A` and `Img` take a `Url`, which only `Href` (safe) and `Trusted` build;
  - raw HTML is a node of its own, `Raw` or `RawBlock`, so the place that keeps it is where the tree is built.

  The law `HrefIsSafe` holds `Href` to cmark's rule.
- **An operation type with a law.** `markdown.Highlighter { Highlight(lang, code) []html.Node, law KeepsTheText }`: whatever a highlighter marks up, the text of the code is unchanged. `Nodes` has the row `needs Highlighter` and no other, and `main` binds `NoColor` or `Keywords` from the flag.
- **Unions and exhaustive `match`.** These are all unions, and the checker holds every `match` over them total:
  - the block kinds `Kind`, the inline kinds `IKind`;
  - `Trust` (`Safe | Unsafe`);
  - `html.Node`, and the machine's `HtmlEnd`, `Seen` and `Ending`.
- **Laws over whole documents** (`markdown/laws.ergo`):
  - `EscapedPunctuationIsText`, `CodeSpanKeepsText`, `EntitiesAreTheirText`;
  - `BlankLineSeparates`: blocks of a pool joined by blank lines read as they read apart;
  - `DefinitionsAnywhere`: a definition before or after its link, in any case;
  - `EmphasisNests`, `TightListsHaveNoParagraphs`, `KeywordsMarkEachKeyword`;
  - `workload.DocumentHasEveryBlock`: the receipts' workload reaches every block and span.
- **Typed failures at the edge.** `source` reads the file or standard input and catches `fs.NotFound`, `fs.Denied`, `fs.Failed` and `io.ReadFailed` by name, each to an `os.Exit` with its own message.

## Layout

- `html/`: the output tree. `node.ergo` holds the union `Node`, the builders, `Url` with `Href` and `Trusted`, the writer, `Render`, `TextOf`, `Escape` and `Unescape`. `laws.ergo` holds `EscapeRoundTrip`, `EscapeHasNoMarkup`, `TextSurvivesRendering` and `HrefIsSafe`.
- `markdown/`: the reader.
  - `blocks.ergo`: the block phase.
  - `rawhtml.ergo`: the HTML block machine and the tag scanners.
  - `inline.ergo`: the inline phase, links and references.
  - `entity.ergo` and `entities.ergo`: character references; the table is generated from Go's `html/entity.go` (BSD).
  - `render.ergo`: `Highlighter`, `Trust`, `Parse`, `Nodes`, `Render`.
  - `laws.ergo`.
- `workload/`: the receipts' document and its law.
- `cmd/mdhtml/main.ergo` and `cmd/mdgen/main.ergo`: the binaries, the only places that choose providers.
- `testdata/commonmark-spec-9103e341/`: the specification at 0.31.2, unchanged, with its CC-BY-SA 4.0 license (`SOURCE` names the pin).

## The Go control

`cmd/ergo/testdata/mdhtml/control` is the same design in hand-written Go: the same block and inline stores, the same stacks, the same tree and writer, the same flags.

`TestMdhtmlAgreesWithControl` (cmd/ergo, full tier) runs both binaries on:
- every example of the specification: with `-unsafe` each must print the example's HTML byte for byte, and in safe mode both must print the same;
- seeded documents spliced from the examples' lines, some indented, quoted or made list items, with LF, CRLF and CR line ends, in each mode;
- the workload at two sizes.

It also checks the entity table against Go's `html.UnescapeString`.

The receipts are files of the run, not tests. They measure instructions, allocations, GC share and peak memory per byte and per block, against the control.

## Cases

- `document`: every block and span, safe mode.
- `page`: `-title` with characters to escape, and `-highlight` on a `go` and an `sh` block.
- `file`: a named file; standard input is not read.
- `missing`: a file that is not there, status 1.
- `help`: `-help`, status 0.
- `usage`: two files, status 2.
