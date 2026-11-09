# search

Search as you type over a small catalogue, with a cart. After each key the search waits for a pause, then searches. The last query wins: a key during a search stops that search. A hit can be added to the cart, and checkout gives the order. The program runs a script of timed keys on a virtual clock and shows each change of state and each output, at the millisecond it happens.

Tag: `default`. It is an original program, a neutral re-creation of a common pattern: a class B machine with a timer and state-owned work. Its goldens were made by running the program, and then checked line by line against the rules below by hand.

## Rules

- States: `idle`, `typing`, `searching`, `showing`, `adding` and `problem`. Every state holds the cart.
- A key always re-enters `typing` with the longer query. This restarts the pause and stops any work that runs.
- `typing` waits for a pause of 250ms with no key (its timer), then `searching` starts the search of the query (its work).
- A search takes 400ms and an add 100ms (`search.Typical`). The result of the search shows the hits (`! shown`). If the index is down, the state is `problem`.
- `choose N` adds hit N to the cart (`adding`). A hit the screen does not show is refused (`? no hit`). The result shows the hits again (`! put`). If the cart is full, the state is `problem`.
- `back` goes from `problem` to the hits. `clear` goes to `idle`. `checkout` ends the run with the order (`! order`).
- At one instant, a key or another outside signal goes first, then the timer, then a result of work.
- When an entry ends, its timer is dropped and its running work is stopped. Work that ended at the very instant its entry ended has its result in the inbox already: the runner drops that result by its entry epoch (`- dropped a result of an ended entry`). So a result never reaches a later entry.
- The memory catalogue holds ten items. Its index takes at most 12 letters: a longer query fails `IndexDown`. The memory basket has room for three items.

Script: each line is `@MS WORD` (milliseconds from the start, never going back). WORD is `key TEXT`, `clear`, `choose N`, `back` or `checkout`. The last line is `@MS end`. Blank lines and lines that start with `#` are skipped.

## Layout

The program is a module of packages (`ergo.mod`: `module search`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `search/` | The domain package. `contracts.ergo`: `Item`, the typed failures `IndexDown` and `CartFull`, and the contracts `Catalog { All, Find fails IndexDown }` and `Cart { Room, Add fails CartFull }`, with their laws. `machine.ergo`: the state `Search`, `Signal`, `Output`, `NoSuchHit`, `Pause`, `Job` and `Work` (the work of each state), `Timer` (the `after` function), `Step`, and `type Finder machine[Search, Signal] { initial: Start, step: Step, after: Timer }`. `drive.ergo`: `Drive`, the runner written by hand, and its `Report`. `laws.ergo`: the machine's rules as functions. |
| `memory/` | `Shelf` (a `search.Catalog`) and `Basket` (a `search.Cart`). `laws.ergo` states the machine's laws over them; `tests.ergo` holds the contract laws' tests and the example's own run. |
| `script/` | `Read` (fails `BadLine`) and `Run`, which needs `Console`, `io.Stdin`, `search.Catalog` and `search.Cart`. |
| `cmd/search/` | The binary. `main.ergo` binds `let Live = context { search.Catalog: memory.Shelf, search.Cart: memory.Basket }` and the host's standard input. It is the only place that chooses them. |

## What it shows

- **State-owned work, by hand (M45).** `Work(s Search) Option[Job]` names the work of each state, as `Timer` names its timer. `Drive` starts the job when the entry opens and stops it when the entry ends. The job's typed failure becomes a signal: `IndexDown` becomes `IndexFailed`, `CartFull` becomes `Full`.
- **Entry epoch (M46).** Each job's result carries the epoch of the entry that started it. `Drive` drops a result of an ended entry before the step. The case `race` shows this with a key at the very instant a search ends.
- **Runner-owned timer (V5 `after`).** The pause is `Typing`'s timer. A key re-enters `Typing` with `Enter`, so the pause starts again: the debounce needs no counter.
- **`Emit` (q28; MQ1).** The result of a search moves to `Showing` and tells `Shown` in one decision. An add tells `Put`.
- **Input-initial (V4).** The machine's input is the cart it begins with: `machines.Start(Finder{}, []Item{})`.
- **Exhaustive `match`.** `Step` names all ten signals, and each arm whose answer depends on the state names all six states. A result in a state that has no work is `Ignore`. The only `case _` arm is over a script word.
- **Typed failures.** `IndexDown`, `CartFull`, `NoSuchHit` (a step failure: the runner shows it and keeps the state) and `BadLine`.
- **Laws.** Four laws over the machine, run by `Drive` on the virtual clock, with key times drawn from integers:
  - `HitsForLatestQuery`: once the keys stop, the hits shown are those of the whole text typed;
  - `AtMostOneSearch`: two searches never run at once;
  - `NoStaleHits`: every show has the hits of its own query, and no result of an ended entry reaches the step (half the gaps put a key at the instant a search ends);
  - `DebounceCoalesces`: keys each within 250ms of the one before give one search.

  The contracts have three member laws: `Catalog.FindIsSubset` (every hit is in the catalogue and holds the query), `Catalog.FindIsStable` (the hits keep the catalogue's order) and `Cart.AddAddsOne` (an add puts one item at the end, or fails `CartFull` exactly when the cart is full). A provider has no state (M23), so the same query always gets the same answer; order is the stability that a provider can break.
- **Tests.** The laws are the tests. `ergo test` runs the three contract laws in `memory` against `Shelf` and `Basket`; `memory/tests.ergo` calls their functions and has one scripted run, `SearchRun`.

## Limits found

- **No state-owned work (M45; MU4).** `Drive` is a runner written by hand: `drive.ergo` is about 240 lines (measured: 261 with `Said`) that a `work` member of the machine and a live runner would replace. It runs on a virtual clock, and the job calls its provider when it starts, so the program is a replay, not a live search (MU3, #57).
- **The entry epoch is kept by hand (M46).** `Drive` keeps the counter and drops stale results. `machines.RunTimed` drops a stale timer, but it has no work, so it cannot run this machine.
- **No observation of a running machine (M50).** The timeline comes from the lines that `Drive` writes, not from a subscription to the machine.
- **Machine laws need a provider (M55).** The machine's rules need `Catalog` and `Cart`, and a law needs no service. So each rule is a function of `search`, and `memory` states it as a law with a `with` of its own providers. The contract laws are functions too, as in keypad.
- **G-ma-1** (closed in batch 13): the examples runner tests package `search` too; its contract laws run in `memory`, where their providers are.
- **No `==` on slices (M29).** `Names` turns items into one text to compare.
- **No duration × int (M24).** A millisecond is `duration(int64(n) * 1000000)`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/search/cmd/search < examples/search/cases/browse/stdin
./bin/ergo test examples/search/memory
go run ./scripts/examples search
```

## Cases

- `browse`: two keys in one pause give one search; an add; a hit not on the screen; clear; a key that stops a running search; checkout.
- `full`: the fourth add fails `CartFull`, and back shows the hits again; a query over 12 letters fails `IndexDown`.
- `race`: a key at the very instant a search ends; the search's result is dropped by its epoch.
- `badscript`: a hit that is not a number (status 1).
