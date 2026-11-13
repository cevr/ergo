# Compile speed: the CS0 baseline

The baseline of the compile-speed work (owner, 2026-11-13: a cold build
under a second, a warm one within a millisecond). Every number is a count:
the compiler's stage counters (`ERGO_STAGE_REPORT`, [tooling](../tooling.md#stage-counters))
and the user-mode instructions of the whole process tree (`perf_event_open`
with inherit). Wall times are context only: the host is shared.

## Method

`go run ./scripts/buildcost` and `go run ./scripts/buildcost empty`, at the
merge of g-b2 into the batch-18 line (BUILD-direct's action store, the
checked std snapshot, the test result cache), 2026-11-13. Linux/amd64, Go
1.27.0, 16 cores shared, load average 20; each `ergo` run at GOMAXPROCS=1,
nice 19, inside an 8 GiB memory scope. Go's std archives are warm in
GOCACHE (cold is post-download; std compiles are CS3's), the Ergo cache
starts empty for each program, and each scenario follows the one above it.
Programs: `gN` is the seeded generator's program of N packages plus
`cmd/app` (`internal/testinputs/genprogram`, seed 1, up to 24 function
groups per package); `deps` is `examples/deps`.

## Scenarios (M instr; counters)

| Scenario | g10 | g50 | deps |
| --- | ---: | ---: | ---: |
| First build (empty Ergo cache, std check from source) | 18,777 | 24,987 | 21,454 |
| Warm, same output | 498 | 2,001 | 380 |
| Warm, new output | 482 | 2,105 | 350 |
| Comment edit | 1,312 | 4,617 | 1,250 |
| Body edit in the lowest package | 6,409 | 13,197 | 1,269 (dead code, no compile) |
| Signature edit (an unused function added) | 1,361 | 4,571 | 1,158 |
| Body edit in main | 6,138 | 13,189 | 9,854 |
| `ergo check`, twice | 472; 470 | 2,346; 2,306 | 304; 325 |
| `ergo test` of the lowest package, first; again | 3,990; 76 | 4,020; 77 | 5,953; 80 |

What the counters say of each warm build (g10; g50 and deps alike):

- **A warm `ergo build` still does the front end:** 22 files read, 21
  parsed, 11 packages checked (std decoded from the snapshot: 9 packages,
  0 std files parsed), 3.0 MB hashed (the emission key hashes std's
  sources and the module verifies its outputs), 0 lowered, 4 compiles and
  the link reused from the action store, 0 processes, and **5.9 MB
  written**: the output is a copy of the store's executable. CS2 removes
  the parse, check and key work and the copy.
- **A comment edit lowers again** (1 module, 534 KB of Go) and then finds
  every Go action reused: the emitted Go did not change. CS2 cuts the
  check; CS5 the lowering.
- **A body edit anywhere compiles the whole program again**: one Go
  package per program, 1 compile and 1 link (CS4 splits the package).
- **A repeated `ergo test` is 76 M instr**: the cached verdict (g-b2),
  with 0 processes; most of it is the front end and 1.8 MB hashed.
- **A warm `ergo check` re-checks everything**: 470 M (g10), 2,306 M (g50).

## Empty-package compile

Go's `compile` of `package p` with an empty import config, the fixed cost
of one compile process: **9.4 to 9.6 M instr** (three runs), 7 to 35 ms
wall on this host. Against an edit build of g10 (6,409 M) that is 0.15 %
per package: a compile process per Ergo package (CS4) costs about 1 % for
a program of ten packages, far below the 5 % that would call for a compile
server (CS10b).

## Thin client

The start of a process that does no work, user-mode instructions:

| Process | M instr | Wall (context) |
| --- | ---: | ---: |
| `/bin/true` | 0.16 | 1 to 4 ms |
| A Go program with an empty main | 0.27 to 0.34 | 2 to 4 ms |
| `ergo` with no arguments (usage, the thin client) | 3.1 | 7 to 20 ms |

The `ergo` start is 3.1 M instr, about the whole 3.3 M budget of a 1 ms
warm answer (plan §3.2), before any input is read: a quiet-host
millisecond for a warm `ergo build` typed in a shell needs a thinner start
(the binary's init work), or the request inside `ergo serve` (CS9). The
host was at load 20; the quiet-host wall time is not measured here.

## R

R, the private reference server of 11 packages (42,771 lowered Go lines),
lies between g10 and g50 in source size; its numbers are inferred by
interpolation (warm build about 1.2 G instr, warm check about 1.4 G) and
are not measured in this repository.

## Link

CS7: `ergo build`, `ergo run` and `ergo test` link with `-s -w` unless
`--debug` is given. Measured on the go1.27.0 linker of the emitted module
of eight examples (`ergo build -work --debug`, then the `link` line of
`go build -work -x`, run again under pstat with and without `-s -w`),
user-mode instructions of the whole process tree, median of 3 runs, host at
load 90 to 113, GOMAXPROCS=2:

| Example | Link M instr, full | Link M instr, `-s -w` | Bytes, full | Bytes, `-s -w` |
| --- | ---: | ---: | ---: | ---: |
| calc | 1,604 | 959 | 6,154,737 | 4,182,279 |
| deps | 1,668 | 980 | 6,360,187 | 4,321,543 |
| kvstore | 1,639 | 977 | 6,266,477 | 4,247,815 |
| chat | 1,652 | 964 | 6,151,686 | 4,165,895 |
| forge | 1,636 | 966 | 6,185,971 | 4,202,759 |
| deploys | 1,638 | 956 | 6,077,906 | 4,116,743 |
| mdhtml | 1,642 | 964 | 6,194,676 | 4,206,855 |
| relay | 1,598 | 923 | 5,817,412 | 3,928,327 |

A stripped link does about 41 % fewer instructions (0.64 to 0.69 G fewer)
and writes about 32 % fewer bytes (about 2 MB). The pclntab stays, so
tracebacks and defect reports are unchanged.

Spike, not adopted: `-gcflags='-N -l'` (no optimization, no inlining) on
the emitted module's own packages (std cached; a nonce `-trimpath` makes
each run compile them again), median of 3: deps 8,904 to 6,879 M instr
(-23 %), kvstore 6,209 to 5,133 M (-17 %), the binary 3 % larger. It trades
the program's run-time speed for compile time, which the owner's rule
(correctness and speed) does not allow as a default; a flag for it is a
separate decision. `-linkshared` was not measured: it needs std installed
with `-buildmode=shared` into GOROOT, and its executable needs that shared
std at run time, so it cannot be the default of a self-contained build.

Script: the wf10 run's `compile-speed/CS7` receipt script (`receipt.sh`,
pstat from the v1 audit's speed tools); the run directory is not tracked.

## Reproduce

```
go build -o /tmp/ergo ./cmd/ergo
go run ./scripts/buildcost -ergo /tmp/ergo -sizes 10,50 -examples deps
go run ./scripts/buildcost empty -ergo /tmp/ergo
```

The raw log is the wf10 run's `compile-speed/receipts/cs0-buildcost-bd0862c14.log`
(the run directory is not tracked, by owner rule).
