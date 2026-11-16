# compiler: the Ergo compiler written in Ergo

`compiler/` is the top-level Ergo module (`module compiler`) of the self-hosted compiler (capstone S1 to S6). Its first packages are the formatter of the self-hosting pilot (SH0, SH1), moved here at S1a. The Go formatter (`internal/sketch/format`) stays the product and the oracle until S1c, and it is frozen: a fix goes to both sides, and `internal/selfhostcheck`'s agreement properties are the lock.

The binary holds these packages through `internal/linked` (`docs/tooling.md`, Linked Ergo packages): the row `format` of `linked.Packages` lowers `compiler/api` through the IR into `internal/linked/format/format.go`, and Go calls it. The Go parser is shared until S2d: `internal/hostsyntax` parses, encodes the tree and calls the linked package.

## Layout

- `tree/` (pure): `Item` is a closed union (`Number | Position{At Option[int]} | Bytes | Flag | Missing | Ref | List`), `Tree` an arena of `Node`s addressed by id. `Decode` reads `internal/hostsyntax`'s encoding (grammar in `parse.go`'s doc comment) and fails `SyntaxFailure | Unsupported | BadEncoding`; `Project` writes the tree as text, with each position's line and column computed in Ergo.
- `format/` (pure but for the parser it is handed): the formatter rewritten from `internal/sketch/format`. `format.ergo` holds the contract (`Layouter` with its laws), the failures (`OutputLimit`, `InvalidTree`) and the entries `Source` (parse with the given parser, validate, print) and `Print`; `printer.ergo` and `nodes.ergo` print each node family; `span.ergo` gives each node's Pos/End and the token table; `layout.ergo` is the line/cell state and its pure transition over the closed `Command` union; `comments.ergo`, `align.ergo` (column sections, render, the size limit decided before any text is built) and `text.ergo` (byte and rune helpers); `laws.ergo` checks Idempotent, ShapeKept, CommentsKept and Bounded on an outcome.
- `api/` (pure): the linked package's root, primitive in and out. `Project(encoding)` is the tree's projection; `FormatTree(encoding, limit)` the formatter's outcome; `Laws(src, srcTree, out, outTree)` the source laws over two trees Go parsed; `Steps(commands, limit)` the layout state's outcome.
- `syntax/` (pure): the scanner and parser on SELFLAYOUT's layout (capstone S2a, S2b), moved here from their run dirs. The scanner writes token columns (`token.ergo`: a one-byte kind and a start, `End` by a second scan; `scan.ergo`: dispatch on the first byte, keywords by length and compare), `comments.ergo` and `lines.ergo`; the parser (`parse*.ergo`, `diag.ergo`) writes `Tree`, 13-byte rows with one `extra` pool (`tree.ergo`), and `view.ergo` reads a row as the closed unions `Expr`, `Stmt`, `Decl`, `Type` and writes one back (`View`, `Put`). `skip "reason"` is a statement only before a string literal: `skip` is no scanner keyword. The laws are in `scan_laws.ergo` and `tree_laws.ergo`.
- `syntax/dump/` (pure): `Scan(src)` and `Parse(src)`, the canonical dumps of one scan and one parse (the parse through the view). They are the linked package `internal/linked/syntax` (the row `syntax` of `linked.Packages`), which `internal/sketch/syntax`'s `TestScanAgreesWithGo` and `TestParseAgreesWithGo` compare in process with the Go scanner and parser.
- `check/store/` (pure): the self-hosted checker's tables (capstone S3a): id spaces, object tables, the package scope, the type pool, the declarations with their fingerprints (`Prints`: `Fp` over a declaration's tokens, `Ifp` over what an importer sees), each file with its laws.
- `check/bind/` (pure): collect and bind on the store (capstone S3b). `collect.ergo` puts each file's package-level declarations into the store; `parents.ergo` is the parent column over the package's rows (`PNode` = file base + row), with each row's children; `bodies.ergo` resolves every identifier (`Uses`) and marks the body's declarations (locals and laws) on one undo stack (`dense.Stack`); `prints.ergo` gives each declaration its prints. `internal/selfhostcheck`'s `TestCompilerBindAgreesWithChecker` holds Uses and Defs equal to the Go checker's over the corpus.
- `check/types/` (pure): the kinds of bracket parameters and the types of declarations, on the store (capstone S3t). `kinds.ergo` solves each generic type's parameter kinds (type, error row, capability row) by union-find over positions, links through local generic types and the receiver rule (196m); `resolve.ergo` resolves a type expression to a pool type and prints it as the Go checker's `Type.String`; `records.ergo` lists the record declarations, inline variants included. Other packages' types come in as a table until S3c's frozen interfaces. `check/store/types_api.ergo` is S3t's addition to the store: the pointer, tuple, signature, array and foreign kinds, the own and entry marks, and readers. `TestCompilerTypesAgreeWithChecker` and `TestCompilerTypesProperty` hold the kinds and the record field types equal to the Go checker's.
- `cmd/kinddump`, `cmd/typedump`: print the solved kinds and the resolved record field types of the named files, the agreement tests' dumps.
- `cmd/binddump`: prints bind's declarations, uses (`-uses`) or declarations with locals (`-locals`) of the named files, the agreement test's dump.
- `cmd/parsedump`, `cmd/scandump`: print the dumps of the named files, one `F path` line before each, and with `-n` the counts the receipts use.

## Run

```sh
ergo check compiler/api
ergo test compiler/format
ergo test compiler/syntax
ergo test compiler/check/store
ergo test compiler/check/bind
ergo test compiler/check/types
go test -count=1 -run '^Test(Scan|Parse)AgreesWithGo$' ./internal/sketch/syntax
ERGO_SELFHOST_SEED=1 go test -count=1 -run '^TestSelfhost(BridgeAgrees|FormatAgrees|LayoutModel)$' ./internal/selfhostcheck
ERGO_TEST_CLI=bin/ergo go test -count=1 -run '^TestCompiler(Store|Bind|Types)' ./internal/selfhostcheck
go run ./scripts/linked
```
