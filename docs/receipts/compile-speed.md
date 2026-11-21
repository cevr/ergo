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
(the binary's init work), or the request inside `ergo dev` (CS9). The
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

## Warm `ergo test ./...`

CS8b: an unchanged `ergo test ./...` whose every package passed answers
from the warm store. Before CS8b, each package replayed its test result
cache only after discovery had parsed and checked every package. Measured
with pstat: the user-mode instructions of the whole process tree, 3 warm
runs after one cold run, GOMAXPROCS=4. The base binary is wf10/cs8
9029811e9 and the head binary is wf10/cs8b. Host load was 47 to 72 (the
instruction counts hold under load; the wall times do not):

| Example | Base M instr | Head M instr | Base: files parsed, packages checked | Head: files parsed, packages checked | Head bytes hashed |
| --- | ---: | ---: | ---: | ---: | ---: |
| kvstore | 529 to 537 | 5.5 to 5.6 | 65, 10 | 0, 0 | 38,228 |
| deps | 857 to 948 | 5.5 to 5.7 | 84, 19 | 0, 0 | 58,909 |
| adventure | 238 to 246 | 4.6 to 4.8 | 36, 6 | 0, 0 | 21,932 |

Neither side starts a process or does a Go action. Base and head print
the same output (stdout and stderr), apart from the replay notes. The
head's 4.6 to 5.7 M instructions include the `ergo` process start (2.1 to
3.1 M, section Thin client). That leaves about 2.5 M instructions after
the start, inside the 3.3 M warm budget (plan §3.2; inferred, by
subtraction).

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

## Std snapshot in the binary

Compile-speed unit CS1 embeds the checked std snapshot of CHK1b in `ergo` (`internal/sketchfront/stdsnap.go`; docs/tooling.md, "Embedded std snapshot"). Measured on 2026-11-13 at the CS1 head, at a 1-minute load of 28 to 55. The base is wf10/batch-18 a85c26dee.

- Counted (`TestColdCheckReadsNoStdSource`, in process, with every cache empty): a check of a program that imports `strings` and `time` parses 0 std files and checks 0 std packages. It decodes as many std packages as the check from source checks, and no more.
- Measured (`ergo check` on that program, a fresh `XDG_CACHE_HOME` for each run, 5 alternating runs). The numbers are seconds of user CPU, and the maximum resident set:

| Binary | User CPU per run (s) | Max RSS |
| --- | --- | --- |
| head, snapshot on | 0.02, 0.01, 0.02, 0.02, 0.02 | 24 MB |
| head, `ERGO_STD_SNAPSHOT=off` | 0.40, 0.44, 0.41, 0.43, 0.58 | 97 to 107 MB |
| base | 0.43, 0.42, 0.35, 0.37, 0.44 | 103 to 109 MB |

  The base's cold check checks std from source, then writes the disk cache. The head's check needs neither. Wall times were 0.04 to 0.11 s on and 0.74 to 1.85 s off. The load was high, so the wall times are not a verdict.
- Measured: the binary is 33,736,716 bytes at the head and 30,469,767 bytes at the base (+3.27 MB). The snapshot is 3,172,893 bytes, about 1.46 MB gzipped (plan §4 guessed 1 to 3 MB).
- Follow-up done in CS1b: the export is deterministic, so a regeneration that changes nothing checked rewrites the same bytes, and git stores no new blob. The snapshot is now 3,010,260 bytes: it no longer carries the type tables' memos (about 160 KB). Two `-update-std-snapshot` runs in separate processes wrote byte-identical files (cmp).

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

## Direct path (CS10)

