# Receipt: the layout of the lowerer's IR (SELFLAYOUT SL9)

This is unit SL9 of the self-layout plan (`self-layout/plan.md` §4.1, in the run folder). It applies the method of the plan's §2.1 to the typed IR that the lowerer builds for the IR build path. It measures allocations and bytes per IR node, the GC share, the cost of a walk, and the bytes per node under the row form. The capstone's S4 fixes the self-hosted compiler's IR form on these counts. This is a receipt only: no product code changed.

## Pins and method

- Head: `wf10/irg6` `9722c9e4f`, the IR builder at its latest (IRG6 r4, batch 21 tip 73). Go 1.27.0, linux/amd64.
- Corpus: the `cmd/*` package of every module under `examples/`. That is 55 programs, each checked and lowered as `ergo build` lowers it (`irspike.Load`). On them, the IR builder gives 1,598 bodies an IR body. The other 2,233 bodies keep their Go source body (the builder's fallback).
- The probe is `internal/experimental/irspike/cmd/sl9probe`. It was never committed; it is kept in the run folder at `self-layout/SL9/tools/sl9probe.go`. For each program it runs:
  - `lower.Lower` (the source path) and `lower.LowerIR` (the IR path), once untimed and then once counted. It counts their allocations and bytes (`runtime.MemStats`) and decodes the IR file (`ergoir.json`) into a mirror of the lowerer's own IR types (`irFunc`, `irStmt`, `irExpr`, `irCase`). That mirror is the pointer tree the builder holds before it encodes.
  - A transcode of that tree into the row form of plan §2.2: an op column of 1 byte, a position column of 4 bytes (line and column packed), and two operand columns of 4 bytes each. A kind whose fields need more than two words spills them to one extra pool of `uint32` words. Lists are (start, length) in the pool. Strings are interned once. Children come before their parents. The schema of a kind is the union of the fields that the kind uses on the corpus.
  - One walk of each form, counted in process on a locked thread (`perf_event_open`, user mode, the best of 5).
- GC share: `pstat` (whole process, user-mode instructions, median of 3) of `sl9probe -mode lowerir -reps 3` (every program lowered 3 times through `LowerIR`) and of `-mode none` (load and check only), each at `GOGC=100` and at `GOGC=off`. The share is the lowering's instructions at the default minus those with the collector off, over the instructions at the default.
- An IR node is one function, statement, expression or case of the IR. The corpus has 43,432 of them in 41 kinds.
- All numbers are counts, so the host's load does not change them. The load was 45 to 63 during the runs. Raw output: `self-layout/SL9/probe.txt` and `gcshare.txt`.

## Results

| # | Measure, on the corpus | Pointer layout (today) | Row form | Ratio |
| --- | --- | --- | --- | --- |
| L1 | bytes per IR node held (structs, backing arrays, strings) | 264.8: `irStmt` is 280 B, `irExpr` 256 B, `irCase` 104 B, `irFunc` 112 B | 23.3: 13 B of columns per row, 2.2 extra words per node, 3,189 distinct strings in 33.7 KB | **11.4** |
| L2 | one walk of every node | 100.8 instr per node | 6.0 (op column only); 17.0 (op, a, b) | **16.8**; 5.9 |
| L3 | allocations per IR node, whole `LowerIR` | 150.6 (`Lower`: 156.3) | — | — |
| L4 | bytes allocated per IR node, whole `LowerIR` | 24,148 (`Lower`: 24,078) | — | — |
| L5 | decode of the IR file (the patched compiler's side) | 1.87 allocations, 287 bytes per node | 0 (the rows are the bytes) | — |
| L6 | GC share of `LowerIR` | 69,219 M at `GOGC=100` against 45,425 M at `GOGC=off` (3 × 55 lowerings, after the load and check) | — | **34.4 %** |

Readings:

- **The IR node itself is small next to what the lowering allocates.** `LowerIR` allocates 24 KB and 151 objects for each IR node it writes, and `Lower` allocates the same. Most of the cost is the lowering, not the IR: it emits Go text for every declaration and for the 2,233 fallback bodies. The IR tree is about 1 % of the bytes allocated (265 of 24,148 per node). A row form of the IR alone would change the lowering's allocation by about 1 %. The gains of S4 are in the lowerer's other state: the Go text, the per-function maps of `funcCtx` and the names.
- **The GC share is 34 %**, close to the parse's 40 % (plan §2.1 M1). The causes are the same: many short-lived objects per node. That supports compile-speed §5.2 rule 1 (no allocation per node, and the GC-share guard of plan §4.2) for the lowering stage.
- **The pointer IR costs 11 times the bytes of rows.** Each statement and expression struct carries every field that any kind uses, with 24-byte positions and 16-byte strings: 280 and 256 bytes for an average of 2 to 3 used words.
- **A walk costs 101 instructions per node on pointers and 6 to 17 on rows.** These are the same ratios as the syntax tree (M3: 127 against 8.1 and 16.1). The IR layer gives the same walk gain as the tree layer.
- **Schemas fit the (op, a, b) row.** On the corpus, 24 of the 41 kinds need at most 2 words and fit the row with no spill. They account for 69 % of the nodes: `local`, `const`, `field`, `ret`, `def`, `assert`, `toiface`, `fail`, `panic`, `zero`, `nil` and the like. The spilling kinds are `bin` (3 words), the calls (3 to 4), `lit` (5), the switches and cases (7) and `if` (8). They spill to the extra pool, as Zig's ZIR does.

## Decision for S4

Decided: the IR line of plan §2.2 is no longer a guess. The self-hosted IR is rows of (op, pos, a, b) with one extra pool per function, and kinds with more than two words spill — by SL9's counts (L1, L2, and the schema table); reverse by a new probe that shows a kind mix the row cannot hold in two words for most nodes.

Decided: S4 does not expect a row IR to make the lowering cheaper on its own. The 24 KB allocated per node is the lowerer's other state, so the census that S4 needs first is of the lowerer's maps and text buffers, as SL8 did for the checker — by L3, L4 and L6; reverse by a lowering whose allocation per node is near the IR's own bytes.

## Kinds on the corpus

| Kind | Nodes | Schema words |
| --- | ---: | ---: |
| e:local | 10,153 | 1 |
| e:const | 4,943 | 2 |
| e:field | 3,675 | 2 |
| e:bin | 3,338 | 3 |
| s:ret | 3,295 | 1 |
| s:def | 2,653 | 2 |
| case | 2,371 | 7 |
| func | 1,598 | 6 |
| e:assert | 1,365 | 2 |
| e:lit | 1,117 | 5 |
| s:if | 973 | 8 |
| e:pcall | 916 | 4 |
| e:mcall | 885 | 4 |
| e:call | 730 | 3 |
| s:expr | 703 | 1 |
| e:toiface | 690 | 2 |
| s:fail | 509 | 1 |
| s:tswitch | 502 | 7 |
| s:set | 473 | 3 |
| s:panic | 459 | 1 |
| e:zero | 451 | 1 |
| e:nil | 296 | 1 |
| 19 more kinds | 1,337 | 0 to 7 |

## Known gaps

- Only the bodies the IR builder lowers today are counted: 1,598 of 3,831. The fallback bodies have no IR node. When coverage grows, the kind mix can shift toward the larger kinds.
- The walk reads each node's kind (and, in the second row column, its operands). It does no work per kind, as M3 of the plan's §2.1 does not either.
- Allocation per node is for the whole lowering. The split between the IR builder and the Go emitter was not measured.
