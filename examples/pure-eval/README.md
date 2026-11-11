# pure-eval

A token-bucket rate limiter. A bucket holds tokens up to its capacity, each request costs tokens, and time refills the bucket at its rate. The program reads a script of requests and waits and prints the limiter's verdict on each request.

Tag: `default`. It is an original program in a neutral domain. Its goldens were written by hand from the rules below.

The point of the example is **check-time evaluation**: the limiter is pure, so `ergo check` runs its tests and laws itself, with no Go build, and gives the same verdicts as `ergo test` ([testing](../../docs/testing.md#check-time-evaluation)).

## Rules

- `bucket CAPACITY RATE` starts a new full bucket. The bucket at the start holds 10 tokens and gains 1 a tick.
- A request costs `light` 1, `heavy` 3 or `bulk` 10 tokens. When the bucket holds its tokens, it is `allowed` and takes them. When the cost is more than the capacity, it is `refused`. Else it is `delayed` by the least number of ticks after which it would be allowed, and takes nothing.
- `wait TICKS` refills the bucket by its rate a tick, never past its capacity.
- Blank lines and lines that start with `#` are skipped. Any other line is an error: exit 1 with `pure-eval: line N: …` on stderr.

## Layout

A module of packages (`ergo.mod`: `module pureeval`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `limit/` | The domain package. It is pure. `bucket.ergo`: `Bucket`, `Cost`, `Verdict`, `New`, `Refill`, `Decide`, `Apply`, `Text`. `laws.ergo`: four top-level laws. `tests.ergo`: two tests with no parameters, which are examples. |
| `cmd/pure-eval/` | The binary. It reads the script from standard input and prints each verdict. It is the only part with a capability. |

## What it shows

- **Pure functions over records and unions**, with an exhaustive `match` on `Cost` and on `Verdict`.
- **Top-level laws.** `RefillNeverOverfills`, `VerdictIsExact` and `DelayIsLeast` take drawn integers, which `bucket` maps onto a valid bucket. They are sampled: 100 cases, `tested`. `CostsAreOrdered` takes two `Cost` values, which can be listed, so every pair runs and it is `proved`.
- **The same verdicts at both seams.** `ergo check limit` runs the tests and laws in the checker's process. `ergo test limit` builds and runs them as Go. For one seed both draw the same cases, so a refuted law names the same case at both.

## Check-time verdicts

`ergo check limit` (seed 1, the default budget of 1,000,000 steps and 64 MiB). Steps are the evaluator's count of work, not a time.

| obligation | status | evidence | cases | steps |
| --- | --- | --- | ---: | ---: |
| `BurstThenWait` | passed | evaluated | 1 | 638 |
| `TextIsLowerCase` | skipped | | | 21 |
| `law RefillNeverOverfills` | passed | tested | 100 | 20090 |
| `law VerdictIsExact` | passed | tested | 100 | 19230 |
| `law DelayIsLeast` | passed | tested | 100 | 22454 |
| `law CostsAreOrdered` | passed | proved | 9 | 650 |

The evaluation's summary status is `skipped`, because one obligation is skipped; the exit status is 0. `ergo test limit` runs all six and passes.

**Skips.** `TextIsLowerCase` calls `strings.ToLower`, a Go function. Check-time evaluation never runs Go code, even a pure function, so `ergo check` lists the test as skipped with the reason `a Go function (ToLower), even a pure one` and the replay command `ergo test limit --seed 1`. `ergo test` runs it.

## A mutant

Change the guard of `Refill` in `limit/bucket.ergo` so that it forgets the rate:

```
-	if ticks > room/b.Rate {
+	if ticks > room {
```

A bucket with a rate above 1 then fills past its capacity. Both seams refute `RefillNeverOverfills` at the same drawn case: `ergo check limit` exits 1 with the law `refuted`, its case, its arguments and its replay command; `ergo test limit` exits 1 with the law's `FAIL` line.
