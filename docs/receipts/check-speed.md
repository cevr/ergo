# Check speed: generated tables, GC target, checked std

## Controls and method

Measured on the Linux workbox, 2026-11-11. The controls are the same native
`ergo check` command over all 54 example entry folders, not a different
language or a reduced contract. Each target has three samples per binary,
interleaved by target and round. CPU is user + system time; RSS is the
process's maximum resident set. `/usr/bin/time` reports CPU to 10 ms.
GOMAXPROCS=1 for the measured command; nice 19, inside the run's 8 GiB
memory bound. No wall-time assertion is used.

The mean below is the mean of the per-target medians. Host load was
32.52 / 24.51 / 30.68 at the start and 26.95 / 25.36 / 30.45 at the end
(1 / 5 / 15 minute load averages). These are loaded-host observations,
not quiet-host latency guarantees. All 486 command invocations exited 0.

| Binary | Mean CPU (ms) | Targets at least 100 ms | Mean median RSS (KiB) | Largest RSS (KiB) |
| --- | ---: | ---: | ---: | ---: |
| Source base, 2765b02af | 183.5 | 51 / 54 | 34,311 | 42,140 |
| Generated tables, fa5a5c0bd | 101.1 | 32 / 54 | 26,778 | 35,524 |
| Tables + GOGC 400, d869f385b | 82.8 | 22 / 54 | 32,192 | 45,856 |

Raw samples: the wf10 run's `chk1/b/check-gogc.log`. Reproduction:
`chk1/check-cpu.sh OUT 3 base chk1 gogc`, with the three binaries in the
script's BIN folder. The run directory is not tracked, by owner rule.

The GC change respects an explicitly set GOGC. Its measured tradeoff
against the generated-table control is lower CPU with more peak memory.
The owner goal, below 100 ms CPU for each target, is not met by this
control: 22 target medians remain at least 100 ms.

## Checked std cache

The checked cache's correctness guard compares raw diagnostics, inspect
output, both lowered modes, and evaluator reports with the source path,
over every example entry and replayable generated callers. The source
and cached passes initialize std and check callers in the same order;
they do not normalize outputs. A warm disk-cache check's counted std
work is zero. A key mismatch, invalid snapshot or failed write falls back
to source checking.

The full cache CPU/RSS receipt is pending a quiet-host window. The run's
`chk1/b/bench-pending.md` names the exact command. No measured speed claim
for the committed cache head is made here before that receipt.

The source-only lowering has a separately captured ordering defect
(`chk1/b/nondet.md`, two source files and both lowered outputs). The cache
unit does not fix or sort away that defect.
