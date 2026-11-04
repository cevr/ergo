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

## What it shows

- **Unions and exhaustive `match`.** `Move`, `Game` and `Event` are unions, and every rule is a `match` that names every variant, with no `case _`. A new variant stops the build until each rule handles it. The only wildcard is in `parseMove`, whose subject is a byte.
- **A machine-shaped step.** `step(g Game, m Move) Decision[Game, string]` is the machine's transition: `Enter` with the next state, `Reject` for a blocked move, `Ignore` for a finished game. The replay loop handles all five `Decision` variants.
- **Commands and events.** Moves are the commands. `decide` is pure and records what a move does as past-tense events (`Walked`, `Waited`, `ShellCollected`, `TideRose`, `WalkerDrowned`, `PeakReached`). `evolve` folds one event into the state, is total over `Game` and `Event`, and decides nothing. Each frame names the step's decision and the recorded events.
- **Laws.** Six top-level laws state the rules: `BlockedIsRejected`, `FinishedIsFinal`, `TurnNeverFalls`, `TideNeverFalls`, `ShellsNeverFall` and `TideFollowsTurn`.
- **Tests.** Four example tests (`ShoreCollectsEveryShell`, `GreedyDrowns`, `BumpRejectsBlockedMoves`, `TypoIsRejected`) and two property tests over generated moves (`FinishedIgnoresEveryMove`, `TideFollowsEveryTurn`).
- **A typed failure.** `parseMove` and `parseMoves` fail with `BadMove`, which holds the position of the bad letter. A move list is parsed whole before play. `main` catches the failure for each replay, so `main` needs only `Console`.

## What runs today

The unions, the matches, `step`, `decide`, `evolve`, the typed failure and the replays run. The compiler checks the laws and the tests and leaves them out of the build. These parts wait for other units:

- The `Tidewalk` machine declaration (`type Tidewalk machine[Game, Move] { initial: start(), step: step }`) is refused at build until lower-v1 stage 4 lowers machine plan values. The program checks clean with it.
- `ergo test` runs the six tests when lower-v1 stage 4 adds it. Until then the harness prints `skip tidewalk-replay: test`.
- Laws run as generated tests when #162 lands, and laws move into the machine type when machines carry laws.
- A runner from the actor library replaces the replay loop over `step`.
- The program becomes a `tidewalk` package, a `frame` package and `cmd/tidewalk-replay/main.ergo` when a program can hold packages (G7).

## Case

`cases/basic` replays four move lists: `shore` (a win with all three shells), `greedy` (drowning, then ignored moves), `bump` (rejected moves and a wait, then a win) and `typo` (a bad letter). The golden is the output of the reference model of these rules, 332 lines.
