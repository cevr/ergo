# catalog

A shop's product catalog, published as an XML feed by the XML codec module (`modules/xml`) and read back. `catalog feed N` prints the feed of a sample catalog of N products; `catalog check` reads a feed from standard input and prints its counts, or the line and column where it is malformed, or the path of the value that is not a catalog's; `catalog receipt N` prints the size of the XML and the JSON of N products and the heap allocations of one encode and one decode of each, counted by Go's `testing.AllocsPerRun`.

Tag: `default`. It is an original program, not a port.

## Installing the module

`modules/xml` is a module of its own (`module ergo.dev/xml`), not std: everything it uses, any module may use. The program requires it in `go.mod` (`require ergo.dev/xml v0.0.0`, with `replace ergo.dev/xml => ../../modules/xml`) and imports it as `ergo.dev/xml`, as any program installs an Ergo module. The module uses no Go, so `ergo.mod` trusts nothing. `ergo test` of the program does not run a dependency's tests, as `go test` does not; the examples runner runs the module's own tests and laws in this program's test leg, since it installs the repository's folder.

## Layout

The program is a module of packages (`ergo.mod`: `module catalog`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `catalog/` | The domain: `Catalog`, `Product`, the listing's `Status` and `Event` unions and the machine `Listing` over them (laws `AlwaysRetirable`, `Ends`), `Count`, `Sample`, and `Feed`, the contract a feed's format keeps (law `ReadsBack`), with its typed failure `Unreadable`. It names no format. |
| `xmlfeed/` | `Feed`'s XML provider, on the module's `Encode` and `Decode`; it turns the codec's failures into `Unreadable`. |
| `cmd/catalog/` | The binary. `main.ergo` parses the command (a union, matched exhaustively) and is the one place that binds `catalog.Feed` to `xmlfeed`'s provider. |

## What it shows

- **A module's codec takes std json's path.** `xml.Text` is a sealed operation type with one provider, so `encode(v, t)` on `let t Text = text{…}` is a cell writer: each event is a direct call. A record's key folds at build time (`Name` of a constant key), so `Key("sku")` unfolds to `raw("<sku>")` and `RawJoins`, proved by `ergo test` and written to `ergo.laws`, joins it with the text before it. The buffer is borrowed from std `strings` with the type's size hint, as std json's is, so an encode allocates its result and the element stack, nothing per value: numbers are appended in place by std `strconv`'s appenders, as std json's are.
- **A streaming reader.** `xml.Parser` is a pull reader: `Next` gives one token at a time and keeps only the names of the elements open. `Decode` checks the whole text with it first (`Check`, which builds no token), so no value is built from a text that is not well formed, then reads the value straight from the checked text: a string with no reference to replace is a slice of the input. Its reader holds its place in its own fields: `Decode` binds it to `xml.Cursor`, a cell of a sealed operation type, and lends the cell to `decode[T]` as its `Reader`.
- **Laws, machines and typed failures.** `Listing` is a machine whose laws `ergo test` decides on the whole graph. `Feed.ReadsBack` holds for the provider the binary binds. The module's laws are seeded properties of the codec: round trip, well-formed output, and a malformed document rejected with its offset, line and column. `Unreadable` carries either the place in the text or the value's path; `check` prints whichever it has.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/catalog/cmd/catalog -- feed 3
./bin/ergo run examples/catalog/cmd/catalog -- check < examples/catalog/cases/check/stdin
./bin/ergo test modules/xml
go run ./scripts/examples catalog
```

## Cases

`feed` (three products), `check` (a feed written by hand: an XML declaration, comments, a CDATA section, character references, an empty element and a field the catalog does not have), `malformed` (an end tag that closes the wrong element: its line and column), `shape` (a stock that is not an integer: its path), `receipt` (two products) and `usage`.

## Limits found

- Input is a whole string: chunked input waits for `io.Source` (SJ0).
