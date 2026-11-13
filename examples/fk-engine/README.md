# fk-engine

A compute engine that exists only while the screen says so. It is a port of Foldkit's `managed-resource-layer` example (`conformance/upstream/foldkit-d21db423/examples/managed-resource-layer`, MIT), stage a of FK6: the model, with no view yet. The program runs a script of timed presses on a manual clock, with a pool of engines whose boots take 500 ms, and shows the screen and the engines out after each press.

Tag: `default`.

## The shape

- Package `engine` is the domain. `Engines` is the contract that boots an engine and gives a `Lease`, a resource whose release (`Close`) gives the engine back. The machine `Panel` is the screen: `Off | Booting | Ready{Engine, Computed, Last} | Failed{Why}`, with signals `Start | Stop | Compute` (the buttons) and `Started | Refused` (the boot's answers).
- `Booting`'s entry owns the engine. Its work boots, sends `Started` or `Refused`, and then holds the lease until the entry ends. `Started` is a `Stay`, so the entry lasts through `Ready`. `Stop` is an `Enter` of `Off`, which ends the entry and releases the lease, whether the boot had finished or not. A stop while booting ends the boot at its wait, before it gives an engine.
- Package `memory` is the pool: engines `engine-1`, `engine-2`, … in boot order, the count of engines booted and given back, and `Fault` to fail the next boot.
- Package `script` reads `@MS start|stop|compute|fault|end` and plays it live: `Panel` as an actor (`actor.StartMachine`) with the pool bound to `Engines` in a context.

What upstream needs and the port does not:

- **No lifecycle echoes.** Upstream has 8 Messages; 3 of them (`StartedEngine`, `StoppedEngine`, `FailedStartEngine`) come from the runtime's managed-resource entry, and `SkippedCompute` answers a compute that ran without the engine. Here `Started` and `Refused` are the boot's answers, sent by the work that owns the boot. `StoppedEngine` has no counterpart: the end of the entry is the release.
- **No `ResourceNotAvailable`.** A compute exists only as a step of `Ready`, which has the engine. In any other state `Compute` is `Ignore` in the step, so "compute without an engine" cannot be written.
- **The compute is a step.** Squaring is pure, so `Ready`'s step computes the result itself. The plan expected L1 and L2 here ("the step cannot emit and stay"). They do not bite, because there is no command to emit. A compute that had to run on the engine's task would hit them.

Deliberate difference from upstream: the count of computes and the last result belong to `Ready`, so a new engine starts at 0 with no result. Upstream keeps both across stops. A result belongs to the engine that computed it.

## Upstream tests

Each title of upstream's `src/story.test.ts` and `src/scene.test.ts`, and what restates it. (FK0 moves this list to `origin/tests.txt`, which the examples runner checks.)

| Upstream title | Restated by |
| --- | --- |
| ClickedStartEngine requests the engine by entering Booting | `engine` test `StartEntersBooting` |
| StartedEngine marks the engine ready with its id | `engine` test `StartedIsReadyWithItsId` |
| ClickedStopEngine releases the engine by entering Off | `engine` test `StopEntersOff`; the release is law `ReleasesEqualAcquires` |
| StoppedEngine is a no-op lifecycle ack | no restatement: there is no StoppedEngine; the end of the entry is the release |
| FailedStartEngine records the failure reason | `engine` test `RefusedRecordsWhy` |
| ClickedCompute increments the counter and fires Compute with the next value | `engine` test `ComputeCountsAndSquares` |
| SkippedCompute leaves the model unchanged | no restatement: a compute without an engine is `Ignore` in the step (law `ComputeNeedsAnEngine`) |
| initial view shows the engine off with a Start button | not restated: stage a has no view (FK6b, after H4 and H4r) |
| clicking Start engine enters the booting state and shows Stop | not restated: stage a has no view (FK6b) |
| a ready engine shows its id and the Stop button | not restated: stage a has no view (FK6b) |
| clicking Compute fires the Compute command and renders the result | not restated: stage a has no view (FK6b) |
| the engine lifecycle drives boot, ready, and stop through the resource steps | not restated: stage a has no view (FK6b); case `engine` plays the same lifecycle |
| a failed engine boot shows the failure reason | not restated: stage a has no view (FK6b); case `engine` shows it |

## Laws

- `engine.Panel`: `ComputeNeedsAnEngine` (a compute changes the screen only in `Ready`, adding one to the count and showing it squared; anywhere else it is ignored) and `OnlyBootingTakesAnEngine` (a `Started` out of turn changes nothing).
- `memory`: `EveryLeaseComesBack` (engines leased at once have different ids, and none is out once their leases end).
- `script`, over generated starts, stops, computes, faults and clock moves, with presses landed on boot ends: `ReleasesEqualAcquires` (after every press, one engine is out while ready and none otherwise; once the screen stops, every engine is back) and `OneBootPerStart` (no more engines boot than the starts pressed while off or failed).

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `engine`: a compute while off, a start, a compute while booting, two computes, a stop, a stop while booting (the boot gives no engine, so the next one is `engine-2`), a failed boot and a compute while failed, a start after the failure, a start while ready, and a stop.
- `badscript`: an unknown word; the program says which line and exits 1.
