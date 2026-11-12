# Bench: a performance referee for Ergo

Status: **design record** (STROBE BN0, 2026-11-12). The statistics are **built**: std `bench`, `internal/sketch/std/bench/stats.ergo` (BN1; [stdlib.md, Bench statistics](../spec/stdlib.md#bench-statistics)). The referee machine, the host meter, the `bench` declaration, the `ergo bench` command, the report, the gate step and the examples are not built yet ([Not built yet](#not-built-yet)). The terms are defined in [GLOSSARY.md](../../GLOSSARY.md#tooling-and-testing) and nowhere else.

Owner, 2026-11-12: "What I want is also something like hona/strobe for ergo - if we could either add support for strobe or just copy it". Ergo copies strobe's method and rules, not its code: strobe (MIT, release 0.32.0) is 127,332 lines of Rust for browsers, native windows, phones, profilers and a daemon; about 4,800 of them are the part Ergo needs (measured: `stats.rs` 856, `validity.rs` 874, `host/spawn.rs` 1,145, `app/report.rs` 740, `app/card.rs` 1,146).

## Purpose

A speed claim in Ergo is a verdict with its interval, from paired blocks of two sides, or a refusal with its reason. It is never a ratio of two medians with no interval, never a threshold read from one run, and never a failed run counted as a fast one. The receipts Ergo has today each copy a counter and a method, and none has an interval or a refusal (measured: four copies of the `perf_event_open` call; a `/usr/bin/time` script whose `|| true` counts a failed `ergo check` as fast).

## Method

1. **A bench block** is one fresh process of one side at one bench size. The value of a block for a counter is its slope: the counter at the large size less the counter at the small size, over the difference of the sizes. So a fixed cost (start, parse, the runtime's init) falls out, and the value is the cost of one bench unit (one lookup, one row).
2. **Pairs.** Blocks run in pairs, one of each side. The order alternates, base then head, then head then base. One block of each side runs first and is discarded (warm caches, a page cache, a CPU frequency step). A pair's difference is `ln(head) - ln(base)` when every value of the series is above 0 (the log scale; a subtraction, so swapping the sides negates it exactly), else `head - base` (the difference scale).
3. **The test** is Wilcoxon's signed-rank test of the differences. Zeros are dropped; tied sizes share their mid-rank; the null distribution is that of those ranks over every sign assignment, built exactly from the doubled ranks as chances (each rank halves the chances it adds to). strobe's own history shows why: the untied distribution with a rounded statistic gave p 0.039 for `d = [1, 0, 1, 3, 1, 0, 1, 1, -1, 2]`, whose exact p is 14/256 = 0.055.
4. **The estimate** is the Hodges-Lehmann estimate, the median of the Walsh averages `(d[i] + d[j]) / 2` for `i <= j`. **The interval** is `[W(c), W(m-1-c)]` of the m sorted averages, with c the largest value where `P(T <= c) <= (1 - level) / 2` for the signed-rank statistic T of n untied ranks. It covers `1 - 2 P(T <= c)` (96.9% at 6 pairs, 95%). When no c reaches the level, the interval is `[min, max]` and covers `1 - 2^(1-n)`; a verdict needs an interval whose coverage reaches its level.
5. **Looks.** A planned series judges after 8, 12, 18, 26 and 40 pairs, each at 98.42%: Pocock's boundary 2.413 for five looks at 5% overall. A fixed series (`--pairs N`) judges once, at 95%. A primary decided at a look keeps that look's verdict.
6. **Limits.** Fewer than 6 pairs never decide (`FewPairs`). A series holds at most 400 pairs (`TooManyPairs`), so every distribution is exact and no code path uses a normal approximation.
7. **Claims.** Every counter is better lower. A *change* claim with bound b: `Better` when the interval lies below 0, `Worse` above 0, `Same` when it lies inside `(-ln(1 + b), ln(1 + b))` on the log scale (or inside `±b × max(|median base|, |median head|)` on the difference scale), or is exactly `[0, 0]`; else `Unclear`. A *no-worse* claim with margin m (one-sided 2.5% per look, as strobe's): `NoWorse` when the interval lies below the margin, `Worse` when it lies above it, else `Unclear`. A bound or a margin below 0 is 0.
8. **Exact counters** (kind `Exact`) are compared exactly when each side held one value in every block of at least 6 pairs: the one difference is the answer, with coverage 1. One that moved is refused (`CounterUnstable`).
9. **Secondaries** get a verdict only after the primary has one, with Holm's step-down at 5% over the secondaries actually tested (a refused or an exact one is not in the family).
10. **Pairs needed.** An undecided series projects the pairs it needs for an 80% chance to decide: `((z(level) + z(0.8)) σ / dist)² / 0.955`, σ a robust spread (1.4826 times the median absolute deviation, or the mean absolute deviation times √(π/2) when over half the differences are one value), dist the distance from the differences' median to 0 or the bound, at least the pairs the level needs. The referee stops a planned series as `Underpowered` when even the likely case (a 50% chance) needs more pairs than its last look.

The three changes from strobe, each a law of std `bench`:

- **A symmetric bound.** strobe sets ±2% on the ratio, so `ln 0.98` and `ln 1.02` differ, and a mirrored family read `same` on one side and `unclear` on the other in 14 of 500 simulated families (strobe SPEC §9.3, "Mirror"). Ergo sets the bound in log space, so swapping the sides flips `Better` and `Worse` and keeps every other verdict, its p-value and its pairs needed (law `Mirror`).
- **No normal approximation.** strobe switches to a normal tail above 400 pairs; Ergo refuses more than 400.
- **An exact counter that moves is refused**, where strobe warns and compares it like a timed one: a `hold` claim on a counter that is not exact means nothing.

## Verdicts and refusals

`Verdict = Better | Worse | Same | NoWorse | Unclear | Underpowered | Refused`, a closed union that the report, the gate and the card match exhaustively. Every variant but `Refused` carries its `Difference` (pairs, scale, estimate, interval, coverage, p-value, exactness); `Unclear` adds the pairs needed when a projection exists, and `Underpowered` the pairs needed. `Refused` carries a `Refusal` and no number, so a refusal cannot turn into a number on its way to a report.

| Refusal | When |
| --- | --- |
| `NoCounter` | The host has no counter of the kind (Darwin has no instruction counter; a gate there skips the step with this reason). |
| `Multiplexed` | The kernel counted the event for part of the run only (its running time is less than its enabled time). |
| `RunIncomplete` | A block exited nonzero, or left a process running in its group. |
| `NotEquivalent` | The sides printed different sums: they did not do the same work. |
| `CounterUnstable` | An exact counter took more than one value on a side. |
| `NoMetric` | A declared counter that a block did not report. A run that printed none is not 0. |
| `FewPairs` | Fewer than 6 pairs. |
| `TooManyPairs` | More than 400 pairs in one series. |
| `NotFinite` | A block value that is NaN or infinite. |
| `SharedRunner` | A timed counter judged in a CI job in a shared virtual machine. |

## Counters

The kind of a counter is a type. An exact counter may stand in a `hold` clause and in a law; a counted or a timed counter only in a `primary` or a `no worse` claim, judged over pairs. Holding a counted or timed counter exactly is a diagnostic at check time. The gate judges exact and counted claims and never a timed one (AGENTS.md: a guard counts work, it never times).

| Counter | Kind | Source |
| --- | --- | --- |
| `Copied`: elements copied by a value-semantics append, concat or update | exact | the lowering's counter in bench and test builds |
| `ChecksLeft` (bounds, nil, divide, shift), `BoxSites`, `DictLoads`, `TextBytes` | exact, one value per build | the build report |
| `Allocs`, `AllocBytes`, `GCCycles` | exact when `procs 1`; the referee checks that they hold | an exit hook over the runtime's statistics, in bench builds only |
| a program's own count (`bench.Add("frames", 1)`) | exact | the program, in bench builds only |
| `Instructions`, `Branches` | counted | `perf_event_open`, user mode, the child's whole tree, events enabled at exec |
| `GCShare` | counted, derived | (the run less the run with `GOGC=off`) over the run |
| `CPU`, `Wall`, `PeakRSS` | timed | `wait4`'s rusage; the program's own `VmHWM` at exit |

## The `bench` declaration

Decided (Q2): a `bench` declaration stands beside `test` and `law`, and every build but a bench build erases it. It writes every clause; nothing has a default.

```ergo
// examples/lru/cache/bench.ergo
import bench

// Lookup does n lookups on a full cache. The referee runs it at both
// sizes; the cost of one lookup is the difference over the difference of
// the sizes.
bench Lookup(n int) {
	sizes   200_000, 2_200_000
	procs   1
	primary bench.Instructions
	hold    bench.Allocs == 0
	control go "../cases/hand" no worse 0%
	run {
		let mut c = Filled(1024, Keys(n))
		for k in Keys(n) { let _ = c.Get(k) }
		bench.Sum(c.Len())
	}
}
```

| Clause | Meaning |
| --- | --- |
| `bench Name(n int)` | The case and its size parameter, one `int`. |
| `sizes a, b` | The small and the large size, `0 < a < b`. |
| `procs k` | `GOMAXPROCS` of the harness. |
| `primary C` | The counter the series is judged on. |
| `hold C == v`, `hold C <= v` | An exact claim, checked at the small size. |
| `control go "PATH" no worse M%` | A hand Go package as a side of its own, with a no-worse claim. |
| `run { … }` | The work. `bench.Sum(v)` prints the sum both sides must agree on (`NotEquivalent`). |

Diagnostics (their codes are BN4a's): a missing clause; an unknown counter; `hold` on a counted or a timed counter ("Instructions is a counted counter: hold takes an exact counter; judge it with primary or no worse"); a control path that is not a Go package; a `bench` with no size parameter or a size parameter that is not an `int`; sizes not in order.

Growth claims are laws over exact counters, run in process by `ergo test` with no Go build and no clock:

```ergo
law AppendCopiesLinear(n int) where 1 <= n && n <= 64 {
	bench.Count(Append, bench.Copied, 2*n) <= 2*bench.Count(Append, bench.Copied, n) + 16
}
```

## Sides and modes

A side is a commit (`--base REF`), a compiler (`--base-ergo PATH`: one source, two compilers), a lowering (`--against lowering=reference`, the owner's 2026-11-08 rule that the old lowering stays selectable), a hand Go control (`control`), or a command (`ergo bench run --base "<A>" --head "<B>"`). Modes: `--check` runs each case once per side at its small size and judges exact claims only; `--counted` (the gate's mode) judges exact claims once and counted claims over pairs up to the planned looks, and runs no timed counter; `--aa` runs one side as both; `--pairs N` is one fixed look; `--simulate` runs the referee on a scripted meter. One bench runs at a time (a lock file). The referee pins each child to one CPU of the mask it was given (`ERGO_BENCH_CPUS`, else its own affinity), runs both sides at one priority and records it, and never changes another process.

## The receipt

`bench.v1` is one JSON object per run: the cases, and per series its counter, kind, claim, design, `verdict_kind`, the refusal id or the difference (pairs, scale, estimate, interval or `exact`, coverage, p-value, pairs needed), the builds of both sides, the host, its load and the priority. A Markdown block between `<!-- ergo:bench <id> v1 begin sha256:… -->` and its end line holds the rendered table; `ergo bench report --update FILE` refuses to replace a block whose digest a hand edit broke. Each run is appended to a local ledger, so a receipt says which experiment of how many it is. Nothing published names another process or a home path.

## Left out of strobe

Web, Electron, native-window, webview and phone targets and their drivers; frame, input-latency, GPU, energy and network metrics; profilers, attribution, hotspots, heap snapshots, leak runs and Perfetto; the daemon, the MCP server and the skill installer; video; `track`, `history` and `bisect`; the power plan; Windows; the PNG card (the card is SVG). Reason: no Ergo program needs them today; the corpus wave's UI receipts are counts per frame on a scripted host, which exact counters hold.

strobe's CPU reservation (`--reserve-cpus`, which moves the threads of the user's other processes off the target's CPUs) is left out on purpose: on the workbox those processes are the other lanes and the gates, and a gate computes its CPU budget from its affinity mask. On a loaded host a timed interval widens and the verdict is `Underpowered`; counted counters stay tight.

strobe's rule "No automated tests. Ever." is not copied: the referee is held by laws and properties (std `bench`'s laws; a model-based test of the referee machine in BN2).

## Decisions

The owner's 2026-11-12 rule "decide, don't ask" applies; each decision names its reason and its reversal.

- **Q1.** Decided: copy the method into a native `ergo bench` written in Ergo; use strobe as it is, from the run directory and with no product code, for the timed receipts that wait today; an emitter of strobe bench files waits until a program needs frame or input-latency numbers. By the tiebreaks *A small regular language with deep libraries* and *Laws beat API resemblance*, and the owner's rule that a port leaves nothing to TypeScript. Reverse by: building the emitter (`ergo bench --emit strobe`) and retiring the native referee.
- **Q2.** Decided: a `bench` declaration (above). By *Syntax for compiler-known constructs beats special-cased library items*: the checker must know a counter's kind to refuse `hold Instructions`. Reverse by: a library registration read by std `bench`, which loses the check-time refusal.
- **Q3.** Decided: the card belongs to the batch (Ergo has no CI and no pull requests): batch accept runs `ergo bench --check --base origin/main` and writes the block and the card; no workflow. By *subtract before you add*. Reverse by: a workflow that runs `--check` on push.
- **Q4.** Decided: no frame or latency numbers now. Reverse by: the strobe emitter, with a journey form designed first.
- **Q5.** Decided: the one `perf_event_open` call lives in one reviewed `runtime/v1` body, admitted by name, with its invariant (the kernel's 64-byte first `perf_event_attr`) and a test that fails if the size changes; the unsafe rule's "benchmark that shows a gain" clause does not apply to a system call. By the unsafe rule's "only in reviewed `runtime/v1` bodies" and `go.mod` having no dependency. Reverse by: `golang.org/x/sys` as the first dependency.
- **Q6.** Decided: a timed claim is a paired verdict with its interval, taken at any load; `Unclear` and `Underpowered` are no claim; an absolute target ("under 1.000 s alone on 16 cores") stays a quiet-host item run by hand. By *Measured implementation evidence beats familiar API names* and pillar 7 ("claims need measured evidence"): a paired interval is evidence at any load, a quiet window that never comes is none. Reverse by: keeping the quiet-window rule and judging only counts.
- **Q7.** Decided: no CPU reservation; the referee pins its child inside the mask it is given. Reverse by: a root-delegated `cpuset` with CPUs kept for the referee.
- **Q8.** Decided: no contact with strobe's authors; nothing here depends on a change in strobe.
- **Q9.** Decided: the gate's step fails on `Worse`, on a refusal (but `NoCounter` on a host with no counter, which skips with its reason) and on `Unclear` after the last look: a claim on the margin is a finding, and the unit states a margin it can hold. By *Contract guardrails beat zero-cost slogans*. Reverse by: `Unclear` warns and passes.
- Decided (BN1): every counter is better lower. By *subtract before you add*: every counter of the catalogue is a cost. Reverse by: a direction field on `Change` and `NoWorseThan`.
- Decided (BN1): the bound on the difference scale is b times the larger of the two sides' medians, where strobe takes base's: the same both ways, so `Mirror` holds there too. Reverse by: base's median, and `Mirror` restricted to the log scale.
- Decided (BN1): a judged series with margin 0 whose interval is `[0, 0]` is `Unclear`, not `NoWorse` (the interval does not lie below the margin), as strobe's; only an exact counter says `NoWorse` at margin 0.

## Not built yet

| Unit | What |
| --- | --- |
| BN2 | The referee machine: discard, pair, look, decide, run on for the secondaries, stop; a `Meter` operation type with laws and a scripted provider. |
| BN3 | The host meter: the child in its own process group, pinned, under the memory bound, counted with events enabled at exec, `Multiplexed` from the read times. |
| BN4a, BN4b | The `bench` declaration, its diagnostics and erasure; the `ergo bench` command, its harness, sides, modes, ledger and lock. |
| BN5 | The text table, the `bench.v1` receipt, the delimited block with its digest, the SVG card. |
| BN6 to BN9 | The gate step, the receipt tools, the batch card and the examples' bench files. |

Until BN4b, a speed claim keeps today's rule (a quiet-host command in the lane's `bench-pending.md`).

## Prior art

[PRIOR_ART.md, Benchmark referees](../../PRIOR_ART.md#benchmark-referees-2026-11-12) holds the comparison: strobe, rustc-perf, Go's `testing.B` and `benchstat`, MoonBit's `moon bench`, Benchee, `core_bench`, Criterion.rs, iai-callgrind and hyperfine. No compile-to-host language of PRIOR_ART.md lowers a benchmark construct in a special way: MoonBit and Go compile benchmarks into the test driver; Ergo compiles them into one harness binary per side and erases them everywhere else.
