# search

Search as you type over a small index of file names, with pins. After each key the search waits for a pause, then searches. The last query wins: a key during a search stops that search. A hit can be pinned, and open gives the pinned entries. The search is a machine run live as an actor: its pause is a state timer and its search is a state's work, both owned by the runner. The program plays a script of timed keys against it on a manual clock and shows each state the actor commits and each output it tells, at the millisecond it happens.

Tag: `default`. It is an original program, a neutral re-creation of a common pattern: a class B machine with a timer and state-owned work. Its goldens were made by running the program, and then checked line by line against the rules below by hand.

## Rules

- States: `idle`, `typing`, `searching`, `showing` and `problem`. Every state holds the pins.
- A key always re-enters `typing` with the longer query. This restarts the pause and stops a search that runs.
- `typing` waits for a pause of 250ms with no key (its timer), then `searching` searches the query (its work). The result shows the hits (`! shown`). If the index is down, the state is `problem`.
- `pin N` pins hit N (`! pinned`). A hit the screen does not show is refused (`? no hit`). The pins hold three entries (`search.Room`): a fourth is a `problem`.
- `back` goes from `problem` to the hits. `clear` goes to `idle`. `open` ends the run with the pinned entries (`! opened`).
- At one instant, a timer that is due goes first, then the script's signal (case `tie`). The memory index answers a search at the instant it starts.
- When an entry ends, the runner drops its timer and interrupts its work, and drops a result of an ended entry by the entry's id, so a result never reaches a later entry (runtime.md, Machines and actors 12; MU4).
- The memory index holds ten file names. It takes at most 12 letters: a longer query fails `IndexDown`.

Script: each line is `@MS WORD` (milliseconds from the start, never going back). WORD is `key TEXT`, `clear`, `pin N`, `back` or `open`. The last line is `@MS end`. Blank lines and lines that start with `#` are skipped.

## Layout

