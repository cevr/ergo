# particles

A small deterministic particle game in a text box. Particles move by whole cells each step, bounce off the four walls (and lose speed there when `-loss` is set), and pass their velocities round when they meet in a cell. A fixed-step loop advances the box once per 20 ms of game time, whatever time each 50 ms frame took, and shows each frame as text. Keys pause, resume and quit the game.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program; the worked steps in `world/tests.ergo` were checked by hand, and the cases were checked against them and against the laws (the energy line of each frame, the steps per frame of the memory clock).

## Layout

The program is a module of packages (`ergo.mod`: `module particles`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `world/` | The box, the domain package. `world.ergo` (`Particle`, `World` with construction authority, `Empty`, `Blank`, `Spawn`, `Energy`, the typed failures `BadSize`, `BadLoss`, `TooMany`), `step.ergo` (`Wall`, `Event`, `Step`, `Run`, `Tally`), `laws.ergo`, `tests.ergo`. Pure, except `Spawn`, which needs `Random`. |
| `sim/` | The game. `machine.ergo` (`Game`, the `Phase` and `Input` unions, `Begin`, `Step`, the `Sim` machine, `Key`), `loop.ergo` (the contract `Screen` with its law, `Pace`, `Play`, the fixed-step loop), `laws.ergo`, `tests.ergo`. |
| `render/` | The text: `Box` draws a box, `Status` the line under it. Pure. |
| `term/` | `Screen`, the `sim.Screen` provider over an `io.Stdout` value: it clears a terminal before each frame. |
| `memory/` | `Clock`, a `Scheduler` and `time.Clock` that moves on at once when the loop sleeps, a quarter late on every third frame; `Tape`, a `sim.Screen` that keeps its frames. |
| `cmd/particles/` | The binary: the flags, the seeded `Random`, and the context that chooses the clock (`memory.Clock`, or the host's with `-live`) and the screen. |

## What it shows

- **Exact numbers, so laws are exact.** Positions and velocities are `int`s, and energy is `Σ vx² + vy²`, so `ElasticKeepsEnergy` compares with `==`. A seed replays a run on any host (`SeedReplays`), because the only randomness is `Random`, bound to `rand.Seeded(seed)` in a context.
- **Laws.** `world` states five (`EnergyNeverRises`, `ElasticKeepsEnergy`, `StaysInTheBox`, `SeedReplays`, `StepCountsOne`), `sim` five over the machine and the loop (`PausedIgnoresTicks`, `RunningTicksStay`, `QuitIsDone`, `PauseResumeRoundTrips`, `StepsFollowTheClock`), and the contract `Screen` one (`ShowCountsOne`), which `memory.Tape`, `term.Screen` and the test screens keep. `ergo test` runs the laws; it runs `ShowCountsOne` against `memory.Tape` (`NewTape`), and against `term.Screen`, `tape` and `blind` built field by field: an `io.Stdout` field holds an `io.Recorder` and a `sync.Ref` field a new cell (fu7a builders). `memory`'s test restates it for a Tape in any state.
- **A machine.** `type Sim machine[Phase, Input]` with `Phase = Running | Paused` and `Input = Tick | Pause | Resume | Quit`. Its input is the box: `initial: Begin` runs a fresh game of it, and `Play` starts it with `machines.Start(Sim{}, w)` and moves with `machines.Next`. `Step` is one `match` over the pair that names every case, and uses `Enter`, `Stay`, `Ignore` and `Done`; it never emits, so `Play` moves on an `Emit` as on `Enter` or `Stay`.
- **A fixed-step loop.** `Play` sleeps a frame on `Scheduler`, reads `time.Clock`, and ticks the machine once per whole `Dt` it owes, carrying the rest. The steps are the elapsed time over `Dt`, rounded down (`StepsFollowTheClock`), whatever the frames took.
- **Typed failures.** `Spawn` fails `BadSize | BadLoss | TooMany`; `cmd` turns each into an `os.Exit` with its own message.
- **Providers chosen in one place.** Only `cmd/particles/main.ergo` binds `Random`, `Scheduler`, `time.Clock`, `sim.Screen`, `io.Stdout` and `os.Process`. Its tests run the same game on a `Recorder`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/particles/cmd/particles -- -seed 3 -frames 10
./bin/ergo run examples/particles/cmd/particles -- -live -frames 40   # paced by the host's clock
./bin/ergo test examples/particles/world
go run ./scripts/examples particles
```

## Cases

`basic` (the defaults), `keys` (a pause, a resume and a quit, with loss at the walls: the energy falls), `help`, `badsize` (exit 2) and `unknown` (an unknown flag, exit 2 with the usage).

## Limits found

Each gap is in the lane's gap file (`gaps-ex-b.md`) with a repro.

- **G-ex-b-4** (partly closed by S4): `d / 4` on a `duration` is still refused with "write a duration literal such as 4s", but `time.Divide(d, 4)` and `time.Count(elapsed, d)` now do the clock's arithmetic.
- **G-ex-b-5** (partly closed by C1): `testing.VirtualClock` jumps when every task waits, but it sleeps exactly; `memory.Clock` stays, because it makes every third frame late, which the fixed-step loop must carry.
- **G-exup-1** (closed in batch 13): `sim.Screen`'s law runs in `memory` against `NewTape()`. `sim`'s test screens `tape` and `blind`, and `term.Screen`, run it too (fu7a: a capability field is built by its builder; a `World` by `world.NewWorld`).
- **G-exup-2** (checker; type inference): `machines.Next(Running{G: g}, d)` is S108, since inference takes `S` from the variant; `PauseResumeRoundTrips` writes `let running Phase = Running{G: g}` first.
- **G-exup-4** (closed in batch 13, fu7a): a `sync.Ref` is built by `sync.NewRef`, so the law runs for the two test screens and their restating test is gone.
- By design, and still friction: a `try` inside a `with` block leaves the function (rows.md, Error rows 4), so the test calls `game` to catch the exit. `os.Exit` takes 1 to 125, so `read` returns `Option[Options]` and help is `None`.

Closed in batch 12: G-ex-b-1 (K2: `time.Times(1ms, if …)` with no `let` first), G-ex-b-2 (T3: a failed `assert a == b` shows both values), G-ex-b-3 (`sync.Ref` holds `memory.Tape`'s frames and the screens' counts), G-ex-b-6 and G-ex-b-8 (T1: `ergo run DIR -- ARGS`, no `dist/`), G-ex-b-7 (T2: `ergo test` prints lines), the `Load` input and the paused blank start (V4: the machine's input is the box), the hand `Next` (V2: `machines.Next`), `world.Same` (W3: `==`), `run` (L3: `main` returns from its `with` block), and the property tests that restated the running laws (V1).
