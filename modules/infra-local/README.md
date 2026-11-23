# infra-local

An Ergo module of this repository (`modules/`, MK0). It holds [`infra`](../infra/README.md)'s drivers for the machine a program runs on (IA2).

| Package | What |
| --- | --- |
| `infra-local/local` | `Dirs`, `Files` and `Programs`, each an `infra.Driver` over std `fs` and `exec`; `Ledger`; the health checks `HTTP` (the host's), `Up` and `Down` (in memory); `Rows` (an `infra.State` in a ledger, with `DirRowsOn`, `FileRowsOn`, `ProcessRowsOn`) and `FileLock` (an `infra.Lock` in a file), so a stack's rows and lock outlive the run (PX6) |

## Kinds

| Kind | Props | Attributes | Apply | Delete |
| --- | --- | --- | --- | --- |
| directory | `DirProps { Path }` | `Dir { Path }` | `MkdirAll` | removes the directory |
| file | `FileProps { Dir, Name, Text, Mode }` | `File { Path, Size }` | writes the whole file every time, so changed text replaces the old | removes the file |
| program | `ProcessProps { Program, Args, Env, Dir, Port, Health }` | `Process { URL, Pidfile }` | starts the program detached (`exec.Spawner.Spawn`, in a session of its own); keeps a program that runs with the same props; ends the old program and starts the new one when the props change; fails, and ends the program, when `Health` is set and a GET of it gets no 2xx answer | ends the program's process group: SIGTERM, then SIGKILL after the grace period |

Paths are relative to the driver's base folder. A path element that is empty, `.` or `..` fails the Apply. `Args` and `Env` hold one entry per line, and `Env`'s lines are `KEY=VALUE`.

Each driver keeps a ledger, a file per object in its own folder, named by the kind and the hex of the id. The ledger holds the object's path or the program's pid, start time, props and port. `Read` and `Delete` get only the id, so they read the ledger. A program counts as running only when it is the same process, by pid and start time, so a reused pid is never taken for it.

`main` binds the host's providers. The programs it starts are the user's, so they get this process's whole environment, passed on by `InheritEnv` (std exec: a program sees only the environment it is given):

```
let host = fs.NewHost()
let checks = try local.NewHTTP(50, 100ms)
let programs = local.ProgramsOn(host, host, exec.NewHost().InheritEnv(os.NewHost().Environ()), func(url string) bool { checks.Ready(url) }, base, base + "/.infra", 5s)
```

A law or a test binds memory ones: `fstest.Memory`, `exec.Processes`, and `Up` or `Down`.

## Laws

- `infra.Driver`'s four laws (`AttrsIsApply`, `RetrySafe`, `ReadAfterApply`, `DeleteIsIdempotent`) run for `NewDirs`, `NewFiles` and `NewPrograms`, on `fstest.Memory` and `exec.Processes`.
- `ChangedFileHoldsTheNewText`: an Apply of a changed text writes the new text.
- `DeleteEndsTheProgram`: after a Delete, the program no longer runs. `DeleteIsIdempotent` reads only the ledger, so it cannot see this.
- `SecondApplyKeepsTheProgram`: a resumed run starts nothing twice.
- `ChangedProgramRestarts`: changed props end the old program and start the new one.
- `UnansweredLeavesNothing`: a program that fails its health check is ended and forgotten.
- `infra.State`'s laws (`PutThenGet`, `RemoveThenGet`) run for `NewIntRows`, and `infra.Lock`'s (`LockExcludes`, `BreakNamesTheHolder`) for `NewFileLock`. A row is kept as hex lines (id, status, place, the props as the text its kind's `show` gives, JSON for the three kinds); a row it cannot read reads as none. std `fs` has no exclusive create, so two runs that take `FileLock` at the same instant can both get it.

On the host, `cmd/ergo`'s `TestInfraLocalStartsARealProgram` (full tier) runs the same driver with `fs.NewHost` and `exec.NewHost`. It starts `sleep`, finds it again, keeps it on a second Apply, and its Delete ends it.

The mutant ledger is `testdata/mutants.json`.
