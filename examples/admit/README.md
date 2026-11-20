# admit

A job admission tool whose gate is built on a real third-party Go module, `golang.org/x/sync`, through `import go`. Jobs share a capacity while they run. admit checks every job's payload first, then admits the jobs through a gate and prints each step. It is the example that trusts Go for an external library: its Go gate calls Go methods, and a Go call that takes the task's context and fails with `go.Error`. The checker is Go's errgroup shape written on std `tasks.Transform`, with no Go (owner q278).

```sh
admit 4 < jobs           # the gate over x/sync's semaphore, the checker on std tasks
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
- `errgroup/` is `jobs.Checker` in Go's errgroup shape, on std `tasks.Transform`:
  - each payload is checked on a task of its own, at most the limit at once;
  - the first check to fail stops the rest;
  - which check fails first depends on the tasks, so a failed run names its job through `jobs.FirstBad`.
- `cmd/admit/main.ergo` is the only place that chooses the providers.

## Go module

`go.mod` requires `golang.org/x/sync v0.24.0` and replaces it with `testdata/sync`. That folder is a committed copy of the module's `semaphore` package with its LICENSE, so a build reads no network and no module cache. The binding under `ergo.bind/` is written by `ergo bind go golang.org/x/sync/semaphore`.

`ergo test` runs the `jobs.Gate` laws against `semaphore.Gate` natively: its operations call Go, and the test binary's law case holds `go.Foreign` to build it (types.md Laws 10, owner q277), so `ergo check` reports them `native`. `ergo test` runs the `errgroup.Checker` law as it runs any Ergo provider's. `ergo check` skips it, because the check-time evaluator has no native of `tasks.Transform`. The cases run both providers, and the `memory` case holds them to the same output as admit/memory's.
