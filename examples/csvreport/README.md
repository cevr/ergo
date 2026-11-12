# csvreport

A CSV report. `csvreport [-by site|device] [-site name] [-min hours] file` reads a meter CSV file whose header names `site`, `device`, `hours` and `kw` (in any order, any case, among other columns), keeps the readings the flags ask for, groups them by site or device, and prints a table of each group's readings, hours and energy, in key order, then the total. A file that is missing, unreadable, malformed, or has a bad row exits 1 with one line on standard error; a usage error exits 2.

Tag: `default`. It is an original program, not a port. Its goldens were computed by hand from `testdata/meters.csv` and the padding rule below, and then checked against the program.

## Layout

The program is a module of packages (`ergo.mod`: `module csvreport`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `report/` | The domain. `readings.ergo`: `Reading`, `Load` (streams std `csv.Rows`, stops at the first bad record: `csv.ParseError \| MissingColumn \| BadNumber`), `ParseHundredths`, `Hundredths`. `group.ergo`: the unions `Keep` (`Every`, `AtSite`, `AtLeast`) and `Key` (`BySite`, `ByDevice`), `Filter`, `Group`, `Total` (a fold), `Table`. `source.ergo`: the operation type `Source` (`Read` fails `Missing \| Unreadable`; law `ReadIsRepeatable`) and `Report`, which needs it. `laws.ergo` (five laws), `tests.ergo`. |
| `memory/` | `Shelf`, the `report.Source` provider of fixed entries (`Readable \| Broken`). |
| `files/` | `Load`: reads the named files through `fs.Files` into a `memory.Shelf`, once, at the start. |
| `cmd/csvreport/` | The binary: std `flag` for the command line, the host's file system, and the only place that chooses the providers. |

The table's key column is as wide as the widest of its name (`site`, `device`) and the keys, in characters; readings, hours and energy are right-aligned in 8, 7 and 12 characters (`strings.PadRight`, `strings.PadLeft`, as Go's `%-*s` and `%8s`), with two spaces between columns. Energy is hours times kW, in hundredths of a kWh, printed with two decimals.

## What it shows

- **A stream with its error as a value.** std `csv.Rows` is a `Seq[csv.Row]` (`Parsed{Fields, At} | Malformed{Error}`): `Load` reads the header and each record as they come and stops at the first bad one. A `Seq` has no failure row, so the reader's error is the `Malformed` variant, and `Load` fails with the `csv.ParseError` it carries. The reader is Go's `encoding/csv`: every record has as many fields as the header, so a short row is its "wrong number of fields".
- **Typed failures to exit codes.** `Report` fails `Missing | Unreadable | csv.ParseError | MissingColumn | BadNumber`; `run` names each one with no `case _`, inside the `with` that binds the shelf, so a new failure stops the build until the binary prints it.
- **Exhaustive match over the domain's unions.** `Keep` and `Key` are unions; `Kept`, `KeyOf` and `Table` match every variant.
- **Laws that run.** report: `TotalIsFold` (the total equals a loop), `GroupsSumToTotal` (groups add up to the total, keys strictly increase), `FilterKeepsOnly`, `LoadReadsRows` (readings written with `csv.Format` load back), `FixedRoundTrip`; `ergo test` runs them over generated values, whose strings hold commas, quotes and line feeds. The csv format's own laws are std's.
- **A contract the binary binds.** `report.Source` is an operation type; tests bind a `memory.Shelf`, the binary a shelf that `files.Load` read through the host's `fs.Files`; the tests' files are an `fstest.Memory`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/csvreport/cmd/csvreport -- -by device -min 2 examples/csvreport/testdata/meters.csv
./bin/ergo test examples/csvreport/report   # and memory, files, cmd/csvreport
go run ./scripts/examples csvreport
```

## Limits found

- **G-exup-6** (closed in batch 13, fu6c): each `csv.Parsed` carries `At`, where each field starts (Go's `Reader.FieldPos`), so a `BadNumber` names the line and column of the bad field, `csvreport: meters.csv:4:5: hours x is not a number`, in the form of the reader's own errors. Before, it named its record's count from the header, which is not the line when a quoted field spans lines.
- **G-exup-1** (closed in batch 13): `Source.ReadIsRepeatable` runs in `memory` against `memory.Shelf`. The std law `fs.Files.ReadIsRepeatable` runs against the test disk `locked` of `files`: its `fstest.Memory` is built by `fstest.NewMemory()`, an empty disk per case (fu6a: a generator builds a type by its constructor).
- A test-only `locked` provider in `files/tests.ergo` wraps an `fstest.Memory`, which never fails Denied.
- A provider cannot need a capability its contract does not write (rows.md, Context 2), so `files.Load` reads every named file up front, into a `memory.Shelf` that the contract's operations then read.

Closed since ex-e: G-ex-e-10 (std `csv`), G-ex-e-11 (`strings.PadLeft`/`PadRight`, `strings.Compare`; `Hundredths` still builds two decimals by hand, which is exact on hundredths), G-ex-f-12 (`fstest.Memory`), G-ex-d-5 (`==` on slices), G-ex-d-4 (generated strings hold the format's bytes), G-ex-b-16 (`let key = if …`), #162 (laws run).
