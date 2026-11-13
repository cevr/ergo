# fk-stopwatch

A stopwatch: start, stop and reset, with the time shown as `MM:SS.cc`. It is a port of Foldkit's `stopwatch` example (`conformance/upstream/foldkit-d21db423/examples/stopwatch`, MIT), stage a of FK1: the model, with no view yet. The program runs a script of timed button presses on a virtual clock and shows the watch after each press and at the end.

Tag: `default`.

## How it works

- Package `watch` is the machine `Stopwatch`: the state `Watch` is `Stopped{Elapsed} | Running{Elapsed}`, and the signal is `Start | Stop | Reset | Ticked`. The `Running` state owns its timer (the machine's `after`): its entry arms `Ticked` after 10 ms, and a tick enters `Running` again with 10 ms more, which arms the next one. Stopping or resetting ends the entry and drops the timer, so a stopped watch never ticks. A press that does not apply (start while running, stop while stopped) is ignored and keeps the entry, so it does not move the next tick.
- Package `script` reads lines `@MS start|stop|reset` and a last `@MS end`, and replays the presses with `machines.RunTimed`.
- `cmd/fk-stopwatch` binds `time.Clock` and `Scheduler` to a `testing.VirtualClock`, so a script of an hour takes no wall time.

Upstream keeps `{elapsedMs, isRunning, startTime}` and reads the clock in two commands (`DetermineStartTime`, `DetermineTickTime`) and a tick subscription keyed on `isRunning`. Here `isRunning` is the state's mode, the subscription is the `Running` state's timer, and the time is the ticks counted, so no command reads the clock. The cost: on a live clock whose ticks come late, the counted time lags the wall time; upstream's `now - startTime` does not. On the virtual clock the two agree.

## Upstream tests

Each title of upstream's `src/story.test.ts` and `src/scene.test.ts`, and what restates it. (FK0 moves this list to `origin/tests.txt`, which the examples runner checks.)

| Upstream title | Restated by |
| --- | --- |
| ClickedStart fires DetermineStartTime with current elapsed time | `watch` test `StartEntersRunningFromTheShownTime` |
| CompletedDetermineStartTime stores the offset start time and starts running | `watch` test `StartRunsTheWatchOnTheClock` |
| ClickedStop pauses the stopwatch without zeroing time | `watch` test `StopKeepsTheTime` |
| ClickedReset zeros elapsedMs, isRunning, and startTime | `watch` test `ResetZeros` |
| ClickedReset on an idle stopwatch is a no-op | `watch` test `ResetOfAnIdleWatchKeepsIt` |
| Ticked fires DetermineTickTime with the stored startTime | `watch` test `TickAddsOneIntervalAndArmsTheNext` |
| CompletedDetermineTickTime stores the new elapsed time | `watch` test `TicksStoreTheElapsedTime` |
| initial view shows the zeroed time and Start + Reset buttons | not restated: stage a has no view (FK1b, after H4 and H4r) |
| clicking Start fires DetermineStartTime and switches to Stop | not restated: stage a has no view (FK1b) |
| clicking Stop while running switches back to Start | not restated: stage a has no view (FK1b) |
| clicking Reset zeros the elapsed time | not restated: stage a has no view (FK1b) |
| a tick from the running Subscription advances the elapsed time | not restated: stage a has no view (FK1b) |
| elapsed time formats as MM:SS.cc | not restated: stage a has no view (FK1b); `Format` is checked by `TicksStoreTheElapsedTime` and the `laps` case |

## Laws

- `ElapsedGrowsOnlyWhileRunning`: one step never takes time away except a reset, and only a tick of a running watch adds time, one interval.
- `ResetIsInitial`: a reset from any state is the initial watch.
- `StopThenStartKeepsElapsed`: stopping and starting again runs on from the same time.
- `ElapsedCountsTicksWhileRunning`: over presses at generated times, the watch shows the interval times the ticks that fell while it ran, against a model that counts them by arithmetic. It is the law that sees the timer: a start while running that re-entered `Running` would restart the tick and fail it.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `laps`: two runs with a stop between two ticks, an ignored stop and start, a reset, a stop at the very time of a tick (stepped before it), and a long last run.
- `hour`: a run of more than an hour shows `62:03.45`: minutes are not wrapped.
- `badscript`: an unknown word; the program says which line and exits 1.
