# adventure

A small text adventure. It reads one command per line from standard input and answers on standard output: find the lamp in the kitchen, take the key from the dark cellar, unlock the tower and climb to the roof. Commands: `go DIR` (or a bare `north`, `n`, … `down`, `d`), `take ITEM`, `drop ITEM`, `look`, `inventory` (`i`), `help` and `quit` (after a yes). The end of the input ends the game where you stand.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and checked line by line against the map in `world/castle.ergo`.

## Layout

The program is a module of packages (`ergo.mod`: `module adventure`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `world/` | The map, the domain package. `world.ergo` (the union `Dir`, `Opposite`, `ParseDir`, `Room`, `Exit` with the item it asks you to carry, `Place`, `World`, `RoomOf`, `ExitOf`, `Both`), `castle.ergo` (the six rooms), `laws.ergo`, `tests.ergo`. Pure. |
| `game/` | The play. `command.ergo` (the union `Command`, `Parse` and its failures `Unknown`, `Missing`, `NoWay`, `Text`), `game.ergo` (`Game`, `Ending`, `Signal`, the failures `NoExit`, `Barred`, `NotHere`, `NotCarried`, the named row `Refusal`, the machine `Play` over `State = Exploring | Asking`, `Step`, `Moved`), `say.ergo` (what each step tells), `run.ergo` (`Run`, the only function with capabilities: `Console` and `io.Stdin`), `laws.ergo`, `tests.ergo`. |
| `cmd/adventure/` | The binary. `main.ergo` binds the host's `io.Stdin` and is the only place that chooses it. |

## What it shows

- **A machine for the game.** `type Play machine[State, Signal] { initial: Exploring{G: New(world.Castle())}, step: Step }`. `Step` matches the pair `(state, signal)`: the end of the input is `Done` with `Stopped` in either state; asking, a yes is `Done`; exploring, a move `Enter`s the next room (or is `Done` with `Won` on the goal), a take or a drop `Stay`s with the item moved, and quit `Enter`s `Asking`.
- **Typed failures.** `Step` fails `Refusal`, the named row `Unknown | Missing | NoWay | NoExit | Barred | NotHere | NotCarried`; each changes nothing. `Run` catches `case Refusal as r` and `Failure(r)` names every one, with no `case _`. `Barred` carries the exit's own text ("It is too dark to go down without a light.").
- **Unions and exhaustive `match`.** `Dir`, `Command`, `State`, `Ending` and `Decision` are matched variant by variant; `Say` matches the pair of the state before and after an `Enter`.
- **Laws.** `world` states three (`OppositeIsAnInvolution`, `ParseDirReadsDirName`, `EveryExitHasAWayBack`) and `game` five (`ParseInvertsText`, `ItemsAreKept`, `GoingBackReturns`, `BarredIsTheKey`, `EndIsStopped`), over generated scripts of commands (`ScriptOf` reads one command from each generated int). `ergo test` runs them, 100 cases each.
- **Scripted input.** The tests drive `Run` with `io.Input{Text: script}` as `io.Stdin` and an `io.Recorder` as `Console`: a full playthrough to the roof, the dark and the locked door, quit asking first, and every failure told.
- **Values.** A `Game` holds where each item is as a slice of `Place`s, and `Placed` makes a new one: a game before a move is the game it was.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/adventure/cmd/adventure        # type commands; quit or ^D ends
./bin/ergo test examples/adventure/game
go run ./scripts/examples adventure
```

## Cases

`win` (a playthrough with every kind of failure on the way, to the roof), `quit` (help, quit and no, quit and yes) and `eof` (the input ends mid-game).

## Limits found

The program met these, written up by this lane and others:

- **G-ex-c-16** A machine's state must be a union: `machine[Game, Signal]` is S108. The game asks before it quits, so `State = Exploring | Asking` is a real union here, but each variant holds the whole `Game`.
- **G-ex-d-11** (partly closed by V2) `machines.Initial(Play{})` and `machines.Next` now start and step the game. `machines.Run` is a pure fold over a slice of signals, and there is no live runner over a stream of lines yet, so `Run` still steps by hand to tell each step as it happens.
- **G-ex-a-9** (partly closed by V5) A step can now `Emit` an output, but its output has the type `O` of `Done`'s output (`Ending` here), so the lines a step tells cannot be emitted beside the ending; `Say` still works them out from the states before and after, and parses the line again. `Say`'s `Emit` arm is `Unreachable`, since `Step` never emits.
- **G-ex-a-3** A call that cannot fail still needs its catch: `Run` builds the end of the input's `Stopped` itself instead of calling `Step(s, Ended)` with a catch of seven failures and a match of decisions it cannot give; the law `EndIsStopped` ties the two together.

Closed in batch 12: **G-ex-d-2** (L2 named rows: `Failure` takes a `Refusal`), **G-ex-d-9** (T4 wider generators: a law takes a generated `[]int` script), **G-ex-a-10** (S2 `slices.Concat` in `Castle`), and the property tests that restated the laws (V1 runs the laws).