CS10 splits the IR path too (`lower.LowerIRSplit`): each Go package of the split compiles with the IR file of its own bodies, on the direct path, which keys an importer's compile on each import's export data (`__.PKGDEF`), not on its archive, and starts the compiles of independent packages together. An IR build after the first starts no go command: the release's GOROOT answer is kept (`toolchain.SourceOf`), and the toolchain plan and the std archives were already kept. Measured on 2026-11-13 at the CS10 head `fe83cd718` on the workbox (`go run ./scripts/buildcost -sizes 10,50 -examples ""`, then the same with `ERGO_LOWER=single`, the one-package reference), `GOMAXPROCS=1`, at a 1-minute load of 66 to 101. The counts do not depend on the load; the instructions are the process tree's user-mode instructions.

| Scenario | Split: compiles run / reused | Reference: compiles run / reused | Split instr (M) | Reference instr (M) |
| --- | --- | --- | --- | --- |
| g10 leaf-body-edit | 4 / 13 | 1 / 3 | 3,887 | 6,118 |
| g10 main-body-edit | 1 / 16 | 1 / 3 | 3,299 | 6,121 |
| g50 leaf-body-edit | 3 / 41 | 1 / 3 | 6,840 | 13,096 |
| g50 main-body-edit | 1 / 43 | 1 / 3 | 7,374 | 13,465 |
| g50 first | 44 / 0 | 4 / 0 | 33,557 | 32,129 |

- Every edit build in both modes ran 0 go commands (`go_commands`); a first build runs 4 (`go env GOROOT`, the IR compiler's build, `go env`, `go list`).
- Measured: after a body edit of the lowest package, the split compiles that package and only the importers whose imports' export data changed: 4 of 17 packages at g10, 3 of 44 at g50. The edit (`3 * c.R * c.R` to `4 * …`) is in a function small enough for Go's inliner, so its body is in the export data, and the importers that may inline it are compiled again, as they must be: their objects hold a copy of the old body. A body Go does not inline compiles one package (`TestDirectRebuildsOnEachInput`, row "dependency body, same export data").
- Measured: an edit build costs 36 to 48 % fewer instructions than the reference at g10 and g50, because the compiles are of the edited packages, not of the whole program.
- Measured: the first build of g50 costs 4 % more instructions than the reference (44 compile processes against 4). Each process pays the empty-package cost (see "Empty-package compile"). With `GOMAXPROCS` above 1, independent compiles run at once (`TestDirectCompilesInParallel`); this receipt ran with 1.
- g10 first is not comparable: the split run was the first of the receipt and built the IR compiler into a cold Go build cache (630,333 M); the reference run found it built (25,239 M).
- Not done, by the plan's own conditions: IRT0a measured that JSON decoding costs more than Go parsing per unit of program (about 2.9 % of an IR package's compile), so a binary IR encoding is a follow-up, a change of the compiler patch. The declaration-file share of the DECL rule is not measured yet, so the stubs stay in the Go files. Std's export data comes from the pin archives since CS3.
- The raw log is the wf10 run's `compile-speed/cs10/receipt-buildcost.log`.

### At GOMAXPROCS=4

The same scenarios at `GOMAXPROCS=4` (CS9 head `de6ab4614`, 1-minute load 84 to 91 on 16 cores):

| Scenario | Split: compiles run / reused | Reference | Split instr (M) | Reference instr (M) | Split wall (s) | Reference wall (s) |
| --- | --- | --- | --- | --- | --- | --- |
| g10 first | 17 / 0 | 4 / 0 | 23,854 | 22,760 | 12.7 | 11.7 |
| g10 leaf-body-edit | 4 / 13 | 1 / 3 | 3,719 | 5,404 | 2.2 | 3.6 |
| g50 first | 44 / 0 | 4 / 0 | 31,354 | 29,925 | 15.3 | 14.8 |
| g50 leaf-body-edit | 3 / 41 | 1 / 3 | 6,627 | 11,922 | 4.1 | 5.7 |
| g50 main-body-edit | 1 / 43 | 1 / 3 | 6,853 | 11,931 | 4.2 | 6.8 |

