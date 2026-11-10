# logstat

A web server access log analyzer. `logstat` reads the logs it is given, or standard input, and writes how many requests there were, the time they span, the bytes sent, the status classes, the mean and the 50th, 90th and 99th percentiles of the time taken, and the most frequent paths. A line it cannot read is told on standard error as `file:line:column: what is wrong`, and the rest is still read.

```text
$ logstat -top 2 access.log
access.log:9:17: want the time in [ ]
access.log:11:18: the time "08/Nov/2026 10:00:08" is not in the layout 02/Jan/2006:15:04:05 -0700
access.log:13:80: the time taken "0.1234567" is not a number
requests  10 (3 bad lines)
from      2026-11-08T09:59:59Z
to        2026-11-08T10:00:09Z
bytes     6829
status    2xx 7  3xx 1  4xx 1  5xx 1
seconds   mean 0.316525  p50 0.050  p90 0.300  p99 2.500
top 2 by path
  4  /api/users
  1  /
```

The flags: `-json` writes the summary as one line of JSON; `-top n` (default 5) is how many keys to list; `-by key` counts by `path` (the target without its query, the default), `addr`, `status` or `method`; `-strict` makes the status 1 when a line cannot be read. Go's flag forms work: `--json`, `-top=3`.

A line is the combined log format cut to the fields logstat reads, with the time taken in seconds at the end (nginx's `$request_time`):

```text
203.0.113.9 - - [08/Nov/2026:10:00:00 +0000] "GET /api/users?id=7 HTTP/1.1" 200 512 0.120
```

The size may be `-` (no body). The time taken has at most six decimals and is read exactly, as microseconds, with no float. Fields may be separated by more than one space; a quote inside the request is escaped with a backslash. Percentiles are nearest-rank: the smallest value that at least p percent of the sample is at or below. Times are written in UTC.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and then checked line by line by hand: each column, count, sum, mean and percentile was worked out from the input.

## Layout

The program is a module of packages (`ergo.mod`: `module logstat`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `access/` | The log's domain. `request.ergo` (`Request`, the union `Class` of status classes, `Path`), `parse.ergo` (`Parse` over a `*scanner` borrow, and its failures `Missing`, `BadNumber`, `BadTime`, `BadRequest`, `Trailing`, each with a column), `problem.ergo` (the union `Problem` of those failures, `ColumnOf`, `Describe`), `format.ergo` (`Format`, the inverse of `Parse`, `Seconds`, `Valid`), `gen.ergo` (`Plain`, which makes a generated request Valid, and `Damaged`, which breaks its line, for the laws), `laws.ergo`, `tests.ergo`. |
| `stats/` | The arithmetic, with no log in it. `percentile.ergo` (`Percentiles` over a `slices.Ascending`, `Mean`), `count.ergo` (`Tally`, `Rank`), `laws.ergo`, `tests.ergo`. |
| `report/` | `summary.ergo` (the union `Key` that `-by` names, `Summary`, `Summarize`), `text.ergo` (the union `Style`, `Write`, `Text`), `json.ergo` (`Encode`, `Quote`, over std json's `Text`), `laws.ergo`, `tests.ergo`. |
| `cmd/logstat/` | The binary. `main.ergo` reads the flags, reads each file (or standard input) line by line, tells the bad lines, and binds the host's streams and files. Its tests run `run` over an `io.Input`, an `io.Recorder` and a Files provider of three files. |

## What it shows

- **Precise typed failures, told by one exhaustive match.** `access.Parse` fails `Problem`, the named row of five records, each with the column of the field it could not read. In the binary, `catch { case err: … }` binds the whole row, which is a value of the union `access.Problem`, so `ColumnOf` and `Describe` each name every failure once, with no `case _`. A new failure fails to check until both say what it is.
- **A sorted sample as a type.** `stats.Percentiles` takes a `slices.Ascending[int]`, which only `slices.Ascend` builds, so nearest-rank indexing cannot meet an unsorted slice.
- **Laws over the parser, the arithmetic and the encoder.** `access.FormatParses` (a Valid request's line reads back as the request), `access.ColumnIsInTheLine` (a failure points into the line, for any text), `stats.PercentileIsASampleValue`, `stats.PercentileIsMonotone`, `stats.PercentileCovers`, `stats.RankKeepsEveryKey`, `stats.RankIsOrdered`, `report.QuoteReadsBack` (checked against Go's `strconv.Unquote`), `report.QuoteIsSafe`, `report.KeyNameReadsBack` and `report.EveryRequestIsCounted`. `ergo test` runs them. The laws over requests take a generated `access.Request`, whose time `time.NewTime` builds (std's constructor of `Time`, fu6a): `FormatParses` makes it Valid with `access.Plain`, `ColumnIsInTheLine` breaks its line with `access.Damaged`, and `EveryRequestIsCounted` takes any slice of them. The generated time found that `Valid` claimed a round trip for a time with a fraction of a second, which the log's layout does not write; `Valid` now asks for a whole second; the arithmetic laws make percents and repeating keys of their generated ints (`stats.Percent`, `stats.Keys`). Mutants of the encoder and the tally are killed (`logstat-mutants.json` of the ex-upgrade unit, 3 of 3).
- **Unions for choices.** The `-by` value becomes a `report.Key`, the output style a `report.Style`, and a status a `access.Class`; each is matched exhaustively.
- **Capabilities as values.** A file's text is bound as `io.Stdin` (`io.Input{Text: text}`) for the one `read` function, so a file and standard input are read by the same code; the tests bind a Recorder and a Files provider.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/logstat/cmd/logstat -- -json < examples/logstat/cases/summary/stdin
./bin/ergo test examples/logstat/access
go run ./scripts/examples logstat
```

## Cases

`summary` (every field form, a time with an offset, a blank line, three kinds of bad line), `json` (`--json -by addr -top=3`), `strict` (`-strict -by method`: status 1 after the summary), `files` (two files, one with a bad line), `missing` (a file that is not there: status 1), `empty` (no input, `-json`), `badkey` (`-by host`: status 2) and `help`.

## Limits found

Each gap has its repro in the run dir's `examples-wave/gaps-ex-a.md`.

- **G-ex-a-11 (std behaviour differs from Go).** A `time.Time` keeps no zone offset: a time parsed with `+0100` is written back with `+0000`. logstat writes every time in UTC, and the law `FormatParses` holds for UTC lines only.
- **G-ex-a-12 (performance).** `slices.Ascending` has no length or index, and `Items` copies the sample, so `Percentiles` takes every percentile in one call to copy it once.
- **G-ex-a-14 (closed by S-json).** `report.Encode` writes over std json's HTML `Text` (Go's escapes of <, > and &, `Null` for an empty span), in the field order Go's struct gives; a byte of invalid UTF-8 is `\udcXX`, where Go writes `\ufffd`, so the text reads back as the bytes.
- **Not a gap, found on the way.** `number` is a predeclared constraint, so a function named `number` is S108 (types.md, Generics 2); and `try` must start an expression (`1000000 * try f()` is S001), so the value is bound first.

Closed in batch 13: G-exup-7 (fu6a: `time.NewTime` builds a generated `time.Time`, so the laws take a `Request` in place of a seed).

Closed in batch 12: G-ex-a-13 (`maps.GetOr`: `Tally` is one line per key), G-ex-a-1 here (S3: `Quote` decodes runes and replaces invalid UTF-8 as Go does), G-ex-a-3 here (K: `key` is `Unreachable` for a name `-by` refuses), the named row `access.Problem` (L2), hex literals (L1), `strings.PadLeft` + `strconv.FormatInt` for `\u` escapes, `fstest.Memory` for the binary's tests (the denied file became a real directory), `ergo run DIR -- ARGS` (T1), and the property tests that restated the laws (V1: the laws run, with the steering moved into them).
