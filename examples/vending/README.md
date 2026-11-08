# vending

A vending machine and the traffic light at the crossing in front of it, run side by side on one virtual clock from a script. The machine takes coins, sells from a slot and gives change, refunds a credit 30 s after the last coin, drops a sale in 2 s, and goes out of service and back. The light turns red, green and yellow; a press of the button ends a green early, never before 20 s; a fault makes it flash red until a repair. The program reads a script of timed lines on standard input (`12s coin quarter`, `40s select A1`, `95s button`) and prints what each machine did and when, then the books.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program; `drive/tests.ergo` has a run worked by hand, and the `basic` golden was checked by hand against the rules above and the money law.

## Layout

The program is a module of packages (`ergo.mod`: `module vending`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `vend/` | The vending machine, a domain package. `coin.ergo` (the contract `Acceptor`, the coin mechanism, with the laws `TakesOnlyDenominations` and `IsRepeatable`; `Denominations`), `machine.ergo` (`Slot`, the `State`, `Signal` and `Event` unions, the typed failures `NotEnough`, `SoldOut`, `NoSuchSlot`, `Busy`, `OutOfService`, `BadCoin`; `Decide`, `Evolve`, `Apply`, `Step`, the `Vend` machine, `Deadline`, `Next`), `laws.ergo`, `tests.ergo`. Pure. |
| `light/` | The crossing light, a domain package. `light.ergo` (the `Light` and `Cue` unions, `Step`, the `Crossing` machine, `Deadline`, `Follows`), `laws.ergo`, `tests.ergo`. Pure. |
| `drive/` | The script and the clock. `script.ergo` (`Item`, `ParseScript`, which fails `BadLine`; `Duration`), `run.ergo` (`Both`, `CatchUp`, `Deliver`, `Run`, `Summary`), `laws.ergo`, `tests.ergo`. Pure, except that a coin goes through `vend.Acceptor`. |
| `memory/` | `Coins`, a `vend.Acceptor` over a table in memory, and `US()`, the coins the machine takes. |
| `cmd/vending/` | The binary: the flags (std `flag`), the shelf, and the context that binds `memory.US()` as the coin mechanism and the host's input and output. |

## What it shows

- **Machines with guards and timeouts.** `type Vend machine[State, Signal]` and `type Crossing machine[Light, Cue]`. Each step is one `match` over the pair of state and signal. The vending machine's guards are typed failures in order (the slot, its stock, the price), so a refused key says which guard refused it; `Tried` turns a refusal into `Reject`. A timed state holds its deadline (`Collecting.Until`, `Vending.Until`; a light's `Since` and the phase length), and a `Tick` at or after it moves the machine on.
- **Decide, then evolve.** `vend.Decide` returns the events of a signal (`Accepted`, `Sold`, `Refunded`, `Dropped`, `WentOff`, `CameBack`) or a failure; `Evolve` only folds them. The books (`Kept`) are a fold over the same events, which is how the money law sees every cent.
- **A virtual clock that the driver owns.** No machine reads a clock. `drive.CatchUp` delivers each deadline's `Tick` at the deadline itself, first deadline first (the vending machine first on a tie), before each line of the script; a phase begins at the deadline, not when a tick came, so the light does not drift.
- **Laws.** `vend` states five (`MoneyIsKept`, `ChangeIsNeverNegative`, `SaleTakesOne`, `TimeoutRefunds`, `OffTakesNoMoney`) and the contract `Acceptor` two; `light` five (`ColoursFollow`, `PhasesDoNotDrift`, `GreenLastsMinGreen`, `ButtonShortensGreen`, `OnlyRepairEndsFlashing`); `drive` two (`ShowReads`, `CatchUpLeavesNothingDue`). Each package's tests restate them as property tests, and for each law a mutant of the program makes its test fail.
- **Exhaustive matches.** Every `match` over a union names each variant or pair; adding a signal makes each step and each driver function a check error until it handles it.
- **Providers chosen in one place.** Only `cmd/vending/main.ergo` binds `vend.Acceptor`, `io.Stdout`, `io.Stdin` and `os.Process`. Its tests run the same program on a `Recorder` and an `io.Input`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/vending/cmd/vending -o /tmp/vending
/tmp/vending < examples/vending/cases/basic/stdin
/tmp/vending -brief -stock 0 < examples/vending/cases/brief/stdin
./bin/ergo test examples/vending/vend     # and light, drive, memory, cmd/vending
go run ./scripts/examples vending
```

A script line is a time, a word and maybe one more word: `coin NAME` (`nickel`, `dime`, `quarter`, `dollar`; any other name is returned by the coin mechanism), `select SLOT` (`A1` cola 125c, `A2` water 100c, `B1` chips 75c), `cancel`, `service`, `resume`, `button`, `fault`, `repair`, `end`. Times are whole `ms`, `s`, `m` or `h`, and never go back. `#` starts a comment.

## Cases

`basic` (a sale with change, a counterfeit, a refused key, a press of the button, a service stop with a refund, a fault and its repair), `brief` (`-brief -stock 0`: a sold-out key, a cancel and a timeout refund), `help`, `badline` (a time that goes back, exit 2) and `badstock` (`-stock -3`, exit 2).

## Limits found

Each gap is in the lane's gap file (`gaps-ex-b.md`) with a repro.

- **G-ex-b-9** (std): no `time.ParseDuration`, nor a text form of a `duration`. Workaround: `drive.Duration` reads a whole number with one unit, and `drive.Show` writes one back (law `ShowReads`).
- **G-ex-b-10** (std): no way to join two slices (`slices.Concat`, `append(a, b...)`). Workaround: a local `appendAll` loop in `drive/run.ergo`.
- **G-ex-b-11** (diagnostic): `let _ = with … { run(args) } catch { … }`, where `run` has no result, is S107 "the path can reach the end of the block without a value" at the block, not "this expression has no value". Workaround: the `with` as a statement.
- **G-ex-b-12** (language): a machine cannot state a timeout (`gen_statem`'s `state_timeout`, XState's `after`), so each timed state carries its deadline, each package exports `Deadline`, and `drive.CatchUp` sends the ticks; no checker rule ties them, only the laws. Each package writes its own `Next(s, d)`, and nothing runs a machine value (`actor.StartMachine` is designed, not built).
- **G-ex-b-4** (seen again): `duration` times an `int` is refused; `drive.Duration` writes `duration(int64(n) * int64(per))`.
- **G-ex-b-8** (seen again): `ergo run` passes no arguments and the program reads standard input, so it is tried with `ergo build` and the binary.
- By design, and still friction: the machine's `initial` is a fixed value (`Start([]Slot{})`), so the program starts the machine with its own shelf by `vend.Start`; the machine never ends, so its output type (`[]Slot`) is never seen. Laws are checked, not run (#162), so each is restated as a property test.
