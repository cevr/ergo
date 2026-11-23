# Native A/B receipt: route A against route B, stage by stage

NATIVE-AB-MEASURE (spec: wf10 research/NATIVE-TOOLCHAIN). Route A is
today's: Ergo's generated Go parsed and type-checked by gc, IR bodies
attached after `checkFiles`, then the unified write and read and the rest
of gc. Route B hands gc a checked package (`-ergopkg`, NATIVE-TYPED-HANDOFF)
and so has no parse or check of Go text. Every number is user-mode
instructions (`perf stat -e instructions:u`) or a count of work, never wall
time. A cell nobody measured reads **unmeasured** with what it waits for;
it is never 0.

## How to read and rerun

- `compiler/cmd/nativeab` (Ergo): `verify --seed N` checks the attribution
  against drawn builds (phase order, parser workers, packages of both
  origins and routes, cold, hit, private, public and dependency edits;
  duplicate, incomplete, lost and pid-reused records) and then every
  capture in `testdata/route-a/`. `capture` runs one `ergo build` under
  perf (the tree's total) with itself as the go command's `-toolexec`, so
  each tool process is counted alone. `attribute FILE` prints a capture's
  cells.
- Conservation: the cells of a capture sum to the tree's total exactly.
  The driver cell is the explicit residual (tree less every tool
  process): ergo itself (its own parse, check and lowering of the Ergo
  source), the go command, the wrappers and perf.
- A capture sets GOFLAGS, so ergo's direct build path (which runs compile
  and link itself) refuses and falls back to `go build`, its reference.
  Each compile command is the same on both paths; the driver cell is the
  go command's. This host's PMU counts but gives `perf record` no
  samples, so the direct path's processes cannot be split from outside.
- Stage cells (parse, check, ergoir, unified write, unified read, gc)
  need the scratch-only phase instrument
  `compiler/cmd/nativeab/testdata/phase-instrument.patch` (data; lane FORK
  reviews it and applies it to a scratch root, never a release hunk). It
  counts every thread of the compile (one inherited counter per thread),
  so the parser's worker goroutines are counted once, in the interval
  they ran. Until then each compile is one cell, `unassigned`.

## Smoke: the two-package subject (route A)

Subject `compiler/cmd/nativeab/testdata/subject`, ergo at the lane's merge
3a7ce6916 (main 887996c35), go1.27.4 with the IR compiler d963a83aca81,
shared GOCACHE, linux/amd64. Gate log: unit folder `gate-1.log`.

| cell (instructions:u) | first build | private edit of `subject/lib` |
|---|---:|---:|
| driver (ergo + go command + wrappers) | 1,005,044,742 | 1,025,375,883 |
| compile `main` (generated) | 293,104,805 | 240,492,150 |
| compile `ergo.local/program/std/io` (generated) | 269,549,319 | hit: no work |
| compile `ergo.local/program/shared` (generated) | 108,556,961 | hit: no work |
| compile `ergo.local/program/std/prelude` (generated) | 42,658,092 | hit: no work |
| ordinary Go and runtime compiles | hit: no work | hit: no work |
| link | 326,946,663 | 346,973,411 |
| **tree** | **2,045,860,582** | **1,612,841,444** |
| route A parse / check / ergoir / unified write / unified read / rest of gc | unmeasured: waits FORK applying the phase instrument (NATIVE-TYPED-HANDOFF lane) | same |
| route B, every leg | unmeasured: waits NATIVE-TYPED-HANDOFF supporting the workload | same |

What it shows (measured unless marked): the edit of `subject/lib`
recompiled one generated package, `main`, and nothing else; the generated
module (`ergo.local/program`) holds the subject's code in `main` (inferred
from the one rebuilt action). On a two-package program the driver is
about half the tree (49% and 64%), and the compiles of generated Go are
35% and 15% of it.

## The matrix (waits the measurement slot)

| workload | route A | route B |
|---|---|---|
| lru, okra-how, regex, stackvm, sudoku | waits the measurement slot | unmeasured: waits NATIVE-TYPED-HANDOFF |
| the self-hosted compiler | waits the measurement slot | unmeasured: waits NATIVE-TYPED-HANDOFF and S4 |
| a Go-heavy consumer | waits the measurement slot | unmeasured: waits NATIVE-TYPED-HANDOFF and ERGO-GO |
| a multi-package body edit | waits the measurement slot | unmeasured: waits NATIVE-TYPED-HANDOFF |