- The counts are the same as at `GOMAXPROCS=1`.
- Measured: an edit build costs 31 to 44 % fewer instructions than the reference.
- The first build costs 5 % more instructions. At this load, its wall time is not shorter than the reference's, although independent compiles are started together. The host was saturated (load above 80 on 16 cores), so these wall times show no parallel speedup either way. A wall-time claim for the parallel compile needs a quiet host.

## ergo dev (CS9)

The measurements below were taken while the command was named `ergo serve` (renamed `ergo dev` by the owner on 2026-11-14, docs/decisions.md); the raw logs keep the old scenario labels (`serve-request`).

`ergo dev` answers a warm `ergo check` on its socket with no process start (docs/tooling.md, "ergo dev"). It was measured on 2026-11-13 at the CS9 head `de6ab4614` (`go run ./scripts/buildcost dev -sizes 10,50 -examples ""`, at a 1-minute load of 82 to 84) as follows. A daemon was started with `--idle 2s`, sent 200 warm-check requests by the measuring process, and measured from its start to its idle exit. A daemon that was sent no request was measured the same way. The difference, divided by 200, is the daemon's work for one request.

| Program | CLI warm check (M instr, one process) | Daemon, one request (M instr), GOMAXPROCS 1 / 4 | Inputs read and hashed per request | `ergo check` as the daemon's client (M instr) |
| --- | --- | --- | --- | --- |
| g10 | 4.1 | 1.7 / 2.4 | 22 files, 116,568 bytes | 2.9 (reads no input) |
| g50 | 7.6 | 5.6 / 6.4 | 88 files, 528,822 bytes | 3.0 (reads no input) |

- Measured: a served request starts no process and costs 42 % (g10) and 74 % (g50) of the instructions of the CLI's warm check, at GOMAXPROCS=1.
- Measured: the daemon reads and hashes every input the warm manifest names, as the CLI's warm path does: its work grows with the program, not with the files that changed.
- Measured: the plan's budget for a request inside `ergo dev` is 1 ms at any program size, in proportion to the files that changed (§3.1), and is not met. The median request took 3.1 ms at g10 and 8.0 ms at g50, at a load above 80. Meeting it needs the daemon to keep the inputs' hashes between requests and to re-hash only the files a file event names. The plan allows file events as a hint, not as evidence, so that is a design step of its own (a follow-up).
- `ergo check` as the daemon's client still pays its own process start (2.9 to 3.0 M), the floor of any CLI command ("Process start"). An editor that speaks the socket pays none.
- The raw log is the wf10 run's `compile-speed/cs9/receipt-buildcost.log`.

### Kept reads

After the measurement above, the daemon keeps each input's hash, each folder's listing digest and the decoded manifest while their stat stays the same (`sketchfront.StatSums`; docs/tooling.md, "ergo dev"). It was measured again on 2026-11-13 at the code of `d0220a73e`, with the same command, at a 1-minute load of 47 to 49:

| Program | Daemon, one request (M instr), GOMAXPROCS 1 / 4 | Before kept reads (M instr) | Inputs read per request | Request wall time, median / p90 (µs), GOMAXPROCS=1 |
| --- | --- | --- | --- | --- |
| g10 | 0.41 / 0.42 | 1.7 | none (22 stats) | 467 / 1,983 |
| g50 | 0.64 / 0.64 | 5.6 | none (88 stats) | 666 / 4,060 |

- Measured: a served request reads and hashes no unchanged input. Its daemon counters (`work`) are all zero.
- Measured: it costs 76 % (g10) and 89 % (g50) fewer instructions than before, and 10 % and 8 % of the CLI's warm check.
- Measured: the median request is under the plan's 1 ms budget at g10 and g50, from a client in another process over the socket; the 90th percentile is not, at this load. Wall times are context, not a claim; the counts are the receipt.
- Its work still grows with the program: 0.41 M at 22 inputs and 0.64 M at 88, the stats of the inputs and folders. Work in proportion only to the files that changed needs a file watch, with the stat as the evidence the watch hints at; that is a follow-up.
- The raw log is the wf10 run's `compile-speed/cs9/receipt-buildcost-2.log`.

