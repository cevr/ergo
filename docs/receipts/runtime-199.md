# Runtime #199 cost receipt

Measured head: `9902494c7cd3546d1ffc133fdb527b2035c7c148` (docs(spec): #199 final contract status). This receipt is the only change after it. The benchmarks ran on that committed, clean tree.

This receipt measures the cost of the interruption polls of runtime.md I1 against a handwritten Go control. It answers the #199 stage 5 cost item and records the evidence for the #190 bar.

## Machine

| Item | Value |
| --- | --- |
| CPU | AMD EPYC 9554P 64-Core Processor (exe workbox VM) |
| OS | Linux 6.12.93 |
| Architecture | amd64 |
| GOMAXPROCS | 16 (16 vCPUs) |
| Toolchain | go version go1.27.0 linux/amd64 |
| benchstat | golang.org/x/perf v0.0.0-20261009192801-be2c69fb417e |
| Load average | 7.12 9.22 12.91 before the run, 3.61 7.25 11.80 after it. Other agent lanes shared the machine. No gate or other run of this lane ran at the same time. |

## Workload

`TestRuntime199CostModule` (internal/sketch/lower/runtime199_cost_test.go) writes one Go module. The module holds:

- the actual emitted code of an Ergo program, from the lowerer at the measured head;
- a handwritten target-shape control;
- a counting copy of both (package `counted`), in which each poll site also increments a counter.

`BenchmarkRuntime199Emitted` and `BenchmarkRuntime199Target` run in one process. Each has three sub-benchmarks:

| work | One op | Size |
| --- | --- | --- |
| dispatch | one capability call `Sink.Put(x)` through the env's provider | 1 call |
| impure-loop | a numeric loop in a frame whose footprint is not empty, then one capability call | costLoopN = 1024 iterations |
| pure-loop | the same loop in a pure frame: the negative control, no poll on either side | costLoopN = 1024 iterations |

The target keeps the emitted contract: the same `rt.Task`, an env parameter that carries it, a provider interface whose operation takes the caller's task, and a poll at the same sites. The workload registers no cleanup and opens no scope, so neither side does ownership work. The target leaves out only the emitted representation: the operations adapter (`opsKeepAsSink`), the `Env` it makes for each call, and the `Range` value of the loop.

Only the generated calls are timed. The go command builds the module before the benchmark process starts. The env and its provider are built before `b.Run`. The poll count runs on the counting copy before `b.ResetTimer`. A per-benchmark accumulator and a package sink keep every result. `TestCostContract` in the module proves that the target computes the emitted results, polls as often, and that a stopped task interrupts both sides at the same sites.

## Poll counts

Static polls are `.Check()` calls in the Go source, counted with go/parser by the test. Dynamic polls are counted at the poll site by the counting copy, for one op, and reported as the `polls/op` metric.

| work | Static polls (emitted function) | Emitted polls/op | Target polls/op |
| --- | ---: | ---: | ---: |
| dispatch | 1 (`dispatch`) | 1 | 1 |
| impure-loop | 2 (`impure`: the back-edge and the capability call) | 1025 | 1025 |
| pure-loop | 0 (`pure`) | 0 | 0 |

The impure loop polls once for each of its 1024 iterations and once at its capability call.

## Results

`-count=10`, all samples kept. ns/op is the benchstat median with its 95% confidence interval.

| work | Emitted ns/op | Target ns/op | benchstat (Emitted vs Target) | B/op | allocs/op |
| --- | ---: | ---: | --- | --- | --- |
| dispatch | 3.225 ± 7% | 3.271 ± 14% | ~ (p=0.631, n=10) | 0 and 0 | 0 and 0 |
| impure-loop | 680.2 ± 16% | 713.7 ± 5% | ~ (p=0.393, n=10) | 0 and 0 | 0 and 0 |
| pure-loop | 376.6 ± 14% | 363.8 ± 11% | ~ (p=0.796, n=10) | 0 and 0 | 0 and 0 |

Raw ns/op samples, in run order:

| Benchmark | Samples |
| --- | --- |
| Emitted dispatch | 3.857 3.211 3.253 3.170 3.190 3.450 3.180 3.296 3.239 3.058 |
| Emitted impure-loop | 704.3 667.3 803.4 792.0 635.4 671.9 751.1 664.0 663.6 688.6 |
| Emitted pure-loop | 348.5 323.4 378.7 489.8 367.7 374.5 430.0 319.0 383.1 412.6 |
| Target dispatch | 3.090 3.729 3.678 3.880 3.337 3.171 3.187 3.204 3.382 3.188 |
| Target impure-loop | 678.3 719.6 721.6 647.9 813.4 689.5 714.2 683.9 713.2 733.4 |
| Target pure-loop | 450.2 359.8 332.1 364.3 363.2 402.6 350.9 400.7 338.6 395.6 |

Outliers, reported and kept: Emitted dispatch 3.857 and Target dispatch 3.880; Emitted impure-loop 803.4 and 792.0, Target impure-loop 813.4; Emitted pure-loop 489.8, Target pure-loop 450.2.

## Against the #190 bar

The #190 bar is parity within noise at 0 B/op. Here parity means that benchstat finds no significant difference and B/op is equal.

| work | Verdict |
| --- | --- |
| dispatch | Parity: no significant difference (p=0.631), 0 B/op on both sides. |
| impure-loop | Parity: no significant difference (p=0.393), 0 B/op on both sides. |
| pure-loop | Parity: no significant difference (p=0.796), 0 B/op on both sides. |

Limits of this verdict:

- The intervals are wide (up to ±16%) on a shared, loaded machine. The samples do not exclude a difference smaller than the interval. This receipt makes no zero-cost claim.
- The parity is against a target that keeps the poll. It does not measure the cost of a poll against Go with no poll. Within each side, impure-loop and pure-loop differ in the poll, the capability call and the frame, so their difference is not a poll cost either.
- `-count=10` runs every Emitted sample before every Target sample. A load change during the run can bias one side.
- The workload has no `with` acquisition or release in the timed code. Their cost is not measured here.
- No result lost against the bar, so the receipt has no gap to report.

## Corpus

The corpus measurement is a test overlaid on the lower package at the measured head (`TestS5bR1Corpus`, kept in the unit's tools as `s5b-r1-tools/zz_s5b_corpus_test.go`). It lowers every tracked `*.ergo` and `*.ef` file.

| Figure | Value |
| --- | --- |
| Tracked files | 745 |
| Programs that lower clean | 112 |
| Emitted bytes (main.go) | 355399 |
| Static polls (`.Check()` calls) | 131 |
| Frame footprints | 1505 |

The figures equal the figures at the base `741d48d6`: this stage changes no lowering.

## Ledger rows (stage 5a ledger, 5b)

| Row | Disposition |
| --- | --- |
| s1r1-f1 | Routed to #190. A host purity declaration, so that a pure extern (`Append`, `Get`) stops costing a poll and stops making a frame opaque, is a language feature, not a cost fix. |
| s1r3-o1 | Routed to #190. Host and runtime calls marked as non-re-entering, so that they stop forcing entry polls, is a runtime feature. |
| s1r3-o3 | Measured: the dynamic poll count per op is in the poll count table above (1, 1025, 0 on both sides). |
| s1r5-m2 | The tracked corpus holds no Seq-loop program: `Seq` occurs only in std sources (`std/prelude.ergo`, `std/queue/queue.ergo`), which are not programs. A new tracked fixture is outside this stage's files. The measurement adds the two Seq-loop programs of the lower tests as extra rows, outside the corpus totals: `capturedSeqProgram` 6140 bytes, 7 polls, 4 footprints; `methodValueProgram` 11827 bytes, 8 polls, 5 footprints. |
| s3c-r3-m1 | Measured: 0 extra polls in the corpus. The 112 clean programs have 30 `with` blocks of a wire (18 inline, 12 named). Their footprints walk or join 176 provider entries that the with does not acquire: 1 of an inline wire, 175 of named wires (the named builder frame joins every provider its wire binds). None of the 176 provider expressions holds a call, and only a call adds a capability, a task or an entry poll to a frame. So the over-approximation adds no poll in the corpus. |

## Commands

Run from the repository root at the measured head. The run was bounded as every ad hoc run of this unit: `systemd-run --user --scope -p MemoryMax=8G -p MemorySwapMax=0`, `GOMEMLIMIT=6GiB` and a private `GOCACHE`.

```sh
bash <unit>/accept/cost.sh
# which runs:
go test ./internal/sketch/lower -run '^TestRuntime199CostModule$' -count=1   # with RUNTIME199_COST_DIR=<unit>/cost-module
(cd <unit>/cost-module && go test -run '^$' -bench '^BenchmarkRuntime199(Emitted|Target)$' -benchmem -count=10) > <unit>/cost-raw.txt
benchstat <unit>/cost-raw.txt > <unit>/cost-summary.txt
# paired comparison:
benchstat -col .name -row /work <unit>/cost-raw.txt
# corpus (overlay adds the measurement test to the lower package):
go test -count=1 -v -overlay <overlay.json> -run '^TestS5bR1Corpus$' ./internal/sketch/lower
```
