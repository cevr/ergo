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

The snapshot is segmented: a base (universe and prelude), then one
segment per std package, each after the packages it imports. A process
decodes the base at install and a package's segment, with the segments it
names, on the package's first import. So a check decodes the std its
imports reach and no other (TestStdDecodeGrowsWithImports counts it:
packages decoded equal the import closure). Round 1 decoded all of std at
install, which set a floor of about 280 M instructions on every check,
also on a program with no std import (wf10 audit 3, F3).

### Instructions, round 2 (measured, 2026-11-12)

`ergo check`, warm caches, user-space instructions of the process tree
(perf_event_open), three interleaved rounds, median per target,
GOMAXPROCS=1. Instruction counts do not depend on host load; load was
35.8 / 42.0 / 46.6 at the end. `gogc` is d869f385b (tables + GOGC 400, no
std cache), `r1` is the round-1 head e206dfeec (eager decode), `r2` is the
round-2 lazy decode. All 126 runs exited 0.

| Target | gogc M | r1 M | r2 M | r2 / gogc | RSS MB gogc / r1 / r2 |
| --- | ---: | ---: | ---: | ---: | --- |
| editions/sketch (no std import) | 13.5 | 290.5 | 12.6 | 0.93 | 13.8 / 38.5 / 21.1 |
| hello, Console only | 12.2 | 280.1 | 10.9 | 0.89 | 13.6 / 38.0 / 20.8 |
| lookup | 55.2 | 305.3 | 27.4 | 0.50 | 16.9 / 39.2 / 22.3 |
| deps | 567.3 | 441.9 | 285.2 | 0.50 | 41.4 / 49.3 / 35.1 |
| router | 544.3 | 416.8 | 269.2 | 0.49 | 42.6 / 47.1 / 34.7 |
| sudoku | 315.8 | 532.0 | 253.8 | 0.80 | 24.3 / 42.3 / 27.3 |
| regex | 292.4 | 381.6 | 161.6 | 0.55 | 32.2 / 44.4 / 31.4 |
| lru | 241.6 | 365.3 | 136.8 | 0.57 | 30.2 / 44.2 / 30.5 |
| stackvm | 182.3 | 345.4 | 112.5 | 0.62 | 26.2 / 43.5 / 28.7 |
| jsonv | 543.1 | 428.1 | 275.4 | 0.51 | 39.6 / 48.8 / 35.0 |
| calc | 434.8 | 429.3 | 231.8 | 0.53 | 32.5 / 47.9 / 32.6 |
| bookmarks | 615.6 | 370.7 | 246.9 | 0.40 | 44.6 / 42.3 / 35.0 |
| tidewalk-replay | 131.7 | 338.9 | 71.3 | 0.54 | 22.9 / 41.3 / 25.5 |
| csvreport | 433.8 | 359.3 | 168.8 | 0.39 | 33.3 / 42.8 / 31.9 |
| sum | 4,384 | 5,285 | 2,264 | 0.52 | |

The program without std imports keeps 7 MB more peak RSS than the no-cache
control (measured); the whole snapshot file (2.9 MB) is read into memory
and the binary is 2 MB larger (inferred causes, not profiled). Raw samples:
the wf10 run's `chk1/b/instr-2.log`; the command is `chk1/instr.sh`.

### Peak memory, CHK1c (measured, 2026-11-12)

CHK1c reads the snapshot by offset: the install reads the header and the
base, and a package's first import reads its segment
(`Work.StdSnapshotBytesRead`; TestStdDecodeGrowsWithImports holds it to
the header, the base and the import closure's segments). The process keeps
the open file, not the bytes.

Two more causes were measured with page and allocation profiles of a check
with no std import:

- The cache key hashed the 2.3 MB of embedded std sources in every
  process (3 MB of peak RSS, 4 M instructions). The key is now the
  compiler's identity alone: that identity is the digest of the compiler's
  bytes, which embed the std sources, so a std edit still changes the key.
- The rest was pages of the binary, not heap: the heap of the cached check
  is smaller than the source check's (3.8 MB against 4.0 MB anonymous
  RSS). The decoder's code is about 45 pages. More pages came from stack
  growth inside the decoder's recursion: each copy of a growing stack reads
  the stack maps of every frame on it. `InstallStd` now grows the stack once,
  while few frames are on it (`growStack`): 192 fewer resident pages of the
  binary (3922 to 3730). TestInstallGrowsTheStackOnce counts the stack
  moves of a first check after the install: zero.

Method: as above, seven interleaved rounds, median per target, peak RSS is
the child's `ru_maxrss`. `nocache` is 0142c37cb with the std cache off
(cmd/ergo does not call UseStdCache), `base` is 0142c37cb (CHK1b r2), and
`head` is CHK1c. Load was 36 to 48 on 16 cores. All 294 runs exited 0.

| Target | nocache M | base M | head M | head / nocache | RSS MB nocache / base / head |
| --- | ---: | ---: | ---: | ---: | --- |
| editions/sketch (no std import) | 15.6 | 14.0 | 9.1 | 0.58 | 18.5 / 25.0 / 18.4 |
| hello, Console only | 14.2 | 12.1 | 7.7 | 0.54 | 18.3 / 24.8 / 18.3 |
| lookup | 67.4 | 35.1 | 30.3 | 0.45 | 21.8 / 26.4 / 20.0 |
| deps | 633.7 | 311.8 | 318.7 | 0.50 | 47.5 / 38.5 / 37.4 |
| router | 696.4 | 311.1 | 334.0 | 0.48 | 49.3 / 40.7 / 39.4 |
| sudoku | 334.3 | 261.2 | 255.8 | 0.77 | 29.1 / 31.7 / 25.3 |
| regex | 503.4 | 366.4 | 372.7 | 0.74 | 38.9 / 40.2 / 36.6 |
| lru | 296.1 | 175.5 | 135.8 | 0.46 | 34.7 / 35.2 / 28.7 |
| stackvm | 206.8 | 139.2 | 119.8 | 0.58 | 31.0 / 33.2 / 26.6 |
| jsonv | 692.4 | 352.7 | 373.8 | 0.54 | 48.6 / 39.7 / 38.1 |
| calc | 487.5 | 277.7 | 269.9 | 0.55 | 38.0 / 36.1 / 35.0 |
| bookmarks | 766.2 | 286.3 | 307.8 | 0.40 | 54.4 / 38.1 / 36.5 |
| tidewalk-replay | 156.3 | 86.4 | 81.3 | 0.52 | 27.9 / 30.4 / 23.8 |
| csvreport | 493.4 | 234.9 | 163.5 | 0.33 | 38.4 / 37.8 / 31.1 |
| sum | 5,364 | 2,864 | 2,780 | 0.52 | |

Peak RSS of the head is at or below the no-cache control on every target.
On the larger targets the instruction counts vary by up to about 7%
between builds and runs (the collector runs concurrently); a probe with
three builds of the same change measured router 313 to 325 M and jsonv
347 to 370 M. Raw samples: the wf10 run's `chk1/CHK1c/receipt-1.log`.

The CPU-time receipt over all 54 targets for the final head stays pending
a quiet-host window (`chk1/b/bench-pending.md`); CPU time depends on load,
instruction counts do not.

The source-only lowering has a separately captured ordering defect
(`chk1/b/nondet.md`, two source files and both lowered outputs). The cache
unit does not fix or sort away that defect.
