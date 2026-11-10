# jobq

A job queue with workers, retries with exponential backoff, and a token-bucket rate limiter. It reads a list of jobs on standard input (`NAME MS [FLAKY] [fatal]`: how long one try runs, how many tries fail for a moment before it works, and whether it cannot work at all), runs them, and prints what happened. By default it runs them on a virtual clock and prints the timeline, which is the same on every host and takes no real time. With `-live` it runs them on real tasks, as a Go worker pool would, on the host's clock, and prints how each job ended.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program; the run in `sched/tests.ergo` and the start of `basic` were checked by hand against the rules (two workers, a burst of two then one start each 100 ms, backoff 100 ms doubling to 1 s).

## Layout

The program is a module of packages (`ergo.mod`: `module jobq`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `jobs/` | The domain package. `job.ergo` (`Job`, the `Result` union `Ok \| Transient \| Fatal`, `Policy`, `Backoff`, `Scripted`, and the contract `Worker` with the law `IsRepeatable`), `life.ergo` (the `Phase`, `Signal` and `Outcome` unions, `Step`, the `Life` machine), `script.ergo` (`Script`, the demo `Worker`), `laws.ergo`, `tests.ergo`. Pure. |
| `limit/` | The token bucket: `Bucket`, `Full`, `Refill`, `Take`, `NextAt`, `Tokens`, on a time it is given. Pure. |
| `sched/` | The queue on a virtual clock: `Config`, the `Event` union, `Run`, which jumps from one event to the next; `laws.ergo` (`Load`, `Shape`, `Ran`, `InFlight` and the laws), `tests.ergo`. Pure, except that each try's result comes from `jobs.Worker`. |
| `pool/` | The queue on real tasks: a `queue.Queue` of jobs, worker tasks (`task.Go`), a pacer task that refills a queue of tokens (Go's buffered-channel limiter), and a queue of `Report`s. The same `jobs.Life` machine decides each retry. |
| `cmd/jobq/` | The binary: the flags, the job reader, the timeline and report text, and the context that binds `jobs.Script` and the host's scheduler. |

## What it shows

- **A machine for each job.** `type Life machine[Phase, Signal]`: `Queued{N, Ready}` and `Running{N, Ends}`, with `Start` and `Finish` signals; one `match (p, s)` that refuses a start before the backoff ends and a finish before the try ends, and decides `Done` (success, fatal, out of tries) or `Enter` (queue the next try after its backoff). The virtual queue and the real pool both run it, so they cannot disagree on a retry (`AgreesWithTheVirtualRun` checks they do not).
- **A virtual clock that the queue owns.** `sched.Run` never sleeps: time moves to the next try to end, the next job to become ready, or the bucket's next token. A run replays exactly, so the laws compare whole timelines.
- **Laws.** `jobs`: `IsRepeatable` (the contract), `BackoffGrows`, `BackoffIsCapped`, `RetriesEnd`, `FatalIsFinal`, `RetryWaitsItsBackoff`. `limit`: `RateHolds`, `NeverOverBurst`, `NextAtHasAToken`. `sched`: `NoJobLost`, `AtMostNInFlight`, `StartsKeepTheRate`, `RetriesWait`. `ergo test` runs each (`IsRepeatable` against the `Script` worker). The property test `LiveAgreesWithVirtual` found a real bug while the program was written: a try of 0 ms never ended, because the virtual clock looked only for events after now. `NoJobLost` now draws jobs from 0 ms.
- **Typed failures and exhaustive matches.** A try's `Result` is a union, and each `match` over `Result`, `Phase`, `Event` and `Outcome` names every variant. The pool's queues fail `queue.Closed`, in the rows of the functions that push and pop.
- **Real concurrency, tested.** The pool uses `task.Go`, `queue.Queue` with backpressure and `Stop` on the pacer. Its tests bind a `testing.VirtualClock` as the `Scheduler`, so the tasks run for real and the sleeps move virtual time only.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/jobq/cmd/jobq -o /tmp/jobq
/tmp/jobq < examples/jobq/cases/basic/stdin
/tmp/jobq -workers 4 -burst 1 -every-ms 50 -attempts 5 < examples/jobq/cases/basic/stdin
/tmp/jobq -live < examples/jobq/cases/basic/stdin
./bin/ergo test examples/jobq/sched     # and jobs, limit, pool, cmd/jobq
go run ./scripts/examples jobq
```

## Cases

`basic` (seven jobs on the defaults: retries, a fatal job, a job out of tries), `wide` (four workers, one start each 50 ms, five tries), `live` (real tasks with millisecond times; the report leaves out the worker and the time, which depend on the host), `help`, `badjob` and `badflag` (exit 2).

## Limits found

Each gap is in the lane's gap file (`gaps-ex-b.md`) with a repro.

- **G-ex-b-16** (partly closed by K3): `let res = if … { Ok } else { rs[i] }` now joins to the union. `let mut best = None` still needs `Option[duration]`, and the checker now says so (S108 "best is typed None by its value; write let mut best Option[duration] = None").
- **G-ex-b-17** (language; known #197): a `Task` cannot be put in a slice, so a pool of `-workers N` is `pool.crew`, a recursion of one frame per task.
- **G-ex-b-18** (closed by owner q26, rechecked in L9): a package `let` of a Map was global mutable state that a "pure" function wrote. Under the value rule `let mut c = calls` copies, so the write never reaches the package `let` and the function stays pure (measured: two calls print `1 1`; `TestValueRuleAgrees` holds the repro as `next`).
- **G-ex-b-5** (closed by C1): the pool's tests and `cmd/jobq`'s now bind `testing.VirtualClock` as the `Scheduler` (the hand `memory.Instant` is gone). The laws stay `sched`'s, and the pool is held to `sched` by `AgreesWithTheVirtualRun`.
- **G-ex-b-9** (partly closed by S4): `time.ParseDuration` now exists, but `flag` has no `Duration`, so the times stay `-backoff-ms` and `-every-ms` ints.
- **G-ex-b-17** also meets C2: `tasks.Transform` runs work on a bounded set of tasks, but its function must give a value, and a worker gives none, so `crew` stays.
- By design, and still friction: `Task[T, E]` needs an error type, and there is no empty row to write for a task that cannot fail. A worker in the pool holds its worker for its backoff (a sleep), as a simple Go pool does, where `sched` frees it; both runs agree on each outcome, not on the times.

Closed in batch 12: **G-ex-b-1** (K2: `duration(if … { -b } else { b })` with no `let` first), the hand `Next` (V2: `machines.Next`), and the property tests that restated the laws (V1 runs the laws). The pool's and the queue's `Emit` arms are `Unreachable`: `jobs.Step` never emits.
