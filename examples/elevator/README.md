# elevator

One elevator car, driven from standard input. Each line is a command: `call N` calls the car to floor N (0 to 9), `tick` moves time on one step, `tick N` moves it N steps, and `halt` ends the run. After each step the program prints what the car does, where it is, and the floors it is still called to; at the end it prints where the car stopped and how many calls it served. A call to a floor outside the building, a call past the car's six, or a line that is no command is told and changes nothing.

Tag: `default`. It is an original program, not a port, and the worked example of the REUSE plan: a user collection, written in the value style, inside a machine's states. Its goldens were made by running the program and checked step by step against `Step`'s rules.

## Layout

The program is a module of packages (`ergo.mod`: `module elevator`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `floors/` | `set.ergo`: the sorted set `Set` of floors with `New`, `Len`, `Floors`, `Has`, `Insert`, `Remove`, `InsertCapped` (which fails `Full`), `Above`, `Below` and `SetOf`, and its five laws beside them. Pure. |
| `car/` | `car.ergo`: the signals `Call`, `Tick` and `Halt`, the machine `Car` over `State = Idle \| Moving \| Open`, `Step` and its helpers, the failures `NoFloor` and `floors.Full`, `Run` over a list of signals, and three laws. `run.ergo`: `Parse`, the command reader with its failure `Unknown`, and `Drive`, the only function with capabilities (`Console` and `io.Stdin`). |
| `cmd/elevator/` | The binary. `main.ergo` binds the host's `io.Stdin` and is the only place that chooses it. |

## What it shows

- **A machine with a collection in its states.** `type Car machine[State, Signal] { initial: Idle{…}, step: Step }`. Each state carries a `floors.Set`, and `Step` makes the next state from the last in the record-update style: `Stay[State]{Next: Moving{..m, Calls: try floors.InsertCapped(m.Calls, f, Cap)}}`, `Moving{..m, At: next}`.
- **Exhaustive `match`.** `Step` matches the pair `(state, signal)`; `Halt` is `Done` in any state, a call goes to `called`, and a tick to `started`, `moved` or `closed` by state. `Drive` matches every `Decision`, with no `case _`.
- **Typed failures.** `Step` fails `floors.Full | NoFloor`; `Drive` catches each by name and tells it. `Parse` fails `Unknown`.
- **Laws beside their code.** `floors` states `InsertKeepsOrder`, `InsertTwiceIsOnce`, `RemoveUndoesInsert` (which also checks that the caller's `Set` is unchanged), `CappedHoldsTheCap` and `AboveAndBelowAreNeighbours`; `car` states `EveryCallIsServed` (given enough ticks, every kept call is served and the car idles), `CallsStayInTheBuilding` and `StepKeepsTheOldState` (a state kept before a step is the state it was). `ergo test` runs each 100 times, and the test build runs them on the poisoned forms of any in-place write.
- **Where a value is copied.** `ergo inspect copies examples/elevator/cmd/elevator` prints each copy the build makes and why. Today `Insert` and `Remove` copy their `Set` on each call, since each returns its argument whole on an early path (the REUSE plan's row g4); REUSE RU1 makes them write in place, and RU3 carries that through the machine's union states. The program's output does not change with either: only the copies do.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
printf 'call 3\ncall 7\ntick 8\nhalt\n' | ./bin/ergo run examples/elevator/cmd/elevator
./bin/ergo test examples/elevator/floors
./bin/ergo test examples/elevator/car
./bin/ergo inspect copies examples/elevator/cmd/elevator
go run ./scripts/examples elevator
```

## Cases

`ride` (two calls up, a call down on the way, the doors at each floor, then idle and halt) and `refused` (a call to the floor the car stands at, floors outside the building, unknown lines, a seventh call refused as full, and the end of the input mid-ride).
