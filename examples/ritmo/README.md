# ritmo

A port of ritmo's top-down game ([typeonce-dev/ritmo](https://github.com/typeonce-dev/ritmo) at 4e46848, `integration/top-down`) and of the parts of ritmo's ECS core it uses. A player walks a walled room of 2200 by 1600 units and collects five pickups.

The game reads one request per line from standard input, so a game replays from a file:

```sh
ritmo [-store memory|disk] [-dir DIR] [-keep TICKS] < SCRIPT
```

| line | what it does |
| --- | --- |
| `KEYS[*N]` | N fixed ticks (1 when `*N` is left out) holding KEYS: `w a s d` walk, `e` interacts, `.` is no key. Prints the status after the last tick. |
| `look` | The room (`#` wall, `*` pickup, `@` player, 100 units a character), then the status |
| `save NAME`, `load NAME` | Keep the world under NAME, and go on from it. `-store disk` writes `DIR/NAME.json`. |
| `rewind N` | Go back N ticks, at most `-keep` (600 by default). |
| `quit` | Stop. |

A line that fails prints `?` (not a request) or `!` (a typed failure: `TooFar`, `Missing`, `Broken`). The run then ends with status 1 after the last line.

Tag: `default`. It is a port without `origin` and `record.sh`. The original's lengths are floats and the port's are integers (see below), so the original cannot record its cases.

## Layout

| folder | what it holds |
| --- | --- |
| `ecs/` | ritmo's core as values: `Entity`, `Column[T]` (one component's values by entity) and `Command[D]` (a spawn or a despawn queued for the Apply step), with the column's laws beside it. |
| `topdown/` | The domain. `world.ergo` holds the components, the drafts and the `World`. `machines.ergo` holds the `Facing` and `Locomotion` machines. `systems.ergo` holds the systems, the two schedules, and ritmo's invariants and fuzzer as laws over them. `api.ergo` holds the Api (`Session`, `Request`, `Reply`, `Handle`, the failures), the ring's rewind laws and the `Saves` contract with its laws. |
| `memory/` | `Shelf`, the in-memory `topdown.Saves`. |
| `disk/` | `Folder`, the `topdown.Saves` over std `fs` and `json`. |
| `screen/` | The world as text, in place of ritmo's renderer adapters. |
| `script/` | The transport: lines of standard input to requests, replies to standard output. |
| `cmd/ritmo/` | The binary. It chooses the store and binds the host's streams and files. |

## How ritmo maps

- **Schema and `requires`:** a record per kind of entity. ritmo's `requires(Player, Position, Velocity, Collider)` is `PlayerDraft{At, Vel, Box}`: a spawn without a collider is a literal that leaves `Box` out, which does not check.
- **Systems:** functions from the world to the next world. No system's row names `Random` or a clock, so none can read one. That is ritmo's determinism lint, here checked by the compiler.
- **Schedules:** `Setup` and `Update` run the systems in ritmo's order.
  - **Apply** makes the queued spawns and despawns (`applyDeferred`).
  - **Commit** steps each machine on its queued signal (`applyStateTransitions`).
- **State machines:** `machine[Dir, Turn]` and `machine[Gait, Stride]`. Each has an exhaustive step and a law that `ergo test` decides on the whole graph.
- **Checkpoint and rewind:** a session keeps the worlds of its last `-keep` ticks (600 by default) in a ring, and a tick writes one slot of it. A world is a value, so a rewind is going back to one. ritmo's checkpoint has to record ids, change ticks, queued states and read positions to do the same. The law `RewindReplays` holds ritmo's promise: replaying the same input after a rewind gives the same world, bit for bit.
- **Invariants and the fuzzer:** these are laws over 400 ticks of seeded input:
  - `PlayerStaysInTheWorld`;
  - `NeverInsideAWall`;
  - `CollectedMatchesTheRoom` (ritmo's `CollectedAtMostTotal`, made exact);
  - `FocusIsInReach`;
  - `MachinesFollowTheWalk`.
- **Snapshot:** std `json` derives the saved form from the `World` record. `Saves`' laws run against both stores.

## Gaps, stubbed at the seam

The full list, with a proposed unit for each, is in the survey (`research/RITMO/survey.md`).

- **Lengths are integers (G3).** They are thousandths of a unit, where ritmo has `float64`. ritmo ports fdlibm so that every engine gives the same bits. Ergo's std `math` has no trigonometry yet, and Go may fuse a float multiply and add on arm64 but not on amd64. A float game recorded on one host could replay differently on the other; integers cannot.
- **No window (G6).** `screen` prints text until the desktop host for std `ui` lands.
- **No gamepad (G5).** Only keys, one line per tick.
- **No random state in the world (G2).** The top-down game draws none, but a game that does needs a generator that is a value, since std `rand`'s `PCG` is a shared stream.
- **Columns listed by hand (G1).** `Despawn` names every column; a derive over the world's columns would write it.
