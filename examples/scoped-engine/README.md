# scoped-engine

A compute engine that exists only while the screen says so. The program runs a script of timed presses on a manual clock, with a pool of engines whose boots take 500 ms, clicks each press's button on the screen, and shows the screen and the engines out after each press.

Tag: `default`.

## The shape

- Package `engine` is the domain. `Engines` is the contract that boots an engine and gives a `Lease`, a resource whose release (`Close`) gives the engine back. The machine `Panel` is the screen: `Off | Booting | Ready{Engine, Computed, Last} | Failed{Why}`, with signals `Start | Stop | Compute` (the buttons) and `Started | Refused` (the boot's answers).
- `Booting`'s entry owns the engine. Its work boots, sends `Started` or `Refused`, and then holds the lease until the entry ends. `Started` is a `Stay`, so the entry lasts through `Ready`. `Stop` is an `Enter` of `Off`, which ends the entry and releases the lease, whether the boot had finished or not. A stop while booting ends the boot at its wait, before it gives an engine.
- Package `memory` is the pool: engines `engine-1`, `engine-2`, … in boot order, the count of engines booted and given back, and `Fault` to fail the next boot.
- Package `screen` is the view:
  - the heading;
  - the status (`Engine is off.`, `Booting engine...`, `Engine ready: ID`, `Engine failed: WHY`);
  - `Start engine` while off or failed, `Stop engine` while booting or ready;
  - `Compute next square`, in a `ui.Fieldset` disabled unless ready;
  - the last result.
- Package `script` reads `@MS start|stop|compute|fault|end`. It plays the lines two ways:
  - `Play`, live: `Panel` as an actor (`actor.StartMachine`) with the pool bound to `Engines` in a context;
  - `PlayScreen` (`host.ergo`), on `ui.NewTestHost`: for each line it moves the clock, clicks the line's button by role and name, and reads the screen. `fault` is the pool's, not a button.
  The cases print the screen; the law `ScreenIsTheLivePanel` holds it to `Play`. `-stats` prints the work of each press.

## Laws

- `engine.Panel`: `ComputeNeedsAnEngine` (a compute changes the screen only in `Ready`, adding one to the count and showing it squared; anywhere else it is ignored) and `OnlyBootingTakesAnEngine` (a `Started` out of turn changes nothing).
- `engine.Engines`: `HeldEnginesAreDistinct` (two engines leased at once are two engines), run for `memory.Pool`.
- `script`, over generated starts, stops, computes, faults and clock moves: `ScreenIsTheLivePanel` (the screen shows the live panel's status and result, with the same engines out, after every press).
- `script`, over generated starts, stops, computes, faults and clock moves, with presses landed on boot ends: `ReleasesEqualAcquires` (after every press, one engine is out while ready and none otherwise; once the screen stops, every engine is back) and `OneBootPerStart` (no more engines boot than the starts pressed while off or failed).

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `scenes`: the six scene tests, in order on one screen.
- `engine`: a compute while off, a start, a compute while booting, two computes, a stop, a stop while booting (the boot gives no engine, so the next one is `engine-2`), a failed boot and a compute while failed, a start after the failure, a start while ready, and a stop.
- `badscript`: an unknown word; the program says which line and exits 1.
