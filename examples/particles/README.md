# particles

A small deterministic particle game in a text box. Particles move by whole cells each step, bounce off the four walls (and lose speed there when `-loss` is set), and pass their velocities round when they meet in a cell. A fixed-step loop advances the box once per 20 ms of game time, whatever time each 50 ms frame took, and shows each frame as text. Keys pause, resume and quit the game.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program; the worked steps in `world/tests.ergo` were checked by hand, and the cases were checked against them and against the laws (the energy line of each frame, the steps per frame of the memory clock).

## Layout

The program is a module of packages (`ergo.mod`: `module particles`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `world/` | The box, the domain package. `world.ergo` (`Particle`, `World` with construction authority, `Empty`, `Blank`, `Spawn`, `Energy`, the typed failures `BadSize`, `BadLoss`, `TooMany`), `step.ergo` (`Wall`, `Event`, `Step`, `Run`, `Tally`), `laws.ergo`, `tests.ergo`. Pure, except `Spawn`, which needs `Random`. |
| `sim/` | The game. `machine.ergo` (`Game`, the `Phase` and `Input` unions, `Step`, the `Sim` machine, `Next`, `Key`), `loop.ergo` (the contract `Screen` with its law, `Pace`, `Play`, the fixed-step loop), `laws.ergo`, `tests.ergo`. |
| `render/` | The text: `Box` draws a box, `Status` the line under it. Pure. |
| `term/` | `Screen`, the `sim.Screen` provider over an `io.Stdout` value: it clears a terminal before each frame. |
| `memory/` | `Clock`, a `Scheduler` and `time.Clock` that moves on at once when the loop sleeps, a quarter late on every third frame; `Tape`, a `sim.Screen` that keeps its frames. |
| `cmd/particles/` | The binary: the flags, the seeded `Random`, and the context that chooses the clock (`memory.Clock`, or the host's with `-live`) and the screen. |

## What it shows

- **Exact numbers, so laws are exact.** Positions and velocities are `int`s, and energy is `Σ vx² + vy²`, so `ElasticKeepsEnergy` compares with `==`. A seed replays a run on any host (`SeedReplays`), because the only randomness is `Random`, bound to `rand.Seeded(seed)` in a context.
- **Laws.** `world` states five (`EnergyNeverRises`, `ElasticKeepsEnergy`, `StaysInTheBox`, `SeedReplays`, `StepCountsOne`), `sim` five over the machine and the loop (`PausedIgnoresTicks`, `RunningTicksStay`, `QuitIsDone`, `PauseResumeRoundTrips`, `StepsFollowTheClock`), and the contract `Screen` one (`ShowCountsOne`), which `memory.Tape`, `term.Screen` and the test screens keep. Each package's tests restate them as property tests, and for each law a mutant of the program makes its test fail.
- **A machine.** `type Sim machine[Phase, Input]` with `Phase = Running | Paused` and `Input = Tick | Pause | Resume | Quit | Load`. `Step` is one `match` over the pair that names every case, and uses `Enter`, `Stay`, `Ignore` and `Done`.
- **A fixed-step loop.** `Play` sleeps a frame on `Scheduler`, reads `time.Clock`, and ticks the machine once per whole `Dt` it owes, carrying the rest. The steps are the elapsed time over `Dt`, rounded down (`StepsFollowTheClock`), whatever the frames took.
- **Typed failures.** `Spawn` fails `BadSize | BadLoss | TooMany`; `cmd` turns each into an `os.Exit` with its own message.
- **Providers chosen in one place.** Only `cmd/particles/main.ergo` binds `Random`, `Scheduler`, `time.Clock`, `sim.Screen`, `io.Stdout` and `os.Process`. Its tests run the same game on a `Recorder`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/particles/cmd/particles            # the defaults; ergo run passes no arguments (G-ex-b-8)
./bin/ergo build examples/particles/cmd/particles -o /tmp/particles
/tmp/particles -seed 3 -frames 10
/tmp/particles -live -frames 40                            # paced by the host's clock
./bin/ergo test examples/particles/world
go run ./scripts/examples particles
```

## Cases

`basic` (the defaults), `keys` (a pause, a resume and a quit, with loss at the walls: the energy falls), `help`, `badsize` (exit 2) and `unknown` (an unknown flag, exit 2 with the usage).

## Limits found

Each gap is in the lane's gap file (`gaps-ex-b.md`) with a repro.

- **G-ex-b-1** (checker bug): `int64(if … { } else { })` gives `int64` to the branches. Workaround: a `let` first (`memory/tests.ergo`).
- **G-ex-b-2** (tooling): a failed `assert a == b` shows only the source, not the values.
- **G-ex-b-3** (language or std): a provider keeps state only in a `Map` field written through a local (`memory.Tape`, `term.Screen`, the test screens): a `*T` function does not provide an operation, and `sync.Ref` is not built.
- **G-ex-b-4** (diagnostic; #175): `d / 4` on a `duration` is refused with "write a duration literal such as 4s". Workaround: `duration(int64(d) / 4)`.
- **G-ex-b-5** (std): no virtual clock that jumps when the program sleeps. `memory.Clock` is one, for one task.
- **G-ex-b-6** (tooling): `ergo run` leaves `dist/` in a program folder, which the examples runner refuses.
- **G-ex-b-7** (tooling): `ergo test` prints the check JSON, not the diagnostic lines.
- **G-ex-b-8** (tooling): `ergo run DIR -seed 3` is "unknown option -seed": `ergo run` passes no arguments to the program, so a CLI is tried by `ergo build` and the binary.
- By design, and still friction: a `return` cannot leave a `with` block and a `try` inside one leaves the function (rows.md, Error rows 4), so `main` calls `run`, and the test calls `game`, to stop early or catch. `os.Exit` takes 1 to 125, so `read` returns `Option[Options]` and help is `None`. A machine's `initial` is a fixed value, so the game starts paused on `world.Blank()` and a `Load` input brings the seeded box. Laws are checked, not run (#162): a provider that breaks `ShowCountsOne` binds without a word, so each law is restated as a property test.
