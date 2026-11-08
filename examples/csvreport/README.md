# csvreport

A CSV report. `csvreport [-by region|product] [-region name] [-min units] file` reads a sales CSV file whose header names `region`, `product`, `units` and `price` (in any order, any case, among other columns), keeps the sales the flags ask for, groups them by region or product, and prints a table of each group's sales, units and revenue, in key order, then the total. A file that is missing, unreadable, malformed, or has a bad row exits 1 with one line on standard error; a usage error exits 2.

Tag: `default`. It is an original program, not a port. Its goldens were computed by hand from `testdata/sales.csv` and the padding rule below, and then checked against the program.

## Layout

The program is a module of packages (`ergo.mod`: `module csvreport`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `csv/` | The format (RFC 4180, as Go's `encoding/csv` reads it). `read.ergo`: `Rows`, a `Seq[Item]` that yields one row at a time from a four-mode reader (`FieldStart`, `Plain`, `Quoted`, `QuoteSeen`); `Item` is `Parsed{Row} \| Malformed{Line, Column, Reason}`; `ReadAll` turns a `Malformed` into the typed failure `BadCSV`. `write.ergo`: `Format`, `Quote`. `laws.ergo` (three laws), `sample.ergo` (generators' helpers), `tests.ergo`. |
| `report/` | The domain. `sales.ergo`: `Sale`, `Load` (streams `csv.Rows`, stops at the first bad row: `csv.BadCSV \| MissingColumn \| ShortRow \| BadNumber`), `ParseCents`, `Money`. `group.ergo`: the unions `Keep` (`Every`, `InRegion`, `AtLeast`) and `Key` (`ByRegion`, `ByProduct`), `Filter`, `Group`, `Total` (a fold), `Table`. `source.ergo`: the operation type `Source` (`Read` fails `Missing \| Unreadable`; law `ReadIsRepeatable`) and `Report`, which needs it. `laws.ergo` (five laws), `tests.ergo`. |
| `memory/` | `Shelf`, the `report.Source` provider of fixed entries (`Readable \| Broken`), and `Disk`, an `fs.Files` provider for tests. |
| `files/` | `Load`: reads the named files through `fs.Files` into a `memory.Shelf`, once, at the start. |
| `cmd/csvreport/` | The binary: std `flag` for the command line, the host's file system, and the only place that chooses the providers. |

The table's key column is as wide as the widest of its name (`region`, `product`) and the keys; sales, units and revenue are right-aligned in 6, 7 and 12 bytes, with two spaces between columns. Revenue is units times price, in cents, printed with two decimals.

## What it shows

- **A stream with its error as a value.** `csv.Rows` is a `Seq[Item]`: `Load` reads the header and each row as they come and stops at the first bad one, and a `for` that breaks stops the reader (`RowsStopEarly`). A `Seq` has no failure row, so the reader's error is the `Malformed` variant, and `ReadAll` and `Load` turn it into a typed failure.
- **Typed failures to exit codes.** `Report` fails `Missing | Unreadable | csv.BadCSV | MissingColumn | ShortRow | BadNumber`; `run` names each one with no `case _`, so a new failure stops the build until the binary prints it.
- **Exhaustive match over the domain's unions.** `Keep` and `Key` are unions; `Kept`, `KeyOf` and `Table` match every variant.
- **Laws.** csv: `RoundTrip` (format then read is the identity on rows), `LinesCount` (each row knows its first line, quoted line feeds included), `FormatIsStable`. report: `TotalIsFold` (the total equals a loop), `GroupsSumToTotal` (groups add up to the total, keys strictly increase), `FilterKeepsOnly`, `LoadReadsRows` (sales written as CSV load back), `MoneyRoundTrip`. `Source`: `ReadIsRepeatable`. Each is restated as a property test, since laws are checked, not run (#162).
- **A contract the binary binds.** `report.Source` is an operation type; tests bind a `memory.Shelf`, the binary a shelf that `files.Load` read through the host's `fs.Files`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/csvreport/cmd/csvreport -o /tmp/csvreport
/tmp/csvreport -by product -min 2 examples/csvreport/testdata/sales.csv
./bin/ergo test examples/csvreport/csv   # and report, memory, files, cmd/csvreport
go run ./scripts/examples csvreport
```

## Limits found

New gaps (in `gaps-ex-e.md` of the run):

- **G-ex-e-10**: std has no csv package, and Go's `encoding/csv` cannot be imported or bound by an extern, so `csv/` is a hand-written reader and writer.
- **G-ex-e-11**: no formatted output: no width or alignment in interpolation and no `fmt`, so `report.line` pads with `strings.Repeat`, and `Money` builds two decimals by hand; no `strings.Compare` (`cmp.Compare` sorts the groups).

Gaps that other lanes found, met here too:

- Go interop stops at a short pure allowlist (G-ex-f-10, G-ex-f-11): no `import go encoding/csv`.
- Std fs has no in-memory `Files` (G-ex-f-12): `memory.Disk`, and a test-only `locked` provider in `files/tests.ergo`, implement all three operations.
- `try` stands only at the start of an expression (G-ex-f-9): `(try ParseCents("12")) == 1200 && (try …)` needs the parentheses.
- A slice has no `==` (G-ex-d-5): `csv.Same` compares rows field by field.
- A generated string comes from a fixed alphabet (G-ex-d-4) with no comma, quote or line feed, so `csv.Spice` maps letters onto them before the format laws see them.
- A `let` of a union takes its first variant's type (G-ex-b-16): `let mut m mode = FieldStart` and `let key report.Key = …` name the type.
- A provider cannot need a capability its contract does not write (rows.md, Context 2), so `files.Load` reads every named file up front, as okra-how does.
- Laws are not run (#162).
