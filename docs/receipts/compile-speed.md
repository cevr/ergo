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

## Pin archives (CS3)

`go run ./scripts/buildcost pins -sets DIR -sizes 10 -examples deps`,
2026-11-13, at the CS3 head, linux/amd64, load 28, with the sets of
`go run ./scripts/pinsets -o DIR` served from a file URL. Cold is
post-download: the Go release, the patched compile and the sets on disk;
the Ergo cache's store and GOCACHE empty
([build](../spec/build.md#pin-archives)).

| g10 | M instr | std_exports | go_compiles | processes |
| --- | ---: | ---: | ---: | ---: |
| Cold, no sets | 169,266 | 203 | 4 | 7 |
| Cold, sets installed | 9,575 | 0 | 1 (main) | 4 |
| First build with a URL (fetch, verify, install, then the cold build) | 19,112 | 0 | 1 | 4 |

- **The sets remove 160 G instr of a cold build**: the 203 standard
  archives (`go list -export`, every std package the program links) and
  the 3 runtime packages compile no more; what is left is the front end,
  the program's own compile and the link.
- The deps rows (`compile-speed/cs3/receipt-pins.log`) agree on the
  counters (std_exports 203 → 0, compiles 4 → 1) but their no-sets run
  found g10's std in a reused GOCACHE, so its 22,098 M is not cold.
- The sets are 48 MB (std, gzip), 2 MB (runtime) and 18 MB (tools) per
  target.

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
go run ./scripts/pinsets -o /tmp/pins
go run ./scripts/buildcost pins -ergo /tmp/ergo -sets /tmp/pins -sizes 10
```

The raw log is the wf10 run's `compile-speed/receipts/cs0-buildcost-bd0862c14.log`
(the run directory is not tracked, by owner rule).

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

- The corpus at CS4 (measured; superseded by CS4b below): the checker's corpus has 4,980 candidates, and 768 of them check clean and lower. `Split` left all 768 Go texts byte-equal (0 changed), so their builds and runs at head are the base's. The check was a scratch test at the CS4 head over `corpusDomain`, not committed. The conformance programs are also one Ergo package each, and the same rule covers them (inferred from `splitMain`: with one user package, no group leaves the root). They were not run separately.
- Cross-package inlining: the Ergo inliner still sees the whole program, so the split does not change which bodies Ergo inlines. Go's inliner inlines across packages through export data. A function that Go inlined inside one package and does not inline across packages would show here as a loss; none showed.

### Std packages and init order (CS4b)

CS4b adds one Go package per std package that the program reaches (`std/<path>`). The lowering's naming table gives each std function and type its std package; a std body has no `//line` directive. A std package holds only std declarations. A program or generated declaration that a std package uses goes to the shared package.

Law InitsKeepTheirOrder: every Go `init` stays in the root, in the reference's order. A generated declaration now starts with the program declarations it names. Without the pin, a machine's graph registration would therefore move to its machine's package, and Go would run that init before the root's. `TestSplitLoweringAgrees` checks the law, and it kills `cs-split-init-order`.

Measured on 2026-11-13 at the CS4b head, at a 1-minute load of 32 to 39. The method is the same as above: `calc`, a fresh `GOCACHE` per mode warmed by a one-line program, and `GOFLAGS=-p=4`.

| Step | Reference: compiled | Split: compiled | Reference wall / user / sys (s) | Split wall / user / sys (s) |
| --- | --- | --- | --- | --- |
| First build | `main` | shared, std/prelude, std/os, std/flag, std/io, ast, eval, syntax, opt, typing, repl, main (12) | 2.76 / 1.80 / 0.45 | 3.03 / 2.02 / 0.65 |
| Edit an integer literal in `opt` | `main` | opt, main | 2.76 / 1.77 / 0.50 | 3.28 / 1.55 / 0.64 |

- The corpus (measured): all 768 clean programs of the checker's corpus were split with their std packages, then built and linked as one dispatch binary, as `TestCheckCorpusBuilds` builds the reference. All of them built. 64 of them split (66 std Go packages in all), and no declaration that is not std landed in a std package. This was a scratch test at the CS4b head, not committed.
- The examples (measured): `go run ./scripts/examples` passed under the split. The first full run failed only in calc, chat and confparse, with "no space left on device" (the host disk filled during the run). The rerun of those three passed its 17 cases. The reference lowering did not change since CS4, so its CS4 run stands.
- Gap: the shared package still holds the environment type `Env` and what it names. `Env` names the program's capability types, so a std package's compile depends on the shared package, and the std packages are not yet cached per pin (plan §3.3).
