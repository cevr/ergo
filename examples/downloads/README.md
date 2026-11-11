# downloads

A download manager. It reads a list of files and fetches each one, chunk by chunk. After a timeout it waits and retries, and each wait is twice the one before. A file ends once, with one value: saved, failed or cancelled.

Tag: `default`. It is an original program, a neutral re-creation of a common pattern: a class B machine with state-owned work, typed failures, a timer whose length depends on the state, an exit value per actor, and a progress view from an observer. Its goldens were made by running the program, and then checked line by line against the rules below.

## Rules

- File states: `queued`, `fetching from N` (the state's work fetches the chunk at byte N) and `waiting` (the state's timer waits before a retry). `Begin` starts a queued file.
- A chunk enters `fetching` again from the next byte, which is a new entry and so a new work. The last chunk ends the file `saved`.
- A timeout (`TimedOut`, a failure of the source and a signal) waits `100ms` after the first, then `200ms`, then `400ms` (`Backoff`, the timer of `Waiting{Try}`). The fourth timeout fails the file (`Budget` is 3). `NotFound` fails it at once.
- `Cancel` ends a file from every state, with the bytes it had. `get URL cancel` cancels the file at its first wait.
- The memory server (`memory.Server`) has `a.txt` (1000 bytes), `b.img` (3500) and `slow.iso` (2500) in chunks of 1000 bytes. `slow.iso` times out twice at byte 1000, counted across fetches. `late.bin` always times out. `gone.zip` is not found.
- Each line is the time on the manual clock (`@MS`) and the file's view: the bytes so far and the wait, shown once per change of the view. The last line of each file is its end, read from the actor's exit value.

## Layout

| folder | what it holds |
| --- | --- |
| `fetch/` | The domain package. `file.ergo`: `Chunk`, the failures `NotFound` and `TimedOut`, the contract `Source { Chunk fails NotFound \| TimedOut }` with its laws, the state `File`, `Signal`, `End`, `Backoff`, the work `fetching`, `Timer`, `Step` and the machine `Download`. `laws.ergo`: the step's rules. |
| `memory/` | `Server`, a `fetch.Source`. `laws.ergo`: the live rules over the actor. |
| `script/` | `Read` (fails `BadLine`), `View` and `Play`, which runs each file as an actor on a manual clock. |
| `cmd/downloads/` | The binary. It binds `fetch.Source` to `memory.NewServer()` and `Scheduler` to the manual clock that `Play` moves. It is the only place that chooses them. |

## What it shows

- **State-owned work with typed failures (MU4).** `fetching` gives `Fetching` a work against `Source`. `NotFound` and `TimedOut` are failures and signals, so the work needs no failure map (#95).
- **A dynamic timer.** `Timer(Waiting{Try})` is `Backoff(Try)`: the timer's length comes from the state.
- **Exit values (MU12).** `Done[End]` ends the actor, and `actor.ExitOf` reads `Finished{Output}`. A file's end is one value, not a state.
- **Observers (MU12).** `actor.Select(a.Changes(), ViewOf)` gives a line only when the view (`View{Got, Wait}`, a comparable record) changes.
- **Exhaustive `match`.** Every step arm names each state. A chunk in a state that has no work is `Ignore`.

## Laws

| law | where | rule | mutant it kills (measured, `MB2/downloads/mutants.ts`) |
| --- | --- | --- | --- |
| `BudgetIsExact` | fetch | n timeouts with their retries leave a file fetching while n ≤ Budget; the next fails it | budget off by one |
| `RetryWithinBudget` | fetch | no state holds more than Budget timeouts | — (`BudgetIsExact` kills it first) |
| `BytesAddUp` | fetch | a saved file has the bytes of every chunk it took | a chunk's bytes not added; a retry from byte 0 |
| `CancelAlwaysEnds` | fetch | Cancel ends every state, with its bytes | a cancel that loses the bytes |
| `BackoffDoubles` | fetch | 100ms, then twice the wait before | a constant wait |
| `EndIsTheFile` | memory, live | each file's end is the server's: saved with every byte, failed, or cancelled at its first wait | the last chunk ignored; NotFound retried; a wait with no timer; a server that times out more |
| `WaitsDouble` | memory, live | waits are 100, 200, 400ms, Budget at most | (with `BackoffDoubles`) |
| `ProgressOncePerChange` | memory, live | no two progress lines in a row are the same | — (Select's rule; runner's) |
| `Source.ChunksJoin`, `NotFoundIsFinal` | contract | chunks join up to a last one; not found is final | — (run against `Server`) |

All 9 measured mutants die by an assertion. If the timer never fires, `Play` would wait for ever, so it first checks that the waiting state has a timer. When it has none, `Play` says so and cancels the file.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/downloads/cmd/downloads < examples/downloads/cases/files/stdin
./bin/ergo test examples/downloads/fetch
./bin/ergo test examples/downloads/memory
go run ./scripts/examples downloads
```

## Cases

- `files`: a small file, a larger one, one that times out twice and then is saved, one that fails after four timeouts, and one that is not found.
- `cancel`: a file cancelled at its first wait, then fetched again. The server still counts the earlier timeout, so this time it times out once only.
- `badscript`: a line that is not `get` (status 1).

## effect-machine side by side (README's first machine, widened; em-parity plan §2)

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| the fetch | `.task(State.Fetching, …)` with `onSuccess`/`onFailure` | `work: fetching`, with failures that are signals | compiler + runner |
| retry wait | a `.timeout` per state, or `Schedule.exponential` in the task (inferred) | `after: Timer`, whose `In` is `Backoff(Try)` | runner + laws |
| the end | `awaitExit` / final state | `Done[End]` and `ExitOf` | runner (MU12) |
| progress | `subscribe` with a selector | `actor.Select(Changes, ViewOf)`, a loop | runner (MU12) |

Where Ergo could not improve:

- **A manager with a child per file (MB F8).** The intended form: a `Manager` machine owns one file child per download (MU5), answers progress by `Ask`, and shuts down by `Drain`, which lets queued files finish and refuses new ones. The child's row is `fetch.Source`, and starting a child needs `Scheduler`, so the manager's work is `machines.Work[Order, fetch.Source | Scheduler]`. That row now checks (MB F8 is fixed). The manager does not run on the manual clock (measured, wf10 F8): a work that started a child waits for its children's stop when its body returns, and the clock does not see that wait, so the first `Advance` while a file waits (its retry timer) waits for ever ("all goroutines are asleep"). Until that wait is one the clock sees (runtime.md, Machines and actors 16), the script is the manager: it runs files one at a time and does not drain.
- **The manual clock (MB2-G2).** `Play` moves the clock by the wait it reads from the view. Files run one at a time, so there is only one timer at a time. Several files at once would need the clock to wait until every woken task has stepped.

## Benchmark

The em-parity §3 scenarios for this program (actor send, Ask, spawn/stop, memory per actor, child fan-out) are measured by MU3's and MU5's benchmarks. The command for an end-to-end time of the recorded cases is in `MB2/bench-pending.md` (downloads).

## Limits found

- **A work's wait for its children is not a clock wait** (above; found in wf10 F8): no manager actor with children, and no `Drain`.
- **MB2-G2**: one file at a time on the manual clock.
- **No `Timeout` name of its own.** A program type named `Timeout` hides the prelude's `Timeout[E]`, which the machine's `after` needs, so the failure is `TimedOut`.
