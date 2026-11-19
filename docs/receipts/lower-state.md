# Receipt: what the lowering allocates (SELFLAYOUT SL9b)

This is the census that SL9 (`docs/receipts/ir-layout.md`) named as S4's first need. The IR tree is about 1 % of what a lowering allocates. This receipt shows where the other 99 % goes: by owner (the file and the function of the lowerer) and by structure (a map, a slice, text, gofmt), with the GC share, and with what the lowerer keeps live at its end. It is in the style of SL8 (`docs/receipts/checker-maps.md`). It is a receipt only: no product code changed.

## Pins and method

- Head: `main` `d3bb9dac6`. Go 1.27.0, linux/amd64. The path measured is `lower.Lower`, which is the build's path at main.
- Corpus: the `cmd/*` package of every module under `examples/`, 53 programs, each checked and lowered as `ergo build` lowers it (`irspike.Load`). They lower to 10,961,802 bytes of Go.
- Allocations: each program is lowered once untimed, so that shared caches are warm, then once with `runtime.MemProfileRate = 1`. Every allocation is recorded with its stack, and the counts are exact, not sampled. Each allocation is charged to:
  - an **owner**: the innermost frame in package `lower`, by its file and its function;
  - a **structure**: what the frames between the allocator and the owner make. In order: `go/format` (any `go/printer`, `go/parser`, `go/scanner` or `go/ast` frame), `fmt`, text buffer growth (`strings.Builder` or `bytes.Buffer`), `strings` and `strconv` results, sort and slices helpers. Otherwise the runtime leaf decides: map make, insert and growth; slice make and append growth; string concatenation and conversion; a `newobject` (a struct, `&T{}` or an escaping local).
- Retained state: a temporary hook (never committed) walks the `*lowerer` by reflection at the end of each lowering. It charges each field the bytes reachable from it that no earlier field reached. The checker's `pkg` and `info` are walked first, so the lowerer's fields are charged only what they add. Maps are sized as Swiss tables: 8 slots per group, a control byte per slot, a load of 7/8.
- GC share: `pstat` (whole process, user-mode instructions, median of 3) of the probe lowering every program 3 times, against loading only. Each was run at `GOGC=100` and at `GOGC=off`.
- The probe's own allocations are left out: any stack with a frame of the probe (`main.`) before a `lower` frame, which is the hook's walk. All numbers are counts, so the host's load does not change them.
- The tools and raw output are in the run folder: `self-layout/SL9b/tools/` (`census.go`, `hook.diff`), `census.txt` and `gcshare.txt`.

## Totals