## Declaration shards

Test-speed FT9 (`lower.LowerIRShards`, the fallback of the killed in-compiler function reuse FT9a) cuts each big package of a test build into Go packages, so a body edit compiles one shard. Measured on 2026-11-15 at `6e98da091` on the workbox: the IR lowering of the `examples/deps` test programs, split and sharded, each program package compiled by ergo's patched compile (go1.27.4) with the dev flags `-l -dwarf=false` and the go command's own flags (`-c=4`), `GOMAXPROCS=4`, the median of 5 runs of the user-mode instructions, at a 1-minute load of 34 to 48.

| Program | Split: root compile (M) | Shards: each shard (M) | Shards: the costliest shard (M) | Shards: the root (M) | Clean, all program packages (M): split / shards |
| --- | ---: | ---: | ---: | ---: | ---: |
| `deps/graph` tests (6 shards) | 1,688 | 122 to 337 (7 to 20 %) | 1,001 (59 %): the tests and laws | 708 (42 %) | 1,711 / 2,663 |
| `deps/resolve` tests (12 shards) | 1,971 | 128 to 545 (6.5 to 28 %) | 899 (46 %): `Resolve` and the table's generator | 784 (40 %) | 2,950 / 5,757 |

- Measured, with which shard an edit lands in from `TestBodyEditChangesOneShard`'s log (seed 1, 10 edits of literals in function bodies per program): all 10 edits of `graph.ergo` compile the shard of 181 M (10.7 % of the split's root compile); 9 of 10 edits of `resolve.ergo` compile a shard of 217 M to 347 M (11 % to 18 %), and 1 the shard of `Resolve` at 899 M (46 %).
- Measured: one shard per program misses FT9's 40 % bound: the tests and laws of `deps/graph` (59 %) and `Resolve` with the table's generator in `deps/resolve` (46 %); the roots are at 40 % and 42 %. A shard is cut by bytes of Go, and these compile at about 30 M per KB against 3 M per KB for the rest (`sff93794e` 33 KB at 1,001 M; `sa42a4527` 61 KB at 181 M). The second cut, below, weights the cost.
- Measured: a clean build compiles 56 % (graph) to 95 % (resolve) more instructions sharded, since each shard pays the process start and its imports (58 M to 107 M, plan §3.5), in more processes that the direct path runs at once.

Second cut, at `2f9663cb0` (load 45 to 46): a shard's cost counts the generic declarations it instantiates, and only a package of 64 KiB is cut (`deps/graph`'s 42 KB package and `deps/semver`'s 24 KB are not).

| Program | Split: root compile (M) | Shards: the shard user-function edits compile (M) | Shards: the costliest shard (M) | Shards: the root (M) | Clean, all program packages (M): split / shards |
| --- | ---: | ---: | ---: | ---: | ---: |
| `deps/graph` tests (5 shards) | 1,688 | 164 (9.7 %), all 10 edits | 899 (53 %) | 745 (44 %) | 1,711 / 2,885 (+69 %) |
| `deps/resolve` tests (5 shards) | 1,971 | 439 (22 %) for 9 of 10 edits; 866 (44 %) for 1 | 866 (44 %) | 734 (37 %) | 2,950 / 4,249 (+44 %) |

