# snake

The snake game on a terminal. The snake moves one cell per tick; `w a s d` turn it, `p` pauses and resumes, `q` quits, and `b` begins a game from the title. Food is drawn from a `Random` seeded with `-seed`, so one seed and one input replay one game. By default the program reads one line of keys per tick from standard input, so a game replays from a file; with `-live` it ticks every `-tick` milliseconds on the host's clock and reads keys as they come. It is an original program: its goldens were written from its own output and checked by hand.

Tag: `default`.

## Layout

The program is a module of packages (`ergo.mod`: `module snake`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `game/` | The domain. `world.ergo` (`Point`, `Dir`, `World`, `Cause`, the board's geometry and `PlaceFood`), `machine.ergo` (the `Mode` and `Signal` unions, `type Snake machine[Mode, Signal]`, `Step` and `Advance`), `laws.ergo` and `tests.ergo`. Only the food needs a capability: the prelude's `Random`. |
| `screen/` | `Frame`, the text of a mode: the board with a border and a status line. Pure. |
| `play/` | The drivers. `Script` reads one line of keys per tick; `Live` ticks on the `Scheduler`, with a task that reads keys into a `queue.Queue`. |
| `cmd/snake/` | The binary. `main.ergo` reads the flags, seeds `Random`, binds the host's standard streams and `Scheduler`, and is the only place that chooses a provider. |

## What it shows

- **A machine.** `type Snake machine[Mode, Signal]` with `Mode = Title | Playing | Paused | Over` and `Signal = Begin | Tick | Turn | Pause | Quit`. `Step` is one `match` over the pair `(mode, signal)`: quit is `Done` with the score from every mode, a turn back the way the snake last moved is `Reject`, a turn the way it heads is `Ignore`, and a game that is over ignores everything but quit. A missing pair does not compile.
- **The two-turn bug, ruled out by the state.** A `World` keeps `Heading` (where the next tick goes) apart from `Moved` (where the last tick went), and a turn is checked against `Moved`. So `w` then `a` between two ticks of a snake moving right is rejected, and the law `NeverIntoTheNeck` holds; checking against `Heading`, as many first versions do, is the mutant that breaks it.
- **Seeded effects.** `PlaceFood` draws a free cell with `Random.IntN`; the binary binds `rand.Seeded(seed)`. The law `SeedReplays` plays a game twice from one seed and compares every mode.
- **Laws over long games.** `StaysOnTheBoard`, `FoodIsNeverOnTheSnake`, `GrowsOnlyByEating`, `NeverIntoTheNeck`, `OverIsFinal` and `SeedReplays` each play a game of 300 signals drawn from a seed on a 5 by 4 board.
- **Tasks and a queue.** `Live` starts a reader task with `task.Go`, which pushes the keys of each line into a `queue.Queue[game.Signal]`; each tick drains it with `Poll` and steps the machine. The tests bind a `Scheduler` whose `Sleep` returns at once.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/snake/cmd/snake -o /tmp/snake
printf 'b\ns\na\nw\n\n' | /tmp/snake -seed 2 -width 6 -height 4
/tmp/snake -live -tick 200          # type keys, then Enter
./bin/ergo test examples/snake/game
go run ./scripts/examples snake
```

## Cases

`game` (a scripted game that eats once, pauses for two ticks and ends at the wall), `quit` (q in the middle of a game), `live` (`-live` with no keys: the snake runs into the wall) and `badsize` (a board too small: status 2).

## Limits found

- **A task that reads standard input cannot be stopped** (G-ex-g-3). When the game ends, `Live` stops its reader task, and the stop waits until the next line or the end of the input. A real terminal game ends only after one more Enter.
- **No raw terminal: no key read, no raw mode** (G-ex-c-15, also hit by snake). `-live` reads keys a line at a time, so a turn takes effect after Enter; the game is playable only as a line-at-a-time game.
- **No duration from an integer number of milliseconds** (G-ex-b-4, also hit by snake). `-tick` becomes `duration(int64(ms) * 1000000)`: `duration(ms)` of an `int` is refused (S108, a conversion is between duration and int64).
- **A machine has no runner and no accessor for its initial state** (G-ex-d-11, also hit by snake). `type Snake machine[…]` is declared, but no code can read its `initial`; `Script` and `Live` start from `game.Title{…}` and call `Step` themselves.
- **A machine's initial state cannot use a capability** (G-ex-c-16, also hit by snake). The first food needs `Random`, so the machine starts at a `Title` mode and `Begin` places the food.
- **The branches of a `match` take the type of the first** (G-ex-c-10, G-ex-b-16, also hit by snake). `Signals` writes `let s Signal = match …`.
- **`return` cannot leave a `with` block** (G-ex-d-10, also hit by snake). `OverIsFinal` calls `AnswersOnlyQuit` to return early.
- **A generated slice holds at most three elements** (G-ex-d-9, also hit by snake). Each law draws its 300 signals from a seeded `Random`.
- **A record that holds a slice has no `==`** (G-ex-d-5, also hit by snake). `SeedReplays` compares the text `Key` of each mode.
- **No subslice** (G-ex-f-1, also hit by snake). `Bites` indexes from 1.
- **A virtual clock that advances by itself is not built** (G-ex-b-5, also hit by snake). The `Live` test binds its own `Scheduler` whose `Sleep` returns at once.