The program is a module of packages (`ergo.mod`: `module search`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `search/` | The domain package. `contracts.ergo`: `Entry`, the typed failure `IndexDown`, and the contract `Index { All, Find fails IndexDown }` with its laws. `machine.ergo`: the state `Search`, `Signal`, `Output`, `NoSuchHit`, `Pause`, `Room`, the work `find`, the timer `Timer`, `Step`, and `type Finder machine[Search, Signal] { initial: Start, step: Step, after: Timer, work: find }`. `laws.ergo`: the step's own law. |
| `memory/` | `Listing`, a `search.Index`. `laws.ergo` states the search's rules over the live actor, with `Listing` and a fresh manual clock. |
| `script/` | `Read` (fails `BadLine`), `Play`, which runs the search as an actor and gives its timeline, and `Run`, which needs `Console`, `io.Stdin`, `Scheduler` and `search.Index`. |
| `cmd/search/` | The binary. `main.ergo` binds `let Live = context { search.Index: memory.Listing }`, a `time.Manual` for `Scheduler`, and the host's standard input. It is the only place that chooses them. |

## What it shows

- **State-owned work (MU4).** `find` gives `Searching` a `machines.Work` against `Index`. The runner starts it after the entry commits and before an observer sees the entry, and interrupts it when the entry ends. `IndexDown` is a failure of `Index` and also a `Signal`, so the work needs no failure map; the hits need `OnSuccess`, which makes them `Found`.
- **Runner-owned timer (`after`).** The pause is `Typing`'s timer. A key re-enters `Typing` with `Enter`, so the pause starts again: the debounce needs no counter.
- **Observers (MU12).** `Play` reads the actor's `Changes`: every state the actor commits, once each, in order. It starts the read before each signal, so it misses no state the signal causes, also those of the work that answers at once.
- **`Emit` (q28).** The result of a search moves to `Showing` and tells `Shown` in one decision. A choice tells `Pinned`.
- **Input-initial (V4).** The machine's input is the pins it begins with: `machines.Start(Finder{}, []Entry{})`.
- **Exhaustive `match`.** `Step` names all eight signals, and each arm whose answer depends on the state names all five states. A result in a state that has no work is `Ignore`. The only `case _` arms are over a script word and in a law.
- **Typed failures.** `IndexDown` (a failure of the work and a signal), `NoSuchHit` (a step failure: `Ask` gives it to the asker and the actor goes on) and `BadLine`.

## Laws

| law | where | rule | mutant it kills (measured, `MB2/search/mutants.sh`) |
| --- | --- | --- | --- |
| `HitsForLatestQuery` | memory, live | once the keys stop, the screen shows the hits of the whole text typed | the work searches another query |
| `DebounceCoalesces` | memory, live | the search runs once after the last key and once in each gap of a pause or more | pause of 300ms; the key before the timer at a tie; a key that stays (no new pause; a deadlock) |
| `PinsHoldRoom` | search | a hit is pinned unless the pins hold `Room` entries | room off by one |
| `Index.FindIsSubset`, `Index.FindIsStable` | contract | every hit holds the query; hits keep the index's order | — (run against `Listing` in memory) |

The live laws run the actor for each generated case, so they also check the runner with this machine. The runner's own rules (one work at a time, no result of an ended entry stepped) are std actor's: its Quint model and its property hold them (MU4), and no edit of this program can break them. The cases add the mutant "no `work` member" (the search never answers: a deadlock).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/search/cmd/search < examples/search/cases/browse/stdin
./bin/ergo test examples/search/search
./bin/ergo test examples/search/memory
go run ./scripts/examples search
```

## Cases

- `browse`: two keys in one pause give one search; a choice; a hit not on the screen; clear; a key after a search types on; open.
- `full`: the fourth choice is a problem, and back shows the hits again; a query over 12 letters fails `IndexDown`.
- `tie`: a key at the very instant the pause ends: the timer goes first.
- `badscript`: a hit that is not a number (status 1).

## effect-machine side by side (`examples/core/src/services-and-tasks.ts`)

effect-machine writes this search with a `Context.Service` class for the index, `State`/`Event` schemas, `.task(State.Searching, ({ state }) => Effect.flatMap(Index, c => c.search(state.query)), { name, onSuccess, onFailure })`, an `.on` per (state, event), `.final` states, `Machine.run` with an input, and `Effect.provideService`.

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| the service | a `Context.Service` class with a tag | `type Index { Find(query string) []Entry fails IndexDown, law … }`: an operation type whose laws every provider runs | compiler + laws |
| the work of a state | `.task(State.Searching, …)` in the builder chain | `work: find`, one function over the state union: one work per state, the match exhaustive | compiler |
| result → event | `onSuccess` and `onFailure`, pinned by overloads; a missing one is TS2769 "No overload matches this call" | `IndexDown` is a `Signal`, so no failure map; the hits need `OnSuccess`, and the checker names a missing map | compiler |
| a result after the state's exit | a plain `self.send` from the task fiber; a result queued before a re-entry can reach the new entry (inferred, em-parity §4.2) | the runner drops it by the entry's id | runner (MU4 model and property) |
| required services | `R` in the machine's type, provided with `Effect.provideService` | `needs Index` from the work's row; only `main` binds it | compiler (equal) |
| debounce | a `.timeout` or a hand timer with a generation counter | `after: Timer`; `Enter` restarts it | runner + law `DebounceCoalesces` |
| observing | `actor.subscribe` callbacks on the actor's fiber | `for s in a.Changes()`: a loop, no callback, every committed state once | runner (MU12) |

Where Ergo could not improve:

- **Two services in one work row (MB F8).** `machines.Work[Signal, Index | Pins]` does not parse, so a machine whose works need two contracts cannot be written. The pins were a contract (`Pins { Room, Add fails PinsFull }`) with an `adding` state and a work; it is now the state's data and `Room` a constant. effect-machine's `R` takes a union.
- **A latency of its own.** A provider of an operation type cannot wait on the `Scheduler` (its methods name no capability), and a work cannot need `Index | Scheduler` (F8), so the memory index answers at once. A search can no longer still run when a key comes, and the old `race` case (a result of an ended entry) moved to the runner's property.
- **Signatures and type arguments** are written where effect-machine infers (`Some[Timeout[Signal]]{…}`, `Emit[Search, Output]{…}`).

## Benchmark

No em-parity §3 scenario is this program's own: an `Ask` per key, a work start per search and a timer per key are measured by MU3's, MU4's and MU18's benchmarks. The command for an end-to-end time of the recorded cases is in `MB2/bench-pending.md` (not run; it needs the quiet bench window).

## Limits found

- **MB F8** (above): the pins contract and its work had to go.
- **MB2-G2** `time.Manual.Advance` wakes a timer's sleeper and returns; it does not wait until the actor has stepped the timer's signal. `Play` knows when a timer is due only because it reads the machine's own `Timer` and keeps the time of each entry (and that a `Stay` is no new entry). A driver of a live actor on a manual clock needs a way to wait until the actor is idle (Effect's `TestClock.adjust` runs the woken fibers first).
- **MB2-G3** An `Emit`'s output reaches the sink after the state is published, so `Play` reads `State` (which waits for the step's end) before it polls the outputs, and shows the outputs after the states.
- **No `==` on slices (M29).** `Names` turns entries into one text to compare.
- **No duration × int (M24).** A millisecond is `duration(int64(n) * 1000000)`.
