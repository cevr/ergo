# selfhost: the self-hosting pilot's Ergo module

The v1 self-hosting pilot (wf10 SH0 to SH2) rewrites one compiler package, the formatter, in Ergo and runs it beside the Go original, which stays the default and the oracle. The Go parser is shared: Ergo reads its tree through an encoding. This folder is the pilot's Ergo module; `internal/selfhostcheck` holds the properties that compare the two sides.

## Layout

- `ergo.mod` (`module selfhost`), `go.mod` (`module ergo.local/prototype/selfhost`, which requires the repository by a local replacement so that its Go adapter may import the repository's `internal/sketch/syntax`), `ergo.bind/` (the adapter's binding, from `ergo bind go ergo.local/prototype/selfhost/hostsyntax`).
- `hostsyntax/` (Go): `Parse(src string) string` calls `syntax.Parse` and writes only the parser's facts as encoding v1 (the grammar is in `parse.go`'s doc comment): line starts, every exported field of every node in declaration order, positions with `NoPos` kept apart, strings as length-prefixed bytes, the parser's located failure, or the name of a node type the adapter does not carry. No layout decision is made here, and the formatter is not imported. `TestFamiliesCoverSyntax` holds the family table to the syntax package.
- `tree/` (Ergo, pure): `Item` is a closed union (`Number | Position{At Option[int]} | Bytes | Flag | Missing | Ref | List`), `Tree` an arena of `Node`s addressed by id. `Decode` reads an encoding and fails `SyntaxFailure | Unsupported | BadEncoding`; `Project` writes the tree as text, with each position's line and column computed in Ergo.
- `format/` (Ergo, pure but for the parser it is handed): the formatter rewritten from `internal/sketch/format` (SH1). `format.ergo` holds the contract (`Layouter` with its laws), the failures (`OutputLimit`, `InvalidTree`) and the entries `Source` (parse with the given parser, validate, print) and `Print`; `printer.ergo` and `nodes.ergo` print each node family; `span.ergo` gives each node's Pos/End and the token table; `layout.ergo` is the line/cell state and its pure transition over the closed `Command` union; `comments.ergo`, `align.ergo` (column sections, render, the size limit decided before any text is built) and `text.ergo` (byte and rune helpers); `laws.ergo` checks Idempotent, ShapeKept, CommentsKept and Bounded on an outcome.
- `formatapi/` (Ergo): the library root `ergo lib` exports, primitive in and out. `Project(src)` returns the tree's projection (SH0); `Format(src, limit)`, `FormatTree(encoding, limit)`, `Steps(commands, limit)` and `Laws(src)` return encoded outcomes of the formatter, the layout without parsing, the layout state, and the law verdicts.
- `cmd/format/main.ergo`: the formatter as a stdin filter, the one place that binds the Go parser and the host's streams. `ergo check` checks it; `ergo build` cannot build it yet, for the reason under "Why not examples/".

## Why not examples/

Measured in SH0: `scripts/examples` loads every folder of `examples/` as a program (it needs `cmd/<name>/main.ergo`, `tags`, `cases/`, and a package folder holds only `.ergo` files), and an Ergo module whose go.mod requires `ergo.local/prototype` cannot be built by `ergo build` today: the emitted go.mod replaces `ergo.local/prototype` twice (`go: conflicting replacements for ergo.local/prototype`). The test therefore lowers the library itself and builds one driver whose go.mod replaces the repository once.

## Run

```sh
ERGO_SELFHOST_SEED=1 go test -count=1 -run '^TestSelfhost(BridgeAgrees|FormatAgrees|LayoutModel)$' ./internal/selfhostcheck
go -C internal/selfhostcheck/testdata/selfhost test ./hostsyntax
```
