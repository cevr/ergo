# lower-v1 build and benchmark receipt

Measured head: `367835b3d077de918e87903b134d301cd7a642d2` (fix(lower): purity ignores source names; a pure Go method meets a host protocol only on purpose). The commit that adds this receipt changes this file only, so it changes no measured code. A later code commit invalidates the receipt, and the verifier runs it again.

The run is the whole-unit acceptance of lower-v1, `accept.sh --repo <checkout> --skip-gate`, at the measured head on a committed, clean tree: `CHECKS 222 FAILURES 0`, `ACCEPT PARTIAL` (the gate leg ran apart, under the shared gate lock). Its receipts are in the unit's run directory `receipts/drive-lz5bqiq3/` (`speed.json`, `benchmark.txt`, `bench-ergo.txt`, `bench-go.txt`, `benchstat.txt`, and one JSON record per command). They are not in the repository.

**Verdict: inconclusive.** The 1x target (Q-S5-4, D-S5-11) asks for a run at load average 8 or lower. The load stayed between 22 and 32 for the whole hour this round waited (one reading each 10 minutes, 17:45Z to 18:45Z) and was 28.9 at the start of the run. The numbers below are a measurement under load, not a pass.

## Machine

| Item | Value |
| --- | --- |
| CPU | AMD EPYC 9554P 64-Core Processor (exe workbox VM) |
| OS | Linux 6.12.93 (x86_64, glibc 2.39) |
| GOMAXPROCS | 16 (16 vCPUs) |
| Toolchain | go version go1.27.0 linux/amd64 |
| benchstat | golang.org/x/perf v0.0.0-20261009192801-be2c69fb417e |
| Load average | 28.88 29.69 33.47 at the start, 34.65 33.92 34.72 after the benchmark. Other lanes shared the host. |
| Bounds | one systemd scope, MemoryMax=8G, no swap, GOMEMLIMIT=6GiB, a private GOCACHE per run |

## The README program: cold, warm and edit builds

The program is the README's opening program (`examples/checkout/main.ergo`). Cold means a fresh Ergo emission cache and a fresh Go build cache, so the cold build compiles Go's standard library too.

| Build | Seconds |
| --- | --- |
| cold | 5.494 |
| warm 1 to 5 | 0.199, 0.252, 0.208, 0.227, 0.244 (median 0.227) |
| edit (one source line changed) | 0.309 |

Each warm build reused the cold build's emitted module (same module directory, unchanged files), and the edit made a new module (measured by the acceptance checks "warm emission reused" and "changed source cache miss").

## The matched benchmark

The kernel is `accept/bench/fold.ergo`:

```go
func Fold(n int) int {
	let mut total = 0
	for i in 0..n {
		total += (i*17 + 3) % 97
	}
	total
}
```

It lowers to `func Fold(n int) int`, with no environment argument and no interruption point (D5; the acceptance check "pure Fold has no hidden argument"). `BenchmarkErgo` calls the emitted `Fold(10000)`, and `BenchmarkGo` calls a hand-written Go `Fold` beside it in the same module. Ten rounds ran, each one `go test -bench '^Benchmark(Ergo|Go)$' -benchmem -benchtime=100ms` process that runs both, so load drift reaches both sides alike.

| Round | Ergo ns/op | Go ns/op |
| --- | --- | --- |
| 1 | 16310 | 16769 |
| 2 | 18093 | 14649 |
| 3 | 16158 | 20910 |
| 4 | 15050 | 15950 |
| 5 | 19563 | 16938 |
| 6 | 14060 | 20400 |
| 7 | 14440 | 22471 |
| 8 | 15607 | 15428 |
| 9 | 15506 | 17442 |
| 10 | 14556 | 18883 |

| | Ergo | Go |
| --- | --- | --- |
| median ns/op | 15556.5 | 17190.0 |
| B/op (every round) | 0 | 0 |
| allocs/op (every round) | 0 | 0 |

Ratio of the medians, Ergo over Go: 0.905. benchstat: `sec/op 17.19µ ± 22% (Go) vs 15.56µ ± 16% (Ergo), ~ (p=0.052 n=10)`; B/op and allocs/op `~ (p=1.000 n=10), all samples are equal`.

Under the D-S5-11 rule the run is not red: benchstat shows no significant slowdown, and Ergo allocates no more than Go. The spread (± 16% and ± 22%) and the load make it weak evidence, so the verdict stays inconclusive until a run at load 8 or lower.

## Scope

One matched arithmetic kernel. This receipt makes no end-to-end performance claim. A wider kernel set is #190's.
