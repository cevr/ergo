# metrics

A time-series aggregator: it reads timestamped samples, reports tumbling windows with their percentiles, and raises alerts from a sliding mean, at most a fixed number of them a minute.

```sh
metrics report 60000 < cpu.txt    # a line per minute: count, sum, min, max, p50, p95
metrics alert 70 < cpu.txt        # alert when the mean of the last 10 s is above 70
```

Samples are `AT VALUE` lines, `AT` in Unix milliseconds, in time order; blank lines and `#` comments are skipped. A line that is not a sample, or a sample older than the one before it, is a typed failure (`BadSample`, `Unordered`) and exits 1 with its line number.

## What it shows

- **A machine for the alert limiter** (`alert/limiter.ergo`): `State = Quiet | Firing`, `Signal = Breach | Calm`, and `Step` matches the pair exhaustively. A breach while quiet alerts unless `Limit` alerts were sent in the last `Window`, and is then muted; a breach while firing is the same incident; a calm check resolves it.
- **Laws checked against a naive fold**: `series.TumblingSumsEqualFold` (each bucket's sum and count are the fold of its window, and the buckets tile time), `series.SlidingAgrees` (the incremental sliding sum equals a fold after every `Add`), `series.PercentileIsARank` (nearest rank, monotone in p), `alert.NeverExceedsRate` (no window of the policy holds more than `Limit` alerts, for any policy and any signals) and `alert.BreachesAreAccounted` (every breach alerts, is muted or falls in an open incident; every resolution names its alert).
- **A virtual clock**: `series.Stamp` reads `time.Clock`, and the binary's test `VirtualClockFeed` drives a live feed from a `time.Manual`, advancing it 2 s per sample, so the third incident within a minute is muted without waiting.
- **Typed failures at each edge**: `Parse` fails `BadSample | Unordered`, `Tumbling` and `NewSliding` fail `BadWidth`, and `main` turns each into an `os.Exit` with its own message.

## Layout

- `series/`: the data. `sample.ergo` (`Sample`, `Parse`, `Stamp` needs `time.Clock`), `window.ergo` (`Tumbling`, `Bucket`, `Sliding` with `Add`, `Count`, `Sum`, `Mean`, and `Percentile`), `laws.ergo` (the window laws and the seeded `Run` they draw from), `tests.ergo`.
- `alert/`: `limiter.ergo` (`Policy`, `Signal`, `State`, `Step`, `Event`, the `Limiter` machine, whose input is the policy, `Drive`), `laws.ergo`, `tests.ergo`.
- `cmd/metrics/main.ergo`: the binary. It binds the host's arguments and standard input in `main`'s context and is the only place that chooses them; the alert policy (2 a minute) and the smoothing width (10 s) are its constants.

## Cases

`report` (one-minute windows of a CPU trace), `alert` (four alerts, one muted), `bad-line` (exit 1), `usage` (exit 2).

## Limits found

The program hit these, recorded in `gaps-*.md`:

- G-ex-c-16: a machine has no context. The policy, the sent times and the muted count are repeated in both variants of `State`.
- G-ex-d-9 (still open after T4): generated slices now grow to 25 elements, but a law needs 80 signals in time order to spend the rate, so each law still takes a seed and builds its own run of samples or signals.

Closed in batch 12: G-ex-d-11 (V2 and V4: `Limiter`'s input is the policy, and `Drive` runs `machines.Start(Limiter{}, p)`'s step from its initial state, with `machines.Next`); G-ex-a-9 and G-ex-b-12 (V5: `Step` emits each alert, mute and resolution, so `alert.Next` is gone; a signal the step ignores is `Nothing`); G-ex-f-1 (S2 `slices.Slice` and `slices.Clone` drop the window's dead prefix); G-ex-d-5 (W3: `Bucket` has `==`); G-ex-d-10 (L3: `main` holds the body of `run`); the property tests that restated the laws (V1 runs them).
