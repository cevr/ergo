# Self-layout receipt: the data layout of the self-hosted compiler

This receipt holds the counts behind the data layout of the self-hosted compiler (capstone [#196](https://github.com/cevr/ergo/issues/196)). The plan is the wf10 SELFLAYOUT plan (run dir `self-layout/plan.md`, 2026-11-14). The sources are in [PRIOR_ART.md](../../PRIOR_ART.md#compiler-data-layout-flat-trees-and-dense-tables-2026-11-14). Every count is user-mode instructions, allocations or bytes; no row is a wall time.

Each figure has one of these labels:

| Label | Meaning |
| --- | --- |
| **measured** | counted on the exe workbox: 16 cores, Go 1.27.0, linux/amd64 |
| **source** | read at a primary source on 2026-11-14 |
| **inferred** | reasoned from measured or source facts |

The raw receipts are files of the run, not of the repository.

## 1. The workload that every later unit reports: `toktree`

`examples/toktree` reads a file of source text and scans it into token columns (`kind uint8`, `start uint32`). It builds the flat tree of its bracket groups on std `dense`:
- rows are kept child-before-parent;
- the child lists sit in one pool;
- one scratch stack with marks collects each list.

It then reads every token and every child list once. This is the parser's data movement without the grammar. The control, `cmd/ergo/testdata/toktree/control`, is the same layout in hand Go, with its columns sized from the input.

```sh
go run ./scripts/opcount -control -rounds 5 toktree
```

Measured at `198c9655f` (branch `wf10/sl1`, std `dense` of DN1 and DN2). The input is 4,000,000 and 32,000,000 bytes of the repository's own `.ergo` files (`cmd/ergo/testdata/toktree/gen`), and one unit is a byte. The figures are medians of 5 rounds, with the two sides alternating. The load average was 45 to 54; instruction counts do not depend on it.

| Counter | Ergo on `dense` | Hand Go | Ergo / Go |
| --- | --- | --- | --- |
| Instructions per byte | 60.81 (spread 2.1%) | 32.79 | **1.854** |
| Instructions per byte, `GOGC=off` | 61.26 | 32.77 | 1.870 |
| GC share of instructions | 0.01 (spread 50%) | 0.00 | — |
| Allocations per byte | 0.00 | 0.00 | 6.1 (counts of a few hundred) |
| Bytes allocated per byte | 20.76 | 8.57 | 2.42 |
| GC cycles at 32 MB | 11 | 5 | 2.2 |
| Peak resident set at 32 MB | 229 MB | 153 MB | 1.50 |

The 2.4 times the bytes come from stores that grow by doubling. Std `dense` has no capacity yet; SL2 adds `WithCap`. Every later unit of the plan (SL2, SL3, SL4, MONO1) reports this ratio. The target is 1.2 or less.

### After SL2: capacities, a scratch stack and a bulk PutList

SL2 adds `dense.WithCap` (and `ColWithCap`, `PoolWithCap`, `StackWithCap`), `dense.Stack` with `Mark`, `Since`, `Cut` and `PutSince`, and makes `PutList` one bulk append (`slices.AppendAll`, an extern of Go's `append` at `[T](xs []T, ys []T) []T`). `toktree` sizes its stores from the input, as the control does, and gathers every child list on one `Stack`. The receipt is the same command, measured at `9a659054f` plus the `EmptyRunsKeepTheirPlace` law (no program change), load 44 to 54.

| Counter | Ergo on `dense` | Hand Go | Ergo / Go |
| --- | --- | --- | --- |
| Instructions per byte | 50.28 (spread 0.0%) | 32.83 | **1.531** (SL1: 1.854) |
| Instructions per byte, `GOGC=off` | 50.27 | 32.8 | 1.53 |
| GC share of instructions | 0.003 | 0.00 | — |
| Allocations between 4 MB and 32 MB | 11 | 11 | 1.00 |
| Bytes allocated per byte | 6.94 | 8.57 | **0.81** (SL1: 2.42) |
| GC cycles at 32 MB | 4 | 5 | 0.8 |
| Peak resident set at 32 MB | 143 MB | 153 MB | 0.94 |

With ids from a `dense.Count` and `dense.IDAt` in place of a `Table` of a type with no fields (SL4b's finding: Go grows a slice of zero-size elements on every append), the same receipt gives Ergo 48.45 instructions per byte against hand Go's 32.83: **1.476**, with the same bytes (6.94 per byte) and peak resident set (144 MB against 153 MB).

The plan's SL2 targets hold: bytes allocated are below hand Go's (target: at most 1.2 times), and the allocations at the two sizes differ by as many as hand Go's, 11. Both come from the one scratch stack, whose top level holds every top-level row and so grows with the input, in the program and in the control alike. The instruction ratio, 1.53, is SL3's, SL4's and MONO1's to close (target 1.2 or less).

### After SL2b: tables of rows of no size, a pure WithCapacity, the full check on growth

SL2b makes three changes:
- A table of rows of no size takes room for every id once. Before it, Go grew such a slice on every `Put`.
- `slices.WithCapacity` is pure, so the stores' constructors take no environment.
- A table checks for a full store only when it grows.

The command is the same, at `3f54f4a3a` (source path). Each variant is a scratch edit, counted and reverted.

| toktree, source path | ergo instr/B | hand Go | ratio |
| --- | --- | --- | --- |
| SL2 (`7ca9caf8e`) | 48.45 | 32.83 | 1.476 |
| SL2b head | 39.68 | 32.83 | 1.209 |
| head, `WithCapacity` not pure | 48.45 | 32.83 | 1.476 |
| ids from `Table` (field-less rows), not `Count` | 42.05 | 32.84 | 1.280 |
| the same, before the full check moved to growth | 42.79 | 32.84 | 1.303 |
| the same, with no field-less fix | 50.69 | 32.84 | 1.544 |
| head, no full check in `next` at all | 38.49 | 32.84 | 1.172 |

Bytes allocated stay at 0.81 of hand Go's.

`Count.Next` keeps its check: a `Count` has no room to check once against. That check costs 1.19 instructions per byte on the source path (the head against the last row) and 0.53 on the IR path (below).

On the IR build path, the tree is `wf10/batch-21pre` plus SL1 to SL2b and FK2's OPCOUNT-IR (`1cdaefb57`), so `scripts/opcount` builds the program the product way. The command is the same. The receipt reads `path ir`; the IR bodies are 2 of 18 functions in the main package.

| toktree, IR path | ergo instr/B | hand Go | ratio |
| --- | --- | --- | --- |
| SL2b head | 50.72 | 35.79 | 1.417 |
| ids from `Table` | 69.93 | 35.80 | 1.954 |
| the same, with no field-less fix | 79.76 | 35.80 | 2.228 |
| head, no full check in `next` | 50.19 | 35.80 | 1.402 |

Bytes allocated are 0.84 of hand Go's on this path.

### The plan's 11.6% GC share, and its 2.30 ratio: the input path, not the stores

The plan's prototype (`self-layout/proto/toktree-ergo`) counted Ergo at 82.87 instructions per byte against hand Go's 35.96 (2.30). Its default run cost 11.6% more than its `GOGC=off` run. The cause was left open.

It read its input with `io.Stdin.ReadAll`. The example reads its file with `fs.Files.ReadFile`, and is otherwise the same program.

One run each on the 32 MB input, through the IR-G exit hook and a CPU profile, measured:

| | prototype (stdin) | example (file) |
| --- | --- | --- |
| allocations | 17,187 | 1,450 |
| bytes allocated | 786 MB | 656 MB |
| GC cycles | 18 | 11 |
| runtime GC CPU (timed, one run) | 0.033 s | 0.004 s |

The profile names the reader. `runtime/v1`'s `lineReader.read` takes standard input 4,096 bytes at a time, each read on a goroutine of its own, and appends each chunk to one growing buffer. On 32 MB that is about 7,800 reads. They account for:
- the 15,700 extra allocations;
- 130 MB of buffer copies (`growslice` and `memmove`: 51% of the profile's samples);
- 7 extra collections.

It also explains the plan's 6 to 8% spread (inferred): the reads run on runtime goroutines whose scheduling under load varies.

The stores themselves allocate almost nothing: 0.0005 allocations per byte in the plan, and fewer here. With the file read, the GC share falls to about 1%, and the ratio falls from 2.30 to 1.85. The `ReadAll` of `io.Stdin` is a std reader's cost, owned outside this plan. It is the first lead for a program that reads a large standard input.

## 2. The Go compiler's layout today (from the plan, §2.1)

These were measured by the plan's probe (`self-layout/proto/slprobe`, `proto/slscan`) on the 1,398 `.ergo` files that `syntax.Parse` accepts at `44febe882`: 4,477,276 bytes, 744,189 tokens and 606,585 nodes.

| Measure | Pointer layout (today) | Flat layout | Ratio |
| --- | --- | --- | --- |
| `syntax.Parse`, instructions per byte | 553 (`GOGC=off`: 328) | — | GC share 40.3% |
| allocations in the parse | 0.217 per byte, 1.60 per node | — | — |
| bytes allocated | 31 per source byte, 230 per node | — | — |
| live after the parse | 33 MB in 850,460 objects | 13.5 MB in 250 objects | 2.5 |
| scan only, instructions per byte | 298 | 181 | 1.63 (all GC: 1.02 with `GOGC=off`) |
| one walk of every node | 127 instructions per node (`Inspect`) | 8.1 per row | 15.7 |
| one forced collection with the trees live | 265 M instructions | below the spread | > 100 |

Bytes per node by layout, for Ergo's grammar. The figures are measured: a transcode of every parsed tree, or a model of it.

| Layout | Bytes per node |
| --- | --- |
| Go structs as today | 54 live on the heap |
| Flat, built (Zig's form: kind, main token and two operands, plus one extra pool) | **20.3** |
| SWC Next's form, by model | 28.5 |
| Yuku's form, by model | 54.0 |
| Carbon's form (8 bytes per token) | 9.8 |

Reproduce with the plan's `proto/run-pairs.sh` and `run-pairs2.sh`. The plan's §10 lists the two probe files, which are never committed.

## 3. The published parsers, counted on this host (from the plan, §1.4)

The parsers parse `typescript.js` (8,207,497 bytes) through npm under Node 24.11.1. One parse is the slope between 2 and 10 parses in one process. The count is user instructions of all threads. The script is the plan's `proto/run-js.sh` (`proto/js/p.mjs`).

| Parser | Instructions per parse | Ratio to SWC Next | Published wall ratio (source) |
| --- | --- | --- | --- |
| SWC Next 0.6.1 | 1.13 G (138 per byte) | 1.00 | 1.00 |
| Yuku 0.27.0 | 1.20 G | 1.06 (inside the noise) | 1.13 |
| Oxc 0.158.0, JSON transfer | 7.82 G | 6.9 | 6.76 |
| SWC 1.16.22 | 13.02 G | 11.5 | 12.81 |

The native benchmarks are source only: the host has no `cargo`, `rustc` or `zig`.

| Native parse, `typescript.js` (source) | ms |
| --- | --- |
| SWC Next | 12.29 |
| Yuku | 13.84 |
| Oxc | 17.73 |
| SWC | 32.59 |

## 4. What the counts decide

The plan's §2.5 holds the decisions. These numbers back them:
- **The tree's form.** The syntax tree takes Zig's 13-byte form, at 20.3 bytes per node.
- **No arena construct.** Pointer-free columns already cost the collector nothing measurable.
- **The parse budget.** The budget of 60 instructions per byte stays the destination: hand Go moves the data in 33 to 36. Ergo is at 1.85 times that today.
