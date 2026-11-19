# admit

A job admission tool built on a real third-party Go module, `golang.org/x/sync`, through `import go`. Jobs share a capacity while they run. admit checks every job's payload first, then admits the jobs through a gate and prints each step. It is the example that trusts Go for an external library: its Go providers call Go methods, Go calls that fail with `go.Error`, a call that takes the task's context, a variadic call and a Go callback that can fail.

```sh
admit 4 < jobs           # the gate over x/sync's semaphore, the checker over its errgroup
admit -memory 4 < jobs   # the same run with admit/memory's providers, in Ergo alone
```

Each input line is `NAME WEIGHT PAYLOAD`, where the payload is numbers separated by commas. A job that weighs more than the whole capacity is refused. A job whose weight is free starts, and any other job is queued. The started jobs finish and give their weight back, and then each queued job waits for its weight. Then admit prints the refused jobs, joined by Go's `errors.Join`, and exits with status 1 when any job was refused.

## Layout

- `jobs/` is the domain: `Job`, `Parse`, the `Event` union a run prints, and `Run`, which matches every event exhaustively. It also holds two contracts with laws:
  - `Gate` (`Capacity`, `Take`, `Wait`, `Give`), with the laws `GiveRestores`, `NeverOverTakes`, `WaitTakes` and `RunGivesBack`;
  - `Checker` (`Check`), with the law `NamesFirstBad`, which holds every checker to `FirstBad`, the reference.
- `memory/` holds the providers in Ergo alone: a gate counted in a `sync.Ref`, and a checker that checks the payloads in turn. `ergo test memory` runs every law against them.
- `semaphore/` is `jobs.Gate` over `golang.org/x/sync/semaphore`:
  - `Take` is the Go method `TryAcquire`;
  - `Wait` is `Acquire`, which takes the calling task's context, so a stop of the task ends the wait. Its Go error is the typed `jobs.Stopped`;
  - `Give` is `Release`.
- `errgroup/` is `jobs.Checker` over `golang.org/x/sync/errgroup`:
  - each payload is checked by a callback that fails with Go's error, on a goroutine of the group's;
  - `Wait` fails with the first error;
  - which goroutine fails first is Go's choice, so a failed group names its job through `jobs.FirstBad`.
- `cmd/admit/main.ergo` is the only place that chooses the providers.

## Go module

`go.mod` requires `golang.org/x/sync v0.24.0` and replaces it with `testdata/sync`. That folder is a committed copy of the module's `semaphore` and `errgroup` packages with its LICENSE, so a build reads no network and no module cache. The bindings under `ergo.bind/` are written by `ergo bind go golang.org/x/sync/semaphore golang.org/x/sync/errgroup`.

The laws of `semaphore.Gate` and `errgroup.Checker` do not run under `ergo test`, because a test never holds `go.Foreign`. The cases run those providers, and the `memory` case holds them to the same output as admit/memory's.
