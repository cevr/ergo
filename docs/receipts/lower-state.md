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

## IR path (S4)

The sections above measure the source path at `d3bb9dac6`. Since FLIP, `ergo build` lowers through the IR (`LowerIRSplit`). This section is the same census on that path. It is capstone §3a.9's first S4 unit (lane S4): what the IR lowerer holds besides the IR tree, so that S4's self-hosted IR builder has its tables decided before its first port.

### Pins and method

- Head: `main` `fb1d1e011`. Go 1.27.4, linux/amd64, `GOMAXPROCS=4`.
- Corpus: the `cmd/*` package of every module under `examples/`: 111 programs, all lowered, none failed. Each is lowered by `lower.LowerIRSplit(u, false)`, the release build's lowering (the dev profile's `Raw` is not set, so gofmt runs).
- Output: 23.5 MB of Go and 55.1 MB of IR files (JSON). 15,522 bodies are IR and 4,145 bodies fall back to Go text (21 %).
- Allocations and the retained walk are as above, with two changes:
  - Each owner file has the class that `lower-map.md` gives it: **IR builder** (`ir*.go`), **IR-path** (a decider that the builder reads), **shared** (deciders and emitters in one file), **text-only**. A file that is not in the map is **unmapped**.
  - The retained walk charges `ir.out` (the IR tree) first, then each other field of `irState`, then the lowerer's fields.
- Tool: `selfhost/SL9b/tools/` in the run folder (`census.go`, the hook in `ir.go`). The raw output is `selfhost/SL9b/census.txt`. All numbers are counts.

### Totals

| Measure | Value |
| --- | --- |
| Allocated by the 111 lowerings | 7,473 MB in 48.6 M objects, about 67 MB and 437 k objects per program |
| Per output byte (Go and IR) | 95.0 bytes and 0.62 objects (source path at `d3bb9dac6`: 93.3 and 0.48 per Go byte) |
| Kept live at the end: the IR tree (`ir.out`) | 270 MB summed, 2.4 MB per program: 4.9 bytes per byte of IR JSON |
| Kept live at the end: the lowerer's own fields | 327 MB summed, 2.9 MB per program (the IR state's other fields included), against the checker's 71 MB per program that it holds |

### By class

| Class | % bytes | % objects |
| --- | ---: | ---: |
| shared | 32.7 | 35.5 |
| IR-path (deciders) | 29.2 | 22.2 |
| IR builder (`ir*.go`) | 22.6 | 19.7 |
| text-only | 10.5 | 17.0 |
| unmapped (`reach.go`, `pkginst.go`, `layout.go`, `anchor.go` and smaller) | 3.4 | 4.3 |
| test and law lowering | 1.7 | 1.3 |

Only the text-only 10.5 % leaves with RETIRE. S4 ports the other 89.5 % or replaces it.

### By owner (the ten largest)

| Owner (file: function) | % bytes | % objects | What it is | S4 |
| --- | ---: | ---: | --- | --- |
| `values.go`: `newValueWalk`, `walk`, `appendChildren`, `varOf` | 15.1 | 10.4 | the value analysis: maps keyed by `syntax.Node` and `*check.Var` (`nodes`, `frameOf`, `vars`, `handed`), sized per body | side columns on the S2 tree's row ids; children are the rows before the parent, so no child slice |
| `ir_template.go`: `template` | 8.6 | 1.8 | `eval.FoldTemplate` runs up to 24 rounds per candidate call; the slices are the evaluator's frames | the evaluator is S3's; a template is tried once per call site, with the holes decided before the run |
| `pkgsplit_ir.go`: `(*irSplit).files` | 7.8 | 3.0 | each IR function is copied by `json.Marshal` and `json.Unmarshal` before the split renames it, then each package's IR is marshalled | IR rows are values: a copy is a row range; the file is the binary IR (CS10c), written once |
| `pkgsplit.go`: `splitMain` | 6.5 | 15.0 | gofmt and maps over the module's Go text, to cut it into packages | the split is decided on the IR before any Go text is written |
| `module.go`: `program` | 5.3 | 11.9 | gofmt of the declaration file | the declaration file is the last Go text (DECL); its form is decided when it is written |
| `sources.go`: `stdFileOf` | 4.4 | 7.2 | the walk that places a std node in its file, before the `stdSrc` memo hits | positions are columns of the std snapshot's trees |
| `rewrite.go`: `try` | 2.9 | 1.2 | slices per rewrite attempt | |
| `alias.go`: `newFlow` | 2.6 | 0.9 | maps keyed by `*check.Var` per body | side column by local id |
| `ir_forms_inline.go`: `cloneExpr`, `cloneStmt` | 3.4 | 2.2 | a pointer tree copied per inlined call | a row range copied into the caller's pool |
| `unfold.go`: `straightLine` | 2.5 | 0.5 | slices per straight-line block | |

