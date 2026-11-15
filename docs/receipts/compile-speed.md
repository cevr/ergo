# Compile speed receipts

## Package split

Compile-speed unit CS4: a program of several Ergo packages lowers to one Go package per Ergo package, plus one shared package below them (`internal/sketch/lower/pkgsplit.go`). `ERGO_LOWER=single` selects the reference, the one-package module. Measured on 2026-11-13 at the CS4 head, on the 16-core workbox, with a 1-minute load of 38 to 60. The reference is the base's lowering: `lower.Lower` is unchanged at the CS4 head, so the reference columns are base against head. The load is high, so the wall times are not a speed verdict. The counts of compile actions do not depend on the load.

### What the go command compiles (measured, `go build -x`)

The `calc` example has 7 Ergo packages. Each mode has its own fresh `GOCACHE`, warmed by a one-line program (so the Go standard library and the Ergo runtime are compiled). Each row is one `ergo build ./cmd/calc` with `GOFLAGS=-p=4`.

| Step | Reference: packages compiled | Split: packages compiled | Reference wall / user / sys (s) | Split wall / user / sys (s) |
| --- | --- | --- | --- | --- |
| First build | `main` (the whole program) | shared, ast, eval, syntax, typing, opt, repl, main | 3.72 / 1.93 / 0.42 | 3.13 / 1.78 / 0.61 |
| Edit a string literal in `ast` (the lowest package) | `main` | ast and every package that imports it: eval, typing, syntax, opt, repl, main | 2.60 / 1.81 / 0.53 | 2.82 / 1.88 / 0.54 |
| Edit an integer literal in `opt` | `main` | opt, main | 2.80 / 1.86 / 0.47 | 2.15 / 1.31 / 0.42 |

- Measured: the go command compiles an edited package and every package that imports it, and no other package. It keys each import by the content ID of the import's whole archive (`cmd/go/internal/work/exec.go`, `buildActionID`: `import <path> <contentID>`), not by its export data. So a body edit that keeps the export data still compiles the importers again. `TestBodyEditCompilesOnePackage` checks this rule: after a body edit of the lowest package `l0`, the compiled packages are exactly `l0`, `mid` and the root. It does not check plan §3.2's "1 Go compile". One compile per body edit needs a driver that keys a package on its imports' export data (`compile -linkobj`), which is the direct path (BUILD-direct, CS10). This base does not have it.
- Measured: `TestColdCompilesInParallel` blocks each leaf compile in a `-toolexec` wrapper until all three leaves are in flight. The go command then has the three leaves in flight at the same time.
- Measured, not controlled: `go run ./scripts/examples` with all 239 cases passed under both lowerings. The split run took 7 min 4 s, and the reference run took 13 min 36 s, each with an empty emission cache and the same warm Go cache, at a load of 40 to 60. Load differences can explain this gap. It is not a claim.

### Run time (measured, `calc -fold` on 200,000 lines)

The same input goes to both binaries in 5 alternating runs. The outputs are byte-equal. CPU seconds are user / sys:

| Run | Reference | Split |
| --- | --- | --- |
| 1 | 2.03 / 0.70 | 1.78 / 0.68 |
| 2 | 1.95 / 0.60 | 1.74 / 0.65 |
| 3 | 1.76 / 0.72 | 1.71 / 0.59 |
| 4 | 1.75 / 0.54 | 1.96 / 0.67 |
| 5 | 1.99 / 0.66 | 1.78 / 0.60 |

The mean user time is 1.90 s for the reference and 1.79 s for the split. The runs show no loss at this load (measured). The binaries are 6,025,020 and 6,033,435 bytes.

- The corpus (measured): the checker's corpus has 4,980 candidates, and 768 of them check clean and lower. `Split` left all 768 Go texts byte-equal (0 changed), so their builds and runs at head are the base's. The check was a scratch test at the CS4 head over `corpusDomain`, not committed. The conformance programs are also one Ergo package each, and the same rule covers them (inferred from `splitMain`: with one user package, no group leaves the root). They were not run separately.
- Cross-package inlining: the Ergo inliner still sees the whole program, so the split does not change which bodies Ergo inlines. Go's inliner inlines across packages through export data. A function that Go inlined inside one package and does not inline across packages would show here as a loss; none showed.

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
