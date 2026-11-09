# cron

A cron expression parser and scheduler. `cron next EXPR` prints the expression's canonical form and the next times it fires; `cron run TABLE` reads a table of jobs and prints each firing from `-from` to `-until`, on a virtual clock that jumps from one firing to the next, or with `-live` on the host's clock, sleeping until each minute. Times are minutes in UTC. It is an original program: its goldens were written from its own output and checked by hand against a calendar.

Tag: `default`.

## Layout

The program is a module of packages (`ergo.mod`: `module cron`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `schedule/` | The domain. `civil.ergo` (`Instant`, and `Civil`, its reading through std `time.Calendar` and `time.Date`), `expr.ergo` (`Field`, `Expr`, `Parse` with five typed failures, `Format`), `next.ergo` (`Matches`, `Next`, `Upcoming`), `table.ergo` (`ParseTable`), `run.ergo` (`Job`, `Firing`, `Run`, which needs `time.Clock` and `Scheduler`), `laws.ergo` (the laws and the brute-force reference) and `tests.ergo`. |
| `cmd/cron/` | The binary. `main.ergo` reads the two commands with `flag.ParseCommand` into the program's own `Cmd` union, binds the host's clock, `Scheduler`, file system and standard output, and chooses std `testing.NewVirtualClock` for a run that is not `-live`: the clock jumps to each firing, which is what the run simulates. |
| `testdata/jobs` | A table of six jobs. |

## The expression

Five fields: minute (0-59), hour (0-23), day of the month (1-31), month (1-12 or `jan`-`dec`), day of the week (0-7 or `sun`-`sat`; 0 and 7 are Sunday). A field is a list of parts separated by commas; a part is `*`, a value or a range `a-b`, each with an optional step `/s` (`5/20` is 5, 25, 45). The macros `@yearly`, `@annually`, `@monthly`, `@weekly`, `@daily`, `@midnight` and `@hourly` stand for their five fields. When neither day field is `*` (or `*/s`), a day matches when either matches; otherwise both must (Vixie cron). An expression that never fires, such as `0 0 30 2 *`, has no next time: `Next` looks 28 years ahead, the period after which every 29th of February comes back on each weekday.

## What it shows

- **Laws checked against a brute-force reference.** `NextIsAfter` (the next firing is after the minute asked), `NextFires` (the expression fires then) and `NextIsTheSmallest` (no minute in between fires) hold `Next`, which skips whole months, days and hours, to `Brute`, which tries every minute of a 40-day window. `FormatParseRoundTrip` holds the printer to the parser, and `CivilRoundTrip` holds the calendar. Each law draws an expression of every kind of part, and a minute from 1970 to 2100, from a seeded `Random`; the test `DrawnExpressionsFireInTheWindow` shows that most drawn expressions fire within the window, so the reference compares a time on most cases.
- **Typed failures.** `Parse` fails `FieldCount | BadValue | OutOfRange | BadStep | BadRange`, each with its field; `ParseTable` fails `BadLine` with the line number and the reason, and `run` names each by `case`, with no `case _`.
- **One `Run`, two clocks.** `Run[R](jobs, until, fire func(f Firing) needs R) needs time.Clock | Scheduler | R` sleeps on whatever `Scheduler` its context binds. The binary binds the host's clock and `NewLiveScheduler()` for `-live`, and `testing.VirtualClock` otherwise; the tests bind `testing.VirtualClock`. The callback's row `R` passes through, so `fire` may print.
- **A command line as a union.** `Cmd = NextCmd | RunCmd`, built by `flag.ParseCommand`, and matched exhaustively in `run`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/cron/cmd/cron -- next "*/15 9-17 * * mon-fri" -n 3
./bin/ergo run examples/cron/cmd/cron -- run examples/cron/testdata/jobs -from "2026-11-30 08:00" -until "2026-11-30 10:00"
./bin/ergo test examples/cron/schedule
go run ./scripts/examples cron
```

## Cases

`next` (six firings of a working-hours expression), `never` (`0 0 30 2 *`), `badexpr` (hour 24: status 1), `run` (three days of the jobs table across a month's end) and `badtable` (a line with a backward range: status 1, the file and line on standard error).

## Limits found

Closed in batch 12: G-ex-g-4 (`civil.ergo` reads `time.Calendar` and builds with `time.Date`; Hinnant's algorithms are gone), G-ex-b-3 and G-ex-b-5 (std `testing.VirtualClock` replaces the program's `virtual.Clock`), G-ex-b-4 (`time.Times(1s, n)`), G-ex-b-1 (`Instant(if …)`), G-ex-c-2 (`Run` writes `== (Some[Instant]{…})` in the `if` header), G-ex-f-1 (`slices.Slice` for the words of a line), G-ex-f-12 (the binary's tests use `fstest.Memory`).

- **A generated string never spells a cron field** (G-ex-d-4, also hit by cron), and a generated slice holds at most three elements (G-ex-d-9). The laws draw their expressions from a seeded `Random`.
- **A binary takes its simulation clock from std `testing`.** A clock that jumps to each `Sleep` is only `testing.VirtualClock`, so `cmd/cron` imports `testing` for a run that is not `-live`; `time.Manual` waits for an `Advance` that a single task cannot make.
