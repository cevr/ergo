# tidewalk-replay

Tidewalk is an original small puzzle game, designed for this repository. A walker crosses a tidal flat to the high rocks while the tide rises. This program is the pure core of the game: it replays fixed move lists and prints one text frame per state. Its golden is also the golden of the later interactive game, `examples/tidewalk`.

Tag: `default`. It has no declared differences: it is an original program, not a port, and its golden comes from an independent reference model of the rules.

## Rules

- The level is 9 columns by 6 rows. `#` is rock; a digit is the height of sand, from 0 to 3. Height 3 is the peak. Three shells lie on the flat.
- A move is `N`, `E`, `S`, `W` or `.` (wait). A move off the grid or into rock is rejected: nothing changes and no turn passes.
- Every other move takes one turn. On every sixth turn the tide rises one height step.
- The walker collects a shell on a cell whose sand is not under the tide. A shell under water is hidden.
- The walker drowns on sand below the tide, and wins on the peak. Drowning comes first. A finished game ignores every later move.
- A move list with a letter that is not a move plays nothing: the program prints `rejected <name>: move <at> is not one of N E S W .`.

## Layout

The program is a module of packages (`ergo.mod`: `module tidewalk`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `walk/` | The game, the domain package. `level.ergo` (the level and its cells), `moves.ergo` (the `Move` commands and the `Moves` brand), `game.ergo` (the `Game` state), `events.ergo` (the `Event` facts, `Decide` and `Evolve`), `machine.ergo` (`Step` and the `Tidewalk` machine), `laws.ergo`, `tests.ergo`, and `replays.ergo`, the `Library` contract it needs. It is pure: no function needs a capability. |
| `frame/` | The text frame of a state: `Show` and `Result`. |
| `memory/` | `Fixed`, the in-memory provider of `walk.Library`: the four replays. |
| `cmd/tidewalk-replay/` | The binary. `main.ergo` binds the context, `let Live = context { walk.Library: memory.Fixed }`, and is the only place that chooses a provider. |

`ergo run examples/tidewalk-replay/cmd/tidewalk-replay` builds and runs it; `ergo test examples/tidewalk-replay/walk` runs the game's tests.

## What it shows

- **Packages.** Each package is a folder, imported by the module path (`import tidewalk/walk`). The binary depends on the domain, the domain on nothing.
- **A brand.** `walk.Moves` has unexported fields, so only package `walk` builds one, through `ParseMoves`: a `Moves` is never a list with a bad letter. Outside `walk`, `walk.Moves{}` and `let m walk.Moves` are S114, and the message names `walk.ParseMoves`.
- **Unions and exhaustive `match`.** `Move`, `Game` and `Event` are unions, and every rule is a `match` that names every variant, with no `case _`. A new variant stops the build until each rule handles it. The only wildcard is in `parseMove`, whose subject is a byte.
- **A machine.** `type Tidewalk machine[Game, Move] { initial: Start(), step: Step }` declares the game as a machine. `Step(g Game, m Move) Decision[Game, string]` is its transition: `Enter` with the next state, `Reject` for a blocked move, `Ignore` for a finished game. `Apply` and `Word` handle all five `Decision` variants.
- **Commands and events.** Moves are the commands. `Decide` is pure and records what a move does as past-tense events (`Walked`, `Waited`, `ShellCollected`, `TideRose`, `WalkerDrowned`, `PeakReached`). `Evolve` folds one event into the state, is total over `Game` and `Event`, and decides nothing. Each frame names the step's decision and the recorded events.
- **Record update.** `evolveWalk` writes each next state as a copy of the walk with the changed fields: `Walk{..w, Turn: w.Turn + 1}`.
- **Laws.** Six top-level laws state the rules: `BlockedIsRejected`, `FinishedIsFinal`, `TurnNeverFalls`, `TideNeverFalls`, `ShellsNeverFall` and `TideFollowsTurn`. The member law `NamedOnce` states the `Library` contract: every replay has a name, and no two share one, because the output names each replay. A provider that holds two replays of one name breaks it.
- **Tests.** Four example tests (`ShoreCollectsEveryShell`, `GreedyDrowns`, `BumpRejectsBlockedMoves`, `TypoIsRejected`) and two property tests over generated moves (`FinishedIgnoresEveryMove`, `TideFollowsEveryTurn`), in package `walk`. In package `memory`, `FixedNamesEachReplayOnce` runs the `NamedOnce` predicate against `Fixed`.
- **A contract and a provider.** `walk.Library` is an operation type; `replayAll` declares `needs Console | walk.Library`, and `main` supplies `memory.Fixed` with `with Live { … }`.
- **A typed failure.** `ParseMoves` fails with `BadMove`, which holds the position of the bad letter. A move list is parsed whole before play. `replayAll` catches the failure for each replay.

## What runs today

All of the above runs. `scripts/examples` runs `ergo test` on `memory/` and `walk/` and prints `ok tidewalk-replay: test memory` and `ok tidewalk-replay: test walk`. The compiler checks the laws and leaves them out of the build. `scripts/examples` refuses an operation type with no law in a package outside `cmd/`. These parts wait for other units:

- Laws run as generated tests when #162 lands: then `NamedOnce` runs against every provider of `Library`, and the test in `memory` is no longer needed. Laws move into the machine type when machines carry laws.
- A runner from the actor library replaces the replay loop over `Step`.

## Case

`cases/basic` replays four move lists: `shore` (a win with all three shells), `greedy` (drowning, then ignored moves), `bump` (rejected moves and a wait, then a win) and `typo` (a bad letter). The golden is the output of the reference model of these rules, 332 lines.
