# quota

A double-entry table of storage quota between volumes, behind an operator's console. The operator types commands, one per line, on standard input: open a volume (with an overcommit, the size it may go below zero, or none), grant, reclaim, move, ask a size, list the volumes. Space only moves: every move takes from one volume and gives to another, and space from outside comes from the volume `pool`, so the sizes always add up to zero. Each move has a key, and a move sent again with its key is posted once. The quota lives in memory for one run, or with `-journal FILE` in a journal file that outlives it.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and checked by hand: the sizes of the `basic` case add up to zero, and each refusal is the guard the rules name.

## Layout

The program is a module of packages (`ergo.mod`: `module quota`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `quota/` | The domain package. `table.ergo` (`Move`, `Record`, the typed failures `UnknownVolume`, `VolumeExists`, `BadFloor`, `BadSize`, `SameVolume`, `NoSpace`, `KeyReused`, `Unavailable`; `Table` with unexported fields, `Empty`, `Open`, `Apply`, `SizeOf`, `Total`, `IDs`), `store.ergo` (the contract `Store` with the laws `TotalIsKept`, `PostIsIdempotent`, `FloorHolds`), `laws.ergo` (the Table's laws, and `Sample`, `Day`, `Posted`), `tests.ergo`. Pure. |
| `console/` | The console. `command.ergo` (the `Command` union, `Parse`, `ParseAll`, which fail `BadCommand`; `Gigabytes`, `Size`), `console.ergo` (`Do`, `Serve`, which need `quota.Store`), `laws.ergo`, `tests.ergo`. |
| `memory/` | `Store`, a `quota.Store` over one Table in a `sync.Ref`; `Files`, an `fstest.Memory` holding given files, for tests. |
| `file/` | `Journal`, a `quota.Store` over a journal file read and written through `fs.Files` and `fs.Writes`; `format.ergo` (`OpenLine`, `PostLine`, `Replay`, which fails `Corrupt`), `laws.ergo`, `tests.ergo`. |
| `cmd/quota/` | The binary: the flag `-journal`, and the one place that chooses the store, `memory.NewStore()` or `file.At(path, disk, disk)` over the host's `fs.NewHost()`. |

## What it shows

- **A contract with laws, kept by two stores.** `quota.Store` declares `Open`, `Post`, `SizeOf` and `Volumes` with their typed failures, and three laws. `memory.Store` and `file.Journal` both keep it; each package's tests restate the laws over the store after a generated day of moves. Both stores are thin: each operation is a step of the pure `Table`, so the rules live in one place.
- **Typed failures as answers.** `Apply`'s guards are in order (the key, the size, the volumes, the floor), each its own failure with the facts an operator needs (`NoSpace{ID, Have, Size, Floor}`, `KeyReused{Key, Was}`). `console.Do` turns each refusal into an answer and goes on; only `Unavailable` (a disk that fails, a journal changed by hand) stops the console with exit 1.
- **Idempotent by key.** A key posted before gives its first record and changes nothing (`#3 m1` twice in `basic`, and `g1` again in the `journal` case, posted on an earlier run); the same key for a different move is `KeyReused`.
- **Laws.** The Table: `SizesAddUp`, `ApplyIsIdempotent`, `FloorsHold`, `ApplyLeavesItsArgument`. The Store: `TotalIsKept`, `PostIsIdempotent`, `FloorHolds`. The journal: `ReplayRebuilds`, `AnyWordKeeps` (a key or volume of any text, tabs and line feeds too, reads back). The console: `SizeReadsGigabytes`. The Table, journal and console laws run as they are (`ApplyIsIdempotent` and `ApplyLeavesItsArgument` draw their move from `Day`, so it meets the day's volumes and keys); the Store laws run in `memory` against `memory.Store`, and `file`'s tests restate them for `file.Journal`, which `ergo test` skips.
- **Exhaustive matches.** `Parse` matches the pair (verb, word count); `Do` matches every `Command`; `Replay` every journal line kind.
- **Providers chosen in one place.** Only `cmd/quota/main.ergo` binds `quota.Store`, `io.Stdout`, `io.Stdin` and `os.Process`, and builds the host's disk. The disk is a parameter of `run`, so the tests run the journal on an `fstest.Memory`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/quota/cmd/quota < examples/quota/cases/basic/stdin
echo 'open home
grant g1 home 10' | ./bin/ergo run examples/quota/cmd/quota -- -journal /tmp/quota.journal
echo 'size home' | ./bin/ergo run examples/quota/cmd/quota -- -journal /tmp/quota.journal
./bin/ergo test examples/quota/quota     # and console, memory, file, cmd/quota
go run ./scripts/examples quota
```

## Cases

`basic` (a day on a memory quota table: grants, moves, a move sent twice, an overcommit used and one exceeded, an unknown volume, a self-move, a reused key, a zero reclaim), `journal` (a journal from an earlier run, made by `setup.sh`: its key is not posted twice), `nodir` (a journal in a missing directory: exit 1 at the first write), `badline` (a size with three places, exit 2) and `help`.

## Limits found

Each gap is in the lane's gap file (`gaps-ex-b.md`) with a repro.

- **G-ex-b-13** (language; diagnostic): a law of `Store` cannot call a helper that needs `Store`, so `TotalIsKept` writes its sum out twice; and an operation named `Move` hid the type `Move` inside the laws ("Move is not a type"), so the operation is `Post`.
Closed in batch 12: G-ex-b-14 (the journal appends one line per change), G-ex-b-15 (a `Map` is a value: `Apply` changes its own copy, `clone` is gone, and `ApplyLeavesItsArgument` still holds it), G-ex-b-3 (`memory.Store` keeps its Table in a `sync.Ref`, each change one `sync.Modify`), G-ex-b-8 (`ergo run DIR -- -journal FILE`).

- **G-exup-1** (closed in batch 13): the Store laws run in `memory` against `memory.Store` (`NewStore`). Since fu7a they run on `file.Journal` too (`NewJournal`, over the case's own `fstest.Memory`); `file`'s tests restate them on a journal that already has volumes.
- **G-exup-5** (language; measured): a union's variants are records of its own package (S108 "wrap quota.VolumeExists"), so `memory.Store` cannot carry the refusal of a change out of `sync.Modify` as a value of `quota.KeyReused | …`. It keeps the Table the change found and runs the pure `quota.Apply` on it again outside, which gives the same record or refusal.
- Closed: `try` may start any operand (q105), so `sum + try f()` needs no `let`. `number` is a prelude name and cannot name a function. A provider's function cannot add a need (`Store` over `needs fs.Files` is S106, README Gap 17), so `Journal` holds its `fs.Files` and `fs.Writes` as fields. No decimal type and no codec (#49): space is `int64` hundredths with the program's own `Gigabytes` and `Size`, and the journal format is hand-written with `strconv.Quote`.
