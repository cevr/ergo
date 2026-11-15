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

## Process start

`go run ./scripts/buildcost start` counts the thin client, `ergo version`,
the warm `ergo check` and `ergo build` of a module of four inputs, and a
probe binary per package of the module and per heavy std package. Each
probe imports only that package and has an empty `main`. Each line is the
least of five runs, at load 26, with the warm answer (CS2) in place.

| Process | Before (M instr) | After (M instr) |
| --- | ---: | ---: |
| A Go program with an empty main | 0.30 | 0.30 |
| `ergo` with no arguments (usage, the thin client) | 3.06 | 2.09 |
| `ergo version` | 2.99 | 2.10 |
| warm `ergo check` (4 inputs, every work counter 0) | 3.87 | 2.92 |
| warm `ergo build` (4 inputs, every work counter 0) | 3.89 | 2.99 |

The module's package inits allocated 1.13 MB in 2,517 allocations before
the change, and 132 KB in 896 after. Three changes made the difference:

- `lower`'s `goMod` was a package-level value that read and copied every
  runtime source (700 KB through `runtime.Files`) at start. Only tests used
  it, so it now lives in a test file.
- `runtime/v1`'s generated `bodyTable` was a map of 490 rows, built at
  start. It is now a slice sorted by name, which Go lays out at build time,
  and `BodyOf` searches it.
- `lower`'s `runtimeBindings`, a map of 490 std Go bindings, is now built
  on first use with `sync.OnceValue`, by lowering only.

The whole warm request is now under the plan's 3.3 M (1 ms) budget, start
included.

What remains is not the module's own init, by the probes:
- `net/http` costs 0.82 M over an empty main, 0.66 M of it `crypto/x509`;
  `internal/toolchain` and `runtime/v1` link it.
- `go/types` costs 0.14 M.
- The rest, about 0.5 M, is the Go runtime's start work for a 30 MB binary
  against an 8 MB one: the probe that links every package of the module
  with an empty `main` is 1.59 M.

A probe undercounts a package's own tables: the linker drops the init of
a package-level map that nothing reaches, and a probe reaches nothing.
`GODEBUG=inittrace=1 ergo version` shows the inits as `ergo` runs them.
`TestStartBuildsNoTables` bounds the module's init bytes at 64 KiB.

## Incremental check (CS5a)

`go run ./scripts/buildcost reuse -sizes 10,50 -examples
deps,bookmarks,backups,adventure` checks each program in process, applies the
edits below one after another, and checks again after each. For each edit it
counts the packages that the decision of [Interface
fingerprint](../spec/build.md#interface-fingerprint) would check again. Today
every package is still checked; this counts what the cutoff would skip. The
edits are:

- a comment appended to a file of the lowest package;
- a body edit there: one more on the first integer literal, or one more
  letter in the first string, in a function that is not generic;
- an exported function added there;
- an unexported function added to main.

The table gives the packages checked again, out of all the program's packages.

| Program | Packages | Comment | Body | Export added | Main decl added |
| --- | ---: | ---: | ---: | ---: | ---: |
| g10 | 11 | 0 | 1 | 4 | 1 |
| g50 | 44 | 0 | 1 | 21 | 1 |
| deps | 6 | 0 | none* | 5 | 1 |
| bookmarks | 4 | 0 | 1 | 2 | 1 |
| backups | 5 | 0 | 1 | 5 | 1 |
| adventure | 3 | 0 | 1 | 2 | 1 |

\* Every function of deps' lowest package (`graph`) is generic, and a
generic body is part of the interface, so the tool makes no body edit.

- A comment edit checks nothing again.
- A body edit checks again only its own package.
- An added export checks again every package above it. The interface
  fingerprint includes the imports' interfaces (Interface fingerprint, 3);
  stopping at a middle package whose view did not change waits on CS5b.

The raw log is the wf10 run's `compile-speed/receipts/reuse-cs5a.log`.

## R

R, the private reference server of 11 packages (42,771 lowered Go lines),
lies between g10 and g50 in source size; its numbers are inferred by
interpolation (warm build about 1.2 G instr, warm check about 1.4 G) and
are not measured in this repository.

## Reproduce

```
go build -o /tmp/ergo ./cmd/ergo
go run ./scripts/buildcost -ergo /tmp/ergo -sizes 10,50 -examples deps
go run ./scripts/buildcost empty -ergo /tmp/ergo
go run ./scripts/buildcost start -ergo /tmp/ergo
```

The raw log is the wf10 run's `compile-speed/receipts/cs0-buildcost-bd0862c14.log`
(the run directory is not tracked, by owner rule).
