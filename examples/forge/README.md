# forge

The forge of a small crafting game and a traffic light at a crossing, run side by side on one virtual clock from a script. The forge takes ore, forges an item from a slot and gives the leftover ore back, returns a credit 30 s after the last ore, drops a forged item in 2 s, and goes cold and is kindled again. The light turns red, green and yellow; a press of the button ends a green early, never before 20 s; a fault makes it flash red until a repair. The program reads a script of timed lines on standard input (`12s ore iron`, `40s select A1`, `95s button`) and prints what each machine did and when, then the tally.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program; `drive/tests.ergo` has a run worked by hand, and the `basic` golden was checked by hand against the rules above and the ore law.

## Layout

The program is a module of packages (`ergo.mod`: `module forge`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `forge/` | The forge, a domain package. `ore.ergo` (the contract `Assayer`, which tells an ore's worth, with the laws `TakesOnlyGrades` and `IsRepeatable`; `Grades`), `machine.ergo` (`Slot`, the `State`, `Signal` and `Event` unions, the typed failures `NotEnough`, `NoStock`, `NoSuchSlot`, `Busy`, `Unlit`, `BadOre` and their row `Refusal`; `Decide`, `Evolve`, `Apply`, `Step`, the `Forge` machine, whose input is the slots, `Deadline`), `laws.ergo`, `tests.ergo`. Pure. |
| `light/` | The crossing light, a domain package. `light.ergo` (the `Light` and `Cue` unions, `Step`, the `Crossing` machine, `Deadline`, `Follows`), `laws.ergo`, `tests.ergo`. Pure. |
| `drive/` | The script and the clock. `script.ergo` (`Item`, `ParseScript`, which fails `BadLine`; `Duration`), `run.ergo` (`Both`, `CatchUp`, `Deliver`, `Run`, `Summary`), `laws.ergo`, `tests.ergo`. Pure, except that an ore goes through `forge.Assayer`. |
| `memory/` | `Ores`, a `forge.Assayer` over a table in memory, and `Standard()`, the ores the machine takes. |
| `cmd/forge/` | The binary: the flags (std `flag`), the rack, and the context that binds `memory.Standard()` as the assayer and the host's input and output. |

## What it shows

- **Machines with guards and timeouts.** `type Forge machine[State, Signal]` and `type Crossing machine[Light, Cue]`. Each step is one `match` over the pair of state and signal. The forge's guards are typed failures in order (the slot, its stock, the cost), so a refused key says which guard refused it; `Tried` turns a refusal into `Reject`. A timed state holds its deadline (`Heating.Until`, `Forging.Until`; a light's `Since` and the phase length), and a `Tick` at or after it moves the machine on.
- **Decide, then evolve.** `forge.Decide` returns the events of a signal (`Accepted`, `Forged`, `Returned`, `Dropped`, `WentCold`, `CameBack`) or a failure; `Evolve` only folds them. The tally (`Kept`) is a fold over the same events, which is how the ore law sees every unit.
- **A virtual clock that the driver owns.** No machine reads a clock. `drive.CatchUp` delivers each deadline's `Tick` at the deadline itself, first deadline first (the forge first on a tie), before each line of the script; a phase begins at the deadline, not when a tick came, so the light does not drift.
- **Laws.** `forge` states five (`OreIsKept`, `LeftoverIsNeverNegative`, `ForgeTakesOne`, `TimeoutReturns`, `ColdTakesNoOre`) and the contract `Assayer` two; `light` five (`ColoursFollow`, `PhasesDoNotDrift`, `GreenLastsMinGreen`, `ButtonShortensGreen`, `OnlyRepairEndsFlashing`); `drive` two (`ShowReads`, `CatchUpLeavesNothingDue`). `ergo test` runs them; the run laws also run over a fixed rack and mostly valid signals, which generated ones seldom are. The `Assayer` laws run in `memory` against `Ores`, built by `NewOres`, which takes only ores of a grade (G-exup-1, closed in batch 13; the law `TakesOnlyGrades` found a table of any values once it ran). For each law a mutant of the program fails it (`forge-mutants.json` of the retheme unit RT1-A, translated from the batch-12 ex-upgrade unit's list, 14 of 14, seeds 1-3).
- **Exhaustive matches.** Every `match` over a union names each variant or pair; adding a signal makes each step and each driver function a check error until it handles it.
- **Providers chosen in one place.** Only `cmd/forge/main.ergo` binds `forge.Assayer`, `io.Stdout`, `io.Stdin` and `os.Process`. Its tests run the same program on a `Recorder` and an `io.Input`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/forge/cmd/forge < examples/forge/cases/basic/stdin
./bin/ergo run examples/forge/cmd/forge -- -brief -stock 0 < examples/forge/cases/brief/stdin
./bin/ergo test examples/forge/forge     # and light, drive, memory, cmd/forge
go run ./scripts/examples forge
```

A script line is a time, a word and maybe one more word: `ore NAME` (`tin`, `copper`, `iron`, `gold`; any other name is slag, which the assayer gives back), `select SLOT` (`A1` sword 125, `A2` shield 100, `B1` arrows 75), `cancel`, `douse`, `kindle`, `button`, `fault`, `repair`, `end`. Times are whole `ms`, `s`, `m` or `h`, and never go back. `#` starts a comment.

## Cases

`basic` (a forged item with leftover ore, a lump of slag, a refused key, a press of the button, a douse with a return, a fault and its repair), `brief` (`-brief -stock 0`: a key of an empty slot, a cancel and a timeout return), `help`, `badline` (a time that goes back, exit 2) and `badstock` (`-stock -3`, exit 2).

## Limits found

Each gap is in the lane's gap file (`gaps-ex-b.md`) with a repro.

- **G-ex-b-9** (partly closed by S4): `time.ParseDuration` and `time.DurationText` exist, but a script's time is narrower (a whole number and one unit, no sign, no `1m30s`) and the log writes `1500ms`, not `1.5s`, so `drive.Duration` and `drive.Show` stay (law `ShowReads`); their arithmetic is `time.Times` and `time.Count`.
- **G-ex-b-11** (diagnostic): `let _ = with … { run(args) } catch { … }`, where `run` has no result, is S107 "the path can reach the end of the block without a value" at the block, not "this expression has no value". Workaround: the `with` as a statement.
- **G-ex-b-12** (partly closed by V5): a machine can now state its timers (`after`), and `machines.RunTimed` fires them, but for one machine over its own trace. `drive` steps two machines and an assayer on one clock, and each signal carries its own time, so each timed state still carries its deadline, each package exports `Deadline`, and `drive.CatchUp` sends the ticks.
- **G-exup-2** (checker; type inference): `machines.Next(g, d)` with `g` a variant is S108, so the laws write `let g Light = Green{…}` first.
- By design, and still friction: the machine never ends, so its output type (`[]Slot`) is never seen, and neither machine emits.

Closed in batch 12: G-ex-b-10 (S2: `slices.Concat`, `appendAll` gone), G-ex-b-4 (S4: `time.Times`), G-ex-b-8 (T1: `ergo run DIR -- ARGS`), the fixed start (V4: `Forge`'s input is the slots, and `drive.Fresh` starts both machines with `machines.Start` and `machines.Initial`), each package's hand `Next` (V2: `machines.Next`), the six-name catches (L2: the row `Refusal`), and the property tests that restated the laws (V1).