| Measure | Value |
| --- | --- |
| Allocated by the 53 lowerings | 1,022 MB in 5.31 M objects (charged to `lower`), about 19 MB per program |
| Per lowered Go byte | 93.3 bytes and 0.48 objects |
| Per IR node (SL9's corpus unit) | about 23.5 KB, as SL9 found for the whole `Lower` |
| GC share of the lowering | (68,908 M at `GOGC=100` − 45,313 M at `GOGC=off`) / 68,908 M = **34.2 %** |
| Kept live at the end, the lowerer's own fields | 104 MB summed over 53 lowerings (about 2 MB each), against the checker's 710 MB that it holds and does not add to |

## By structure

| Structure | Bytes | % bytes | Objects | % objects |
| --- | ---: | ---: | ---: | ---: |
| map (make, insert, growth) | 488.4 M | 47.8 % | 654,816 | 12.3 % |
| go/format (gofmt of the output) | 152.4 M | 14.9 % | 2,063,732 | 38.9 % |
| slice (make, append growth) | 126.1 M | 12.3 % | 1,446,498 | 27.2 % |
| text buffer growth | 122.2 M | 12.0 % | 91,789 | 1.7 % |
| string (concatenation, conversion) | 85.5 M | 8.4 % | 418,849 | 7.9 % |
| struct or value (`newobject`) | 26.1 M | 2.6 % | 214,920 | 4.0 % |
| fmt | 8.5 M | 0.8 % | 62,680 | 1.2 % |
| the rest | 12.9 M | 1.3 % | 354,000 | 6.7 % |

## By owner

| Owner (file: function) | Bytes | % bytes | Objects | % objects | What it is |
| --- | ---: | ---: | ---: | ---: | --- |
| `values.go`: `(*valueWalk).walk` | 283.5 M | 27.7 % | 646,975 | 12.2 % | the value analysis: maps keyed by every syntax node of each body (`pre`, `post`, `parent`, `frameOf`) |
| `module.go`: `(*lowerer).program` | 147.8 M | 14.5 % | 1,657,804 | 31.2 % | almost all of it is `format.Source` over the whole emitted file (gofmt) |
| `sources.go`: `(*lowerer).locate` | 133.2 M | 13.0 % | 6,185 | 0.1 % | `stdSrc`: a map of **every std syntax node** to its file, built once per lowering to place a std node's position (882,843 entries kept live) |
| `module.go`: `anchorLines` | 79.2 M | 7.7 % | 294,713 | 5.6 % | a pass over the output text that repeats `//line` directives |
| `forms.go`: `renameTail` | 64.9 M | 6.3 % | 127,363 | 2.4 % | a `go/scanner` token slice of an emitted body's text, to rename a result |
| `module.go`: `Runtime` | 39.8 M | 3.9 % | 24,153 | 0.5 % | the runtime module's files |
| `body.go`: `(*lowerer).emitBody` | 27.4 M | 2.7 % | 41,003 | 0.8 % | body text |
| `values.go`: `children` | 24.4 M | 2.4 % | 915,971 | 17.3 % | a fresh slice of each node's children, on every visit |
| `canon.go`: `canonicalize` | 24.0 M | 2.3 % | 2,538 | 0.0 % | the canonical unit's maps |
| every other function | 198.0 M | 19.4 % | 1.59 M | 30.0 % | each below 1.3 % |

By file: `values.go` 32.4 %, `module.go` 27.5 %, `sources.go` 13.0 %, `forms.go` 6.6 %, `decls.go` 3.8 %, `body.go` 2.5 %, `canon.go` 2.4 %, `inline.go` 2.3 %; the other 71 files 9.5 %.

## Kept live at the end of a lowering

| Field | Bytes (summed over 53) | Entries |
| --- | ---: | ---: |
| `vals` (the value facts) | 44.2 M | — |
| `stdSrc` (std node → file) | 41.4 M | 882,843 |
| `canon` | 7.6 M | — |
| `pkgOrder`, `pkgNames` | 5.1 M | 39,542 each |
| `types`, `fresh`, `names`, `funcs` | 6.2 M | 9,039 to 9,427 |
| 25 other fields | 0.8 M | — |

## What this says for S4

Decided: four groups of owners make up 72 % of the lowering's bytes, and none of them is the IR. In order of size, S4's layout work for the lowerer is:

1. The value analysis keyed by node, 30 % (`walk` and `children`): side columns keyed by the tree's row ids (the SL6 tree), with pre and post order as the row order itself (children before parents), and no per-visit child slice.
2. gofmt of the emitted file, 15 % of the bytes and 39 % of the objects: an emitter that writes formatted Go, or the IR path that writes no Go text for the bodies it covers.
3. The std node map, 13 % (`locate`): positions are columns of the std snapshot's own trees, not a map rebuilt per lowering. This one is a fix today, not an S4 question: it is per-process data that is rebuilt per lowering, and it is a follow-up below.
4. Text passes over the output, 14 % (`anchorLines`, `renameTail`): decided when the code is emitted, not by scanning the text again.

— by SL9b's counts; reverse by a census that shows another owner above these after S4's tree lands.

The GC share is 34 %, as SL9 measured on the IR path and as the parse's 40 % (plan §2.1). The same rule holds: no allocation per node.

## Follow-ups (not in this unit)

- `locate` builds `stdSrc` over every std node in each lowering, about 2.5 MB allocated per program. Memoising it per process, or reading positions from the std files directly, removes 13 % of the lowering's bytes. It is a small unit on the lowering lane.
- `children` allocates a slice per visit, 17 % of the objects. An iteration callback gives the same order with no slice.

## Known gaps

- The census is of the source path (`Lower`), which is main's build path. The IR path (`LowerIR`) allocates the same within 1 % (SL9).
- A function's charge is its own allocations and those of the std and runtime frames below it. A helper in `lower` that several owners call is its own owner.
- The retained walk sees the lowerer's fields at its end. A pass's own state that is freed before the end (`valueWalk`, `funcCtx`) is counted only in the allocation table.