- Measured: counting instantiations moves the tests and laws of `deps/graph` into a shard of 6.4 KB of Go that still costs 486 M: most of a shard's cost is the instantiations of the program's generic graph, which Go compiles again in every package that makes them. Cutting finer duplicates them; cutting coarser keeps every edit dear. No cut reaches a cold build within +25 % of the split (+44 % to +95 % across both cuts).
- So the first test build of a program does not shard (cold: the split's cost, +0 %), and every later one does (`lowerAuto`). The first build after it compiles every shard once (2,885 M and 4,249 M against a split root recompile of 1,688 M and 1,971 M); each edit after that compiles one shard (164 M to 866 M).
- Not done: emitting each instantiation once, in the generic's own shard, would remove the duplicated cost; that is the monomorphization work (MONO1), not a shard cut.
- The receipt compiles without the inliner (`-l`, FT5's dev profile), so a body never enters export data and no importer of the edited shard compiles again. Until FT5 lands, a body small enough for Go's inliner compiles its importers too (see "Direct path").
- The script and the raw log are the wf10 run's `test-speed/FT9a/receipt/`.

## Shared instances

MONO-HOOKS-MIN (2026-11-19) replaced the mechanism below: shared instances are now MONO1's monomorphic copies, compiled once each, with no instance package and no hook in Go's compiler ([tooling](../tooling.md#package-split)). Its receipt follows this section's 2026-11-15 table, which stays as the record of MONO-SHARED.

MONO-SHARED (`internal/sketch/lower/pkginst.go`, the patched compile's `noder/ergoshared.go`) compiles each shared generic instance once, in package `inst`, where before each package or shard that named it compiled it again ([tooling](../tooling.md#package-split)). This was measured on 2026-11-15 at `6980a712d` on the workbox, with the `deps/graph` and `deps/resolve` test modules that `ergo test` writes, each built twice with the patched compile (go1.27.4): once with the shared lists and once with them stripped. The stripped build is the reference, Go's own rule, and its total leaves `inst` out. The figures are compile instructions in millions, the median of 5 runs. The load was 62 to 78; instruction counts do not track load as times do. The two `deps/graph` split builds share nothing, so they are the same build twice, and the 1.8 % between them is the noise. With shards every non-root user shares; in a split, only an instance two packages name.

| Program, profile | Costliest shard, an edit's cost (M): Go's rule / shared | Clean, all program packages with `inst` (M): Go's rule / shared | `inst` (M) |
| --- | ---: | ---: | ---: |
| `deps/graph` shards, inliner on (`ergo test`) | 1,694 / 610 (−64 %) | 4,591 / 4,148 (−10 %) | 1,804 |
| `deps/resolve` shards, inliner on | 1,369 / 904 (−34 %) | 7,123 / 7,145 (+0.3 %) | 1,030 |
| `deps/graph` shards, `-l` | 921 / 707 (−23 %) | 3,118 / 3,861 (+24 %) | 1,758 |
| `deps/resolve` shards, `-l` | 853 / 610 (−28 %) | 4,947 / 5,246 (+6 %) | 982 |
| `deps/resolve` split, inliner on | (root) 3,104 / 2,625 | 4,356 / 4,168 (−4 %) | 764 |
| `deps/resolve` split, `-l` | (root) 2,002 / 1,803 | 2,943 / 3,270 (+11 %) | 787 |

- Measured: an edit in the costliest shard now costs 23 % (graph) and 29 % (resolve) of the split's root compile with the inliner on. Before, it cost 63 % and 44 %, which missed FT9's 40 % bound. With `-l` it costs 41 % and 30 %, against 53 % and 43 % before.
- Measured: sharing every instance in a split made both clean builds dearer with `-l`, since `inst` reads and compiles what one package would have compiled anyway (`matrix-1.log`: graph 1,664 → 1,791, resolve 2,933 → 3,433). The split now shares only an instance two packages name (`minUsers`). That leaves the `deps/graph` split unchanged and the `deps/resolve` split 4 % cheaper with the inliner on.
- A sharded clean build is still +52 % (graph) and +64 % (resolve) over the split with the inliner on, so it misses FT9's cold bound of +25 %. `lowerAuto` therefore keeps the first test build of a program unsharded.
- Not done: `cutShards` still adds a shard's instantiations to its cost, although `inst` now compiles them. The cuts measured above were made under that cost model.
- The scripts and raw logs are the wf10 run's `dod/mono-shared/receipt/`.