By structure: maps 30.0 % of the bytes, slices 28.0 %, structs (`&irExpr{}`, `&irStmt{}` and other escaping values) 13.8 %, gofmt 8.9 % (25.4 % of the objects), JSON of the IR 7.7 %, text buffers 4.5 %, strings 4.1 %.

### Kept live at the end of an IR lowering

| Field | Bytes (summed over 111) | Entries | S4 layout |
| --- | ---: | ---: | --- |
| `ir.out` (the IR tree of pointers) | 270.2 M | — | rows of (op, a, b) with one extra pool per function (SL9: 23.3 against 264.8 bytes per node) |
| `vals` (the value facts) | 169.9 M | — | `dense.Side` on the tree's row ids and the checker's symbol ids |
| `canon` | 27.5 M | — | declaration order is the checker's table order |
| `stdSrc` (std node to file) | 26.9 M | 412,772 | a column of the std snapshot |
| `pkgNames`, `pkgOrder` (`pkgKey` to Go name) | 43.2 M | 333,324 each | a `dense.Col` per package id; the name is interned once |
| `types` (`*check.TypeName` to its declaration) | 12.5 M | 46,800 | `dense.Side` by type id |
| `letUses` (`*check.Var` to two counts) | 11.3 M | 393,792 | `dense.Col` by local id |
| `names` (the namer) | 11.0 M | — | an interner per package |
| `ir.inl` (the inliner's marks and frames) | 7.2 M | — | a column by function id |
| `funcs` (`*check.Func` to its declaration) | 5.0 M | 35,602 | `dense.Side` by function id |
| the other fields | 12.4 M | — | maps keyed by `*check.Func`, `*check.Var`, `pkgKey`: each a column by its id |

Every field that is kept live and is larger than 1 MB is keyed by a node, a symbol, a type or a package. `TestCompilerHasNoPointerKeyedMaps` refuses each of these keys in `compiler/`, so each becomes a dense column. None needs a Map.

### What this says for S4

Decided: the self-hosted IR builder holds its state as dense columns from its first unit, in this order of size: (1) the IR as rows with a pool per function (the tree, 2.4 MB per program, and the inliner's clones); (2) the value analysis and the alias flow as side columns on the tree's row ids and the checker's symbol ids, about 18 % of the bytes; (3) the split decided on the IR, so that no Go text is formatted or cut again (gofmt and `splitMain`, about 12 %, and the JSON copy, about 8 %). By: SL9 (rows), this census, and DATA-ORIENTED SELF-HOST (common.md). Reverse by: a census of the first S4 port that shows another owner above these.

### Follow-ups (not in this unit)

- `(*irSplit).files` copies every IR function through `json.Marshal` and `json.Unmarshal` (7.8 % of the bytes, with the per-package marshal). A deep copy of the Go values, or renaming in place after the split is decided, removes the round trip. This is a fix on the Go IR path today (lane IR1 or IR2).
- `template` runs the evaluator again for each widened hole, up to 24 rounds (8.6 %). A count of rounds per call site comes first (REUSE OPENS WITH A COUNT).
- The release build still runs gofmt on the declaration file and on the split's Go text (8.9 % of the bytes, 25 % of the objects).

### Known gaps

- The GC share of the IR lowering is not in this receipt. Its run (`perf stat -e instructions:u`, median of 3, 3 lowerings of each program) stopped before its last leg. It measured: load only 39,423 M at `GOGC=100` and 27,168 M at `GOGC=off`; load and lower 1,526,338 M at `GOGC=100`. The leg "load and lower at `GOGC=off`" is missing, so the share is owed to the batch run.
- 4,145 bodies (21 %) still fall back to Go text. Their emitter's allocations are in the shared and text-only classes. When FLIP removes the fallback, those classes become smaller.
- The class of a file comes from `lower-map.md` at `2e7a7bbdb`. A file added since then is unmapped, and an `ir*.go` file is the IR builder by name.
