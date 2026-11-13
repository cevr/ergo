# fk-stopwatch

A stopwatch: start, stop and reset, with the time shown as `MM:SS.cc`. It is a port of Foldkit's `stopwatch` example (`conformance/upstream/foldkit-d21db423/examples/stopwatch`, MIT), stage a of FK1: the model, with no view yet. The program runs a script of timed button presses on a virtual clock and shows the watch after each press and at the end.

Tag: `default`.

## How it works

- Package `watch` is the machine `Stopwatch`. The state `Watch` is `Stopped{Elapsed} | Running{Before, Since, Elapsed}`, and the signal is `Start | Stop | Reset | Ticked`. The step reads `time.Clock`. Start records the clock's reading in `Since`, and the time shown so far in `Before`. The `Running` state owns its timer (the machine's `after`): its entry arms `Ticked` after 10 ms. A tick shows `Before` plus the clock's time since `Since`, then enters `Running` again, which arms the next tick. Stop reads the clock the same way. So the shown time is the clock's elapsed time and never a count of ticks: a late tick, or ticks that come bunched, lose nothing. Stopping or resetting ends the entry and drops the timer, so a stopped watch never ticks. A press that does not apply (start while running, stop while stopped) is ignored and keeps the entry, so it does not move the next tick.
- Package `script` reads lines `@MS start|stop|reset` and a last `@MS end`, and replays the presses with `machines.RunTimed`.
- `cmd/fk-stopwatch` binds `time.Clock` and `Scheduler` to a `testing.VirtualClock`, so a script of an hour takes no wall time.

Upstream keeps `{elapsedMs, isRunning, startTime}` and reads the clock in two commands (`DetermineStartTime`, `DetermineTickTime`) and a tick subscription keyed on `isRunning`. Here `isRunning` is the state's mode, the subscription is the `Running` state's timer, and the step reads the clock, so no command is needed. Ergo's form is not fully pure: a timer's signal is a fixed value (`Timeout{In, Signal}`) and cannot carry the time it fired, and `machines.RunTimed` runs no work. So the clock reading cannot come in as signal data, and the step needs `time.Clock` instead. Every law of the step therefore binds a clock (ledger row FK1-g1). One choice departs from upstream: Stop keeps the time up to the press, by the clock, where upstream keeps the time of the last tick and loses the rest.

## Upstream tests

Each title of upstream's `src/story.test.ts` and `src/scene.test.ts`, and what restates it. (FK0 moves this list to `origin/tests.txt`, which the examples runner checks.)

| Upstream title | Restated by |
| --- | --- |
| ClickedStart fires DetermineStartTime with current elapsed time | `watch` test `StartEntersRunningFromTheShownTime` |
| CompletedDetermineStartTime stores the offset start time and starts running | `watch` test `StartRunsTheWatchOnTheClock` |
| ClickedStop pauses the stopwatch without zeroing time | `watch` test `StopKeepsTheTime` |
| ClickedReset zeros elapsedMs, isRunning, and startTime | `watch` test `ResetZeros` |
| ClickedReset on an idle stopwatch is a no-op | `watch` test `ResetOfAnIdleWatchKeepsIt` |
| Ticked fires DetermineTickTime with the stored startTime | `watch` test `TickShowsTheClockTimeAndArmsTheNext` |
| CompletedDetermineTickTime stores the new elapsed time | `watch` test `TicksStoreTheElapsedTime` |
| initial view shows the zeroed time and Start + Reset buttons | not restated: stage a has no view (FK1b, after H4 and H4r) |
| clicking Start fires DetermineStartTime and switches to Stop | not restated: stage a has no view (FK1b) |
| clicking Stop while running switches back to Start | not restated: stage a has no view (FK1b) |
| clicking Reset zeros the elapsed time | not restated: stage a has no view (FK1b) |
| a tick from the running Subscription advances the elapsed time | not restated: stage a has no view (FK1b) |
| elapsed time formats as MM:SS.cc | not restated: stage a has no view (FK1b); `Format` is checked by `TicksStoreTheElapsedTime` and the `laps` case |

## Laws

Each law folds the step over generated presses and ticks on a `time.Manual` that the law moves itself. A tick can come late (gaps up to 2.5 s, where the timer asks for 10 ms) or bunched with others at one instant.

- `ShownIsClockElapsed`: after every press and every tick, the watch shows the clock's elapsed time while it ran (up to its last tick, when it runs). The model sums the clock's running intervals and counts no tick, so a tick count fails it.
- `ElapsedGrowsOnlyWhileRunning`: no step takes time away except a reset, which shows zero; a stopped watch's time moves only by a reset.
- `ResetIsInitial`: a reset from any state the presses reach is the initial watch.
- `StopThenStartKeepsElapsed`: a stopped watch that is started and stopped again at one instant shows the same time.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `laps`: two runs with a stop between two ticks (it reads the clock: 1.234 s), an ignored stop and start, a reset, a stop at the very time of a tick (stepped before it), and a long last run shown at its last tick.
- `hour`: a run of more than an hour shows `62:03.45`: minutes are not wrapped.
- `badscript`: an unknown word; the program says which line and exits 1.
