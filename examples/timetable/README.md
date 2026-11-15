# timetable

The timetable of a passenger line that crosses time zones. `timetable departures STOP DATE` lists the trains that call at a station on a day of that station's own calendar; `timetable week DATE` picks ISO weeks and shows the trains of each day; `timetable trip SERVICE DATE` follows one train from boarding to arrival, with an optional `-delay` once it has left or a `-cancel` before it leaves. Every time is written on the wall clock of the station it belongs to, with the zone's abbreviation and how far it is from now. `-now` sets now to an instant on a manual clock; without it the host's clock is now. It is an original program: its goldens were written from its own output and checked by hand against the zones' rules (North America changes its clocks on the second Sunday of March and the first Sunday of November).

The line in memory is the Ocean, overnight from Halifax (Atlantic time) through Moncton (Atlantic) to Rimouski and Montréal (Eastern time), with a day train (14) and two night trains (16 at 02:30 and 18 at 01:30, Sundays) that leave at the hours a clock change touches.

Tag: `default`.

## Layout

The program is a module of packages (`ergo.mod`: `module timetable`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `timetable/` | The domain. `line.ergo` (`Line` and `Stop` as a timetable writes them, with IANA zone names; `Route` and `Station` with the zones loaded, by `Load`, which needs `tz.Zones`; `Service`; the `Timetable` contract with its laws; `UnknownStop`, `UnknownService`), `departure.ergo` (`Leaves`, which matches `tz.Resolve`'s `Unique`, `Gap` and `Overlap`; `Calls`; `Departures`; their laws), `trip.ergo` (the `Trip` machine: `Leg`, `Cue`, `Notice`, `Step`, `Waits`; its laws), `week.ergo` (the ISO week picker over `civil.Span`; its laws) and `text.ergo` (the LDML formats and the relative text). |
| `memory/` | The Ocean's timetable held in memory, the provider of `timetable.Timetable`. |
| `cmd/timetable/` | The binary. `main.ergo` reads the three commands with `flag.ParseCommand` into the program's own `Cmd` union and binds the providers: the timetable in memory, std's IANA copy (`tz/iana`), standard output, and the host's clock, or `time.NewManual` at `-now`. |
| `testdata/mutants.json` | The domain's mutant ledger: each mutant is killed by a law of `timetable/`. |

## What it shows

- **A wall time is a choice.** `Leaves` turns a service's wall time at the first station into an instant with `tz.Resolve`, and matches its three answers with no `case _`: a wall time the clock shows once leaves then (`OnTime`); one the clock skips (02:30 on the second Sunday of March) leaves at the gap's start, 03:00, as a railway does, not at `tz.Compatible`'s 03:30 (`Skipped`); one the clock shows twice (01:30 on the first Sunday of November) leaves the first time (`Twice`). The departures say which.
- **A day is the station's day.** `Departures` lists the calls whose wall date at the station is the day asked, so the overnight trains that left Halifax on Sunday are Monday's trains at Montréal, an hour behind on Eastern time.
- **A machine for a trip.** `Trip` is `machine[Leg, Cue]` with `Plan` as its input: `Scheduled`, `Boarding`, `Running` (the next stop and the instants still to come), `Arrived`, `Cancelled`. A `Tick` at the instant a leg waits for (`Waits`) moves it one stop; a `Delay` moves every instant still to come; a `Cancel` ends it before it leaves and is rejected after. `Step` matches every pair of leg and cue. The binary drives the machine with ticks at each instant `Waits` names and prints each `Notice` on the station's clock.
- **Laws beside their code.** `LeavesOnItsDay`, `CallsKeepTheRunningTimes`, `DeparturesAreOnTheDay` and `EveryCallIsListed` in `departure.ergo`; `NeverGoesBack`, `TicksAtEachCallArrive`, `DelayMovesTheRest` and `CancelOnlyBeforeLeaving` in `trip.ergo`; `AWeekIsSevenDays` and `WeeksFollowOneAnother` in `week.ergo`; and the contract's `StopsRunForward` and `ServicesRun`, run over the `memory` provider. The departure laws draw a third of their dates on clock-change Sundays (North America's two, the European Union's) and half of their services between midnight and 04:00, so gaps and overlaps are reached. Each of the seven mutants in `testdata/mutants.json` is killed by one of them.
- **Typed failures.** `StopNamed` fails `UnknownStop`, `ServiceNumbered` fails `UnknownService`, `Load` fails `tz.UnknownZone`, and the binary turns each, and `civil.ParseError` of a date or of `-now`, into an exit with a message by `case`.
- **Formats in LDML.** Each format is built once: `"EEE d MMM HH:mm zzz"` for a call on its zone's clock, `"EEE d MMM yyyy"` for a day, `"YYYY-'W'ww"` for an ISO week, and `-now` read with `"yyyy-MM-dd'T'HH:mm:ssXXX"`.
- **Relative text on a chosen clock.** "in 2 days", "in 40 minutes" are `civil.Phrase(civil.Distance(now, t), civil.EnglishWords())` against the context's `time.Clock`; a case sets it with `-now`, which binds `time.NewManual`, so the cases print the same text on every run.

## Time, as this example writes it

The source for the time section of the Ergo skill:

1. Keep a zone's IANA name in data and load it at the edge: `tz.Zones.Load(name)` with `tz.Zones: iana.New()` bound in `main`. A name the database lacks is `tz.UnknownZone`, never UTC.
2. Hold instants (`time.Time`) and turn them into wall times only to show them (`tz.Zoned`, `tz.Wall`, `tz.Civil`).
3. Turn a wall time into an instant with `tz.Resolve` and match `Unique`, `Gap` and `Overlap` when the domain has a rule; `tz.Compatible` is the rule of ECMA-262 and Temporal when it has none.
4. Build a format once (`civil.Pattern`, `civil.DatePattern`, `civil.ZonedPattern`) and fail at the `let` if the pattern is wrong.
5. Read now from `time.Clock`; a test or a case binds `time.NewManual`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/timetable/cmd/timetable -- departures Moncton 2027-03-14 -now 2027-03-12T09:00:00-04:00
./bin/ergo run examples/timetable/cmd/timetable -- week 2027-03-10 -n 2
./bin/ergo run examples/timetable/cmd/timetable -- trip 14 2027-03-14 -now 2027-03-14T12:00:00-03:00 -delay 25m
./bin/ergo test examples/timetable/timetable
./bin/ergo test examples/timetable/memory
go run ./scripts/examples timetable
```

## Cases

`departures` (Moncton on the March change: train 16's 02:30 was skipped), `nextday` (Montréal on the Monday after), `fallback` (Halifax on the November change: train 18's 01:30 is shown twice, train 16's 02:30 is after it), `none` (a day with no trains), `week` (two ISO weeks), `trip` (train 14 with a delay of 25 minutes), `skipped` (train 16 leaves at 03:00 ADT, twenty minutes after boarding opens at 01:40 AST), `cancel`, and the failures `nostop`, `notrain`, `notrunning` (status 1), `baddate` and `badnow` (status 2).

## Limits found

- **No hours-and-minutes duration literal.** `1h25m` is not a literal; the running times are written `1h + 25m`.
