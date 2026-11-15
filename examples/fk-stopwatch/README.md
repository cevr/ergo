# fk-stopwatch

A stopwatch: start, stop and reset, with the time shown as `MM:SS.cc`. It is a port of Foldkit's `stopwatch` example (`conformance/upstream/foldkit-d21db423/examples/stopwatch`, MIT), stage b of FK1: the model and its screen, on std ui's test host. The program plays a script of timed button presses on the screen over a manual clock, and shows the screen after each press and at the end. The terminal host is stage c.

Tag: `default`.

## How it works

- Package `watch` is the machine `Stopwatch`. The state `Watch` is `Stopped{Elapsed} | Running{Before, Since, Elapsed}`, and the signal is `Start | Stop | Reset | Ticked`. The step reads `time.Clock`. Start records the clock's reading in `Since`, and the time shown so far in `Before`. The `Running` state owns its timer (the machine's `after`): its entry arms `Ticked` after 10 ms. A tick shows `Before` plus the clock's time since `Since`, then enters `Running` again, which arms the next tick. Stop reads the clock the same way. So the shown time is the clock's elapsed time and never a count of ticks: a late tick, or ticks that come bunched, lose nothing. Stopping or resetting ends the entry and drops the timer, so a stopped watch never ticks. A press that does not apply (start while running, stop while stopped) is ignored and keeps the entry, so it does not move the next tick.
- Package `screen` is the view: the time as `MM:SS.cc`, then Reset and one button that is Start while the watch is stopped and Stop while it runs, as upstream's.
- Package `script` reads lines `@MS start|stop|reset` and a last `@MS end`. It runs the machine in an actor on `ui.NewTestHost`, and for each line it advances the clock to the line's time, so each tick due by then fires through the `Running` state's timer. Then it presses the named button and prints the screen: `@MS TIME [Reset] [Start|Stop]`. A press whose button is not on the screen (Stop while stopped) presses nothing and prints `no Stop button`.
- `cmd/fk-stopwatch` binds `time.Clock` and `Scheduler` to one `time.Manual` clock, which the test host advances, so a script of a minute takes no wall time. Each tick still runs through the actor and the screen: a run of an hour is 372,000 ticks, about 10 s of CPU, past the case budget, so `FormatReadsBack` checks the minutes past an hour instead of a case.

Upstream keeps `{elapsedMs, isRunning, startTime}` and reads the clock in two commands (`DetermineStartTime`, `DetermineTickTime`) and a tick subscription keyed on `isRunning`. Here `isRunning` is the state's mode, the subscription is the `Running` state's timer, and the step reads the clock, so no command is needed. Ergo's form is not fully pure: a timer's signal is a fixed value (`Timeout{In, Signal}`) and cannot carry the time it fired, and `machines.RunTimed` runs no work. So the clock reading cannot come in as signal data, and the step needs `time.Clock` instead. Every law of the step therefore binds a clock (ledger row FK1-g1). One choice departs from upstream: Stop keeps the time up to the press, by the clock, where upstream keeps the time of the last tick and loses the rest.

## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files.

## Laws

Each law folds the step over generated presses and ticks on a `time.Manual` that the law moves itself. A tick can come late (gaps up to 2.5 s, where the timer asks for 10 ms) or bunched with others at one instant.

- `ShownIsClockElapsed`: after every press and every tick, the watch shows the clock's elapsed time while it ran (up to its last tick, when it runs). The model sums the clock's running intervals and counts no tick, so a tick count fails it.
- `ElapsedGrowsOnlyWhileRunning`: no step takes time away except a reset, which shows zero; a stopped watch's time moves only by a reset.
- `ResetIsInitial`: a reset from any state the presses reach is the initial watch.
- `StopThenStartKeepsElapsed`: a stopped watch that is started and stopped again at one instant shows the same time.
- `FormatReadsBack` (beside `Format`): the shown time of any time of a day reads back as its minutes, seconds and hundredths, with minutes past an hour not wrapped.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `laps`: two runs with a stop between two ticks (it reads the clock: 1.234 s), a stop and a start whose buttons are not on the screen, a reset, a stop at the very time of a tick (the clock fires the tick first; the stop reads the clock, so the time is the same), and a long last run shown at its last tick.
- `idle`: nothing pressed: `00:00.00 [Reset] [Start]`.
- `badscript`: an unknown word; the program says which line and exits 1.

The 6 scene tests are these cases (`origin/tests.txt`). Upstream's Start and tick scenes also check the commands that read the clock. Here the step reads the clock, so there is no command to check.
