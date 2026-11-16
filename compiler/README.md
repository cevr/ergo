# compiler: the Ergo compiler written in Ergo

`compiler/` is the top-level Ergo module (`module compiler`) of the self-hosted compiler (capstone S1 to S6). Its first packages are the formatter of the self-hosting pilot (SH0, SH1), moved here at S1a. The Go formatter (`internal/sketch/format`) stays the product and the oracle until S1c, and it is frozen: a fix goes to both sides, and `internal/selfhostcheck`'s agreement properties are the lock.

The binary holds these packages through `internal/linked` (`docs/tooling.md`, Linked Ergo packages): the row `format` of `linked.Packages` lowers `compiler/api` through the IR into `internal/linked/format/format.go`, and Go calls it. The Go parser is shared until S2d: `internal/hostsyntax` parses, encodes the tree and calls the linked package.

## Layout

- `tree/` (pure): `Item` is a closed union (`Number | Position{At Option[int]} | Bytes | Flag | Missing | Ref | List`), `Tree` an arena of `Node`s addressed by id. `Decode` reads `internal/hostsyntax`'s encoding (grammar in `parse.go`'s doc comment) and fails `SyntaxFailure | Unsupported | BadEncoding`; `Project` writes the tree as text, with each position's line and column computed in Ergo.
- `format/` (pure but for the parser it is handed): the formatter rewritten from `internal/sketch/format`. `format.ergo` holds the contract (`Layouter` with its laws), the failures (`OutputLimit`, `InvalidTree`) and the entries `Source` (parse with the given parser, validate, print) and `Print`; `printer.ergo` and `nodes.ergo` print each node family; `span.ergo` gives each node's Pos/End and the token table; `layout.ergo` is the line/cell state and its pure transition over the closed `Command` union; `comments.ergo`, `align.ergo` (column sections, render, the size limit decided before any text is built) and `text.ergo` (byte and rune helpers); `laws.ergo` checks Idempotent, ShapeKept, CommentsKept and Bounded on an outcome.
- `api/` (pure): the linked package's root, primitive in and out. `Project(encoding)` is the tree's projection; `FormatTree(encoding, limit)` the formatter's outcome; `Laws(src, srcTree, out, outTree)` the source laws over two trees Go parsed; `Steps(commands, limit)` the layout state's outcome.

## Run

```sh
ergo check compiler/api
ergo test compiler/format
ERGO_SELFHOST_SEED=1 go test -count=1 -run '^TestSelfhost(BridgeAgrees|FormatAgrees|LayoutModel)$' ./internal/selfhostcheck
go run ./scripts/linked
```
