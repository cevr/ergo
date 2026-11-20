# stopwatch

A stopwatch: start, stop and reset, with the time shown as `MM:SS.cc`. The program plays a script of timed button presses on the screen over a manual clock, and shows the screen after each press and at the end. The terminal host is stage c.

Tag: `default`.

## How it works

- Package `watch` is the machine `Stopwatch`. The state `Watch` is `Stopped{Elapsed} | Running{Before, Since, Elapsed} | Starting{Elapsed} | Stopping{Before, Since, Elapsed}`, and the signal is `Clicked{Button} | Pressed{Button, At} | Ticked{At}`. The step reads no clock: its row is empty, so the checker refuses a clock read in it (S102). Every reading is signal data. A click carries no reading, because a view's button sends a fixed signal. A click of Start or Stop enters `Starting` or `Stopping`, whose work (`read`, the machine's `work`) reads `time.Clock` and sends `Pressed{Button, At}`, as Elm's `Time.now` runs as a Cmd (law `ClickThenWorkIsThePress`; FK1-g5, decided 2026-11-15). The clock read is the work's row, not the step's. A click of Reset needs no reading. Start records the press's reading in `Since`, and the time shown so far in `Before`. The `Running` state owns its timer (the machine's `after`, a `machines.Every`): its entry arms a tick 10 ms later, and the runner makes it `Ticked{At}` from the clock's reading when it fires. A tick shows `Before` plus the time from `Since` to its reading, then enters `Running` again, which arms the next tick. Stop does the same with its own reading. So the shown time is the clock's elapsed time and never a count of ticks: a late tick, or ticks that come bunched, lose nothing. Stopping or resetting ends the entry and drops the timer, so a stopped watch never ticks. A press that does not apply (start while running, stop while stopped) is ignored and keeps the entry, so it does not move the next tick.
- Package `screen` is the view: the time as `MM:SS.cc`, then Reset and one button that is Start while the watch is stopped and Stop while it runs,. Each button sends a click, which carries no reading.
- Package `script` reads lines `@MS start|stop|reset|stats` and a last `@MS end`. `stats` prints the work of the pass that showed the last tick: 7 nodes written and 1 patch op, the time. It runs the machine in an actor on `ui.NewTestHost`, and for each line it advances the clock to the line's time, so each tick due by then fires through the `Running` state's timer. Then it presses the named button (a click, which the entry's work answers with the press at the clock's reading before the line's `Settle` ends) and prints the screen: `@MS TIME [Reset] [Start|Stop]`. A press whose button is not on the screen (Stop while stopped) presses nothing and prints `no Stop button`.
- `cmd/stopwatch` binds `time.Clock` and `Scheduler` to one `time.Manual` clock, which the test host advances, so a script of a minute takes no wall time. Each tick still runs through the actor and the screen: a run of an hour is 372,000 ticks, about 10 s of CPU, past the case budget, so `FormatReadsBack` checks the minutes past an hour instead of a case.

Whether the watch runs is the state's mode, the tick is the `Running` state's timer, and the readings come in as signal data (a `machines.Every` timer, FKG1), so the step is pure: the only clock read is the work that answers a click, one explicit effect. Stop keeps the time up to the press, by the clock, not the time of the last tick.

## Laws

Each law folds the step over generated presses and ticks, each carrying a reading the law moves itself; only the law about the click's work binds one. A tick can come late (gaps up to 2.5 s, where the timer asks for 10 ms) or bunched with others at one instant.

- `ShownIsClockElapsed`: after every press and every tick, the watch shows the clock's elapsed time while it ran (up to its last tick, when it runs). The model sums the clock's running intervals and counts no tick, so a tick count fails it.
- `ElapsedGrowsOnlyWhileRunning`: no step takes time away except a reset, which shows zero; a stopped watch's time moves only by a reset.
- `ResetIsInitial`: a reset from any state the presses reach is the initial watch.
- `StopThenStartKeepsElapsed`: a stopped watch that is started and stopped again at one instant shows the same time.
- `FormatReadsBack` (beside `Format`): the shown time of any time of a day reads back as its minutes, seconds and hundredths, with minutes past an hour not wrapped.
- `ClickThenWorkIsThePress` (beside `Step`): a click and the one press its work sends leave the watch where the press at the work's reading does.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `laps`: two runs with a stop between two ticks (it reads the clock: 1.234 s), a stop and a start whose buttons are not on the screen, a reset, a stop at the very time of a tick (the clock fires the tick first; the stop reads the clock, so the time is the same), and a long last run shown at its last tick.
- `idle`: nothing pressed: `00:00.00 [Reset] [Start]`.
- `badscript`: an unknown word; the program says which line and exits 1.

The 6 scene tests are these cases. Here the step reads the clock, so there is no command to check.
