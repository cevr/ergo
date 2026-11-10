# router-model

A model of a request router: a route table that matches a method and a path to a handler, and a server machine that takes requests, reloads its table and drains before it stops. The program reads a script of signals and requests and prints what the server and the table do.

Tag: `default`. It is an original program in a neutral domain. Its goldens were written by hand from the rules below.

The point of the example is **check-time evaluation of a machine**: the server's step is pure, so `ergo check` searches the machine and runs its laws itself, with no Go build, and gives the same verdicts as `ergo test` ([testing](../../docs/testing.md#check-time-evaluation)).

## Rules

- The table: `GET /` → `index`, `GET /health` → `health`, `GET /items` → `list`, `PUT /items` → `store`, and a second `GET /items` → `shadowed`, which the first route of the same method and path hides. A request to a path with routes of other methods only is `405 method not allowed`; a path with no route is `404 not found`.
- The server starts `serving` the blue table with no open request. `begin` opens a request while fewer than 2 are open, and is `rejected` otherwise. `end` closes one; with none open it is `ignored`.
- `reload` starts loading the other table; the server keeps serving the old one, and takes and closes requests. `loaded` swaps to the new table (`swapped to green`).
- `drain` stops taking requests: `begin` is `rejected`. `halt` stops a draining server with no open request (`closed`), and is `rejected` while one is open. A stopped server ignores every signal.
- A line is a signal (`begin`, `end`, `reload`, `loaded`, `drain`, `halt`) or a request `get|put|delete PATH`. Blank lines and lines that start with `#` are skipped. Any other line is an error: exit 1 with `router-model: line N: …` on stderr.

## Layout

A module of packages (`ergo.mod`: `module routermodel`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `route/` | The domain package. It is pure. `table.ergo`: `Method`, `Route`, `Lookup`, `Match`, the `Table`, and the top-level law `LookupIsTheTable`. `server.ergo`: the state `Server`, `Signal`, `Event`, `Step`, and `type Router machine[Server, Signal]` with its four laws. |
| `cmd/router-model/` | The binary. It reads the script from standard input and prints each step and answer. It is the only part with a capability. |

## What it shows

- **A pure machine with laws.** `Router` starts at `Serving{Gen: Blue, InFlight: 0}` and moves by `Step`. A request past `MaxInFlight` is rejected, so the machine has 16 states, and its signals can be listed.
- **Reachability laws, searched.** `CanStop reaches Stopped` and `Swaps path [Serving, Reloading, Serving]` are proved by the shortest run the explorer finds: `drain, halt` and `reload, loaded`.
- **Body laws, listed.** `InFlightBounded` and `DrainingRefusesNew` take a `Signal`, which can be listed, so every signal runs and both are proved.
- **A top-level law with an exhaustive `match`.** `LookupIsTheTable` takes a `Method` and a `Probe`, both listed: 12 cases, proved.

## Check-time verdicts

`ergo check route` (seed 1, the default budget of 1,000,000 steps and 64 MiB). Steps are the evaluator's count of work, not a time. For a machine search, `states`, `edges` and `depth` are the explorer's; its `steps` are the step calls, with the replays that rebuild a state.

| obligation | status | evidence | cases or search | steps |
| --- | --- | --- | --- | ---: |
| `law Router.CanStop` | passed | proved | 9 states, 24 edges, depth 2; run `Drain, Halt` | 1130 |
| `law Router.Swaps` | passed | proved | 3 states, 12 edges, depth 2; run `Reload, Loaded` | 744 |
| `law Router.InFlightBounded` | passed | proved | 6 cases | 6117 |
| `law Router.DrainingRefusesNew` | passed | proved | 6 cases | 1420 |
| `law LookupIsTheTable` | passed | proved | 12 cases | 2329 |

The evaluation's summary status is `passed` and the exit status is 0. No obligation is skipped. `ergo test route` runs the same five laws and passes. `cmd/router-model` has no obligation.

## A mutant

Let a draining server take a request, in the `Draining` arm of `Step` in `route/server.ergo`:

```
 		case Begin:
-			Reject
+			Enter[Server]{Next: Draining{InFlight: d.InFlight + 1}}
```

Both seams refute `DrainingRefusesNew` at the signal `Begin`: `ergo check route` exits 1 with the law `refuted` and its case; `ergo test route` exits 1 with the law's `FAIL` line.
