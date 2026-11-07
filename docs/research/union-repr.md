# Union representation in Go: measurements and a verdict per form

Status: research note (wf10 batch 10, unit M2, 2026-11-07). It changes no compiler or runtime code. The spec changes only when a follow-up unit lands ([types.md](../spec/types.md#unions), Unions 6).

Question: a closed Ergo union lowers today to a sealed Go interface (form A). Is a tag plus its payload (an integer switch, no interface box per value) faster and smaller, and for which shapes? This note measures seven forms on the ten questions of the plan (wf10 `match-plan/plan.md` §2.4) and gives a go or no-go per form by the plan's criterion (§2.5).

Labels: **measured** (a number from a run in [`union-repr-bench/results/`](union-repr-bench/results), or a fact read in code), **inferred** (follows from measured facts), **guess** (not checked). Every number in a table is measured unless the row says otherwise.

## Summary

| Form | What | Verdict | Main evidence |
| --- | --- | --- | --- |
| A | Sealed interface, a struct per variant (today) | Stays: the fallback, and the exported Go view | Baseline |
| B | Enum: all variants empty, `type U uint8` | **Go** | Dispatch −72% (5 variants); step text −52% |
| C | Option shape: `struct{ v T; ok bool }` | **Go** (X1) | 0.49 → 0 allocations per lookup when the Option leaves the function; Cut −62% |
| D | Flat typed slots: `struct{ tag uint8; slots }` | **Go** when the struct is at most 32 bytes and 4 fields; else no-go for now | Breaker 0.63 → 0 allocations per transition, −38%; above 32 bytes a pass-through costs +16 to +19 ns |
| Dx | D, two subjects as one combined-index switch | **No-go** (R4 dropped) | Slower than nested D in every case: +178% (3×4), 2.6× nested D (6×6) |
| E | Tag + boxed payload `struct{ tag uint8; p any }` | **No-go** | Keeps every allocation; step text +38% |
| F | Interface + tag method | **No-go** | Breaker +25%; small dispatch +52% to +57%; step text +82% |
| G | Hybrid by a fixed shape rule (B, C, D, else A) | **Go** (the rule R1 to R3 build) | The sum of B, C and D |

The gain of a tagged form is the allocation, not the dispatch (measured): Go's type switch is already a compare or a jump table, and an integer switch beats it by 20% or more only on enums and on nested two-subject matches.

## Method

- Module: [`docs/research/union-repr-bench`](union-repr-bench), its own Go module (so the repo's `./...` and the gate never reach it). Hand-written Go per form for each question, generated where the shapes repeat (`go run ./gen`; `TestGeneratedFilesCurrent` checks the committed files).
- Contract (`contract_test.go`): each form computes the same results as form A on the generated domain (every Breaker state × signal, 303 tidewalk move lists, 1,024 seeded values per dispatch shape, 10,000 GC values over three rounds of churn, Option and Cut over 1,024 keys); every form D value keeps its inactive slots zero (`wellFormed` on every value the tests build); equality on D agrees with A on every pair (question 7). Eight mutants of the forms and the generator, each red under the contract: [`results/mutants.txt`](union-repr-bench/results/mutants.txt).
- Runs: `run.sh` under the shared `gate.lock`, `GOMAXPROCS=4`, `-count 10`, `benchstat -col /form` (form A is the base column; `p` is the Mann-Whitney U test of benchstat). Each results folder holds `env.txt` (toolchain, CPU, `uptime` before and after), `loads.txt` (the load every 30 s), the raw `count10.txt`, `benchstat.txt`, `size.txt`, `funcsizes.txt`, `escape.txt` (`-gcflags=-m`), `jumptables.txt` (`go tool objdump`) and `gctrace.txt`.
- Host (measured): go1.27.0 linux/amd64, AMD EPYC 9554P, 16 CPUs visible, a shared host. Load (1-minute average) during the runs: 2.5 to 7.7 (median 5.3) in run 1, 3.7 to 5.5 in run 2, 2.3 to 3.5 in run 3, 2.3 to 6.4 in run 4. The `±` of each row is benchstat's 95% interval; rows with a wide interval are flagged.

| Run | Folder | What |
| --- | --- | --- |
| 1 | `results/20261107T222547Z` | All benchmarks. Its `Copy*` rows used a `[k]int64` field and are superseded by run 2; its `GCCycle` rows lack the heap metrics (fixed in run 4). |
| 2 | `results/20261107T230952Z` | `CopyPass`, `CopyNext` (scalar fields), `Narrow`, `DispatchCyclic` |
| 3 | `results/20261107T232835Z` | `Enum` (form B) |
| 4 | `results/20261107T232923Z` | `GCCycle` with heap objects and bytes |

Every measured function is `//go:noinline`, so each form pays one call, as a lowered Ergo function does when Go does not inline it. The Option functions are not marked, because inlining decides whether form A allocates (question 10).

## 1. Allocations per transition

Breaker (the pure step of `check/testdata/fixtures/positive/machine-step-total.ergo`; `(Open, Retry)` enters `Closed{0}`) and a hand port of `examples/tidewalk-replay/walk`. One op is `apply(s, step(s, e))`; `Decision[S, O]` is a union in each form. Run 1.

| Benchmark | Form | ns/op | vs A | allocs per transition | B per transition |
| --- | --- | --- | --- | --- | --- |
| Breaker | A | 22.20 ± 6% | | 0.630 | 10.08 |
| Breaker | D | 13.70 ± 5% | −38.3% (p=0.000) | 0 | 0 |
| Breaker | Dx | 13.60 ± 5% | −38.7% (p=0.000) | 0 | 0 |
| Breaker | E | 23.18 ± 11% | ~ (p=0.481) | 0.630 | 14.11 |
| Breaker | F | 27.77 ± 7% | +25.1% (p=0.000) | 0.630 | 10.08 |

| Benchmark (3 replays, 38 moves) | Form | µs/op | vs A | allocs per move | B per move |
| --- | --- | --- | --- | --- | --- |
| Tidewalk | A | 5.468 ± 6% | | 4.421 | 142.7 |
| Tidewalk | G (Move as B; Game, Event, Decision as D) | 4.473 ± 3% | −18.2% (p=0.000) | 1.316 | 56.4 |

- In form A an `Enter{Open{e}}` boxes twice (the `Open` into `State`, the `Enter` into `Decision`), a `Stay{Closed{n}}` once: Go keeps a static copy of the integers 0 to 255 (`runtime.staticuint64s`), so `Closed{n}` with a small `n` does not allocate (measured: `escape.txt` shows the box; the allocation count shows it is not taken). An empty variant (`Ignore{}`) never allocates (measured).
- Tidewalk G keeps 1.316 allocations per move: the `[]Event` slice that `Decide` returns and the shell string (inferred: both are in A too, and they are the whole of G's count).

## 2. Dispatch time

One subject of 2 to 16 variants, each with an `int` field; two subjects of 3×4 and 6×6. Inputs: 1,024 seeded values in uniform random order (a branch predictor cannot learn them). Run 1.

| Shape | A ns | D | Dx | E | F |
| --- | --- | --- | --- | --- | --- |
| 1×2 | 3.050 ± 6% | ~ (p=0.143) | | +7.7% (p=0.035) | +57.1% |
| 1×4 | 3.496 ± 12% | −11.9% | | ~ (p=0.247) | +52.1% |
| 1×8 | 11.50 ± 4% | −9.4% | | −7.1% | −11.4% (p=0.003) |
| 1×16 | 12.41 ± 3% | −16.1% | | −12.3% | −4.1% (p=0.014) |
| 3×4 | 4.073 ± 5% | +6.5% (p=0.029) | +178.3% | +9.9% (p=0.035) | +157.9% |
| 6×6 | 17.15 ± 3% | **−73.7%** | −32.8% | −72.1% | −26.7% |

The same shapes with the variants in a fixed cycle, which a predictor learns (run 2), and an enum of 5 empty variants, tidewalk's `Move` (run 3):

| Shape | A ns | D | Dx | E | F | B |
| --- | --- | --- | --- | --- | --- | --- |
| 1×16, cyclic | 7.279 ± 6% | **+40.3%** | | −24.1% | ~ (p=0.089) | |
| 6×6, cyclic | 5.378 ± 7% | −37.7% | +54.6% | | | |
| 1×5 enum, random | 11.35 ± 5% | | | | | **−72.0%** |

p = 0.000 for every row without a p.

- Go lowers both forms to jump tables when a switch is large enough (measured in `jumptables.txt`: an indirect `JMP 0(AX)(CX*8)` in `dispatchU8D`, `dispatchU16D`, the combined-index `dispatchP3x4Dx` and `dispatchP6x6Dx`, and also in the type switch of `dispatchU16A` and the nested type switches of `dispatchP6x6A`). A type switch on a sealed interface is therefore not a linear chain of compares.
- With a random subject, an indirect jump is mispredicted on most calls, and a short tree of compares is cheaper (inferred from 3×4: Dx is 2.8× nested D). The combined index makes one 12- or 36-way jump out of two small trees, so it loses in every measured case. R4 product-dispatch is dropped.
- A one-subject integer switch is not reliably faster than a type switch: −16% random, +40% cyclic at 16 variants. The case where D wins by 20% or more is the nested two-subject match (6×6: −74% random, −38% cyclic), where form A tests the second subject's itab inside each arm of the first (inferred).
- B on an enum is −72%: form A's type switch loads the type word and compares it per case; B compares one byte (inferred).

## 3. Code size

Generated programs (`size/<form>`), one union of 20 variants with fields of five shapes (`{}`, `{A int}`, `{S string}`, `{A int, B int}`, `{S string, A int}`), a `step` that matches the union and builds the next variant, and a `describe` that matches it again; plus an enum of 20 empty variants in forms A and B. Text of each function from `go tool nm -size`; the stripped binary is built with `-ldflags='-s -w'`; sections from its ELF headers. Run 1 (`size.txt`; the same numbers in runs 2 to 4).

| Program | step | describe | variant methods | .text | .rodata | stripped binary |
| --- | --- | --- | --- | --- | --- | --- |
| A | 1,305 | 2,024 | 0 | 629,809 | 65,154 | 1,540,256 |
| D | 1,104 (−15%) | 1,562 (−23%) | 0 | 629,265 (−544) | 63,426 (−1,728) | 1,532,064 (−8,192) |
| E | 1,797 (+38%) | 2,393 (+18%) | 0 | 630,673 (+864) | 63,426 | 1,536,160 |
| F | 2,375 (+82%) | 2,551 (+26%) | 560 | 632,689 (+2,880) | 63,426 | 1,548,448 (+8,192) |
| A, enum | 1,390 | 788 | 0 | 628,593 | 65,090 | 1,540,256 |
| B | 662 (−52%) | 88 (−89%) | 0 | 627,153 (−1,440) | 63,522 (−1,568) | 1,532,064 (−8,192) |

- The marker methods of form A cost no text: nothing calls them, so the linker drops them (measured: 0 bytes; inferred cause). Form A's extra `.rodata` (1.6 to 1.7 KB for 20 variants) is its itabs and the type data they need (inferred).
- The binary's file size moves in 8 KB steps (page alignment, inferred), so `.text` and `.rodata` are the finer measure.

Text of the measured functions in the benchmark binary (`funcsizes.txt`, bytes):

| Path | A | Other form | Change |
| --- | --- | --- | --- |
| Breaker `step` + `apply` | 542 + 229 = 771 | D 501 + 151 = 652 | −15% |
| Breaker `step` + `apply` | 771 | E 525 + 243 = 768; F 536 + 261 = 797 | −0.4%; +3% |
| GC churn `nextNP`, `nextWP` | 212, 232 | D 118, 124 | −44%, −47% |
| Enum `target` | 216 | B 134 | −38% |
| Option `lookup` + its caller | 137 + 314 = 451 | C 89 + 217 = 306 | −32% |
| Option `cut` + its caller | 231 + 305 = 536 | C 298 + 280 = 578 | +7.8% |
| Tidewalk `record`, `evolveWalk`, `tideStep`, `tideApply`, `play` | 5,181 | G 5,827 | +12.5% (G's `play` also takes the contract test's check hook, so part of this is the harness; inferred) |
| `narrow` (64-byte variant) | 198 | D 425; Ds 155 | +115%; −22% |

A struct above 32 bytes is copied through memory (question 5), so its functions grow: tidewalk's `Game` (64 bytes in D) is the case above (+12.5%), and `tideApply` alone grows from 229 to 486 bytes.

## 4. GC

A slice of 1,000,000 union values stays live. `GCCycle`: one op is one `runtime.GC()` with the slice live (run 4). `GCChurn`: one op replaces one element with the next value, as a machine replaces its state (run 1). NP has no pointer in the payload (`NP1{A int} | NP2{A, B int}`); WP has a string (`WP1{S string} | WP2{S string, A int}`).

| Payload | Form | GC cycle | heap objects | heap bytes |
| --- | --- | --- | --- | --- |
| NP | A | 5,809 µs ± 17% | 750,317 | 42.13 MiB |
| NP | D | 129.9 µs ± 9% (−97.8%, p=0.000) | 593 | 38.34 MiB |
| NP | E | 5,503 µs ± 12% (~, p=0.089) | 750,492 | 49.78 MiB |
| WP | A | 7.900 ms ± 36% | 1,000,593 | 49.78 MiB |
| WP | D | 3.549 ms ± 18% (−55.1%, p=0.000) | 598 | 45.97 MiB |
| WP | E | 7.074 ms ± 12% (−10.5%, p=0.029) | 1,000,601 | 57.41 MiB |

| Payload | Form | churn ns/op | allocs/op | GCs per million ops | GC pause per op |
| --- | --- | --- | --- | --- | --- |
| NP | A | 24.95 ± 5% | 1 | 0.287 | 0.023 ns ± 173% |
| NP | D | 9.62 ± 11% (−61.4%) | 0 | 0 | 0 |
| NP | E | 25.48 ± 7% (~, p=0.631) | 1 | 0.232 | 0.021 ns |
| WP | A | 33.33 ± 6% | 1 | 0.411 | 0.028 ns ± 49% |
| WP | D | 8.96 ± 5% (−73.1%) | 0 | 0 | 0 |
| WP | E | 31.16 ± 3% (−6.5%, p=0.003) | 1 | 0.358 | 0.021 ns |

- NP in form A has 750,317 objects for 1,000,000 values: half the values are 8-byte `NP1` boxes, which Go's tiny allocator packs two to a 16-byte block (inferred).
- D's NP slice has no pointer, so the GC does not scan it: the cycle is the runtime's fixed cost. D's WP slice holds strings, so the GC scans it, but as one object with no box to visit (−55%).
- GC CPU (inferred): GCs per op × the cycle cost with the live set is the GC work per op: NP A 0.287 × 5.81 ms ≈ 1.7 ns per op (≈ 7% of 24.95 ns); WP A 0.411 × 7.90 ms ≈ 3.2 ns (≈ 10%); D 0. The stop-the-world pause is small in every form (0.02 ns per op): the cost is concurrent mark work, not pauses. `gctrace.txt` holds the raw `GODEBUG=gctrace=1` lines.

## 5. Copy size break-even

A union `Big{F0 … Fk-1 int64} | Small{}`, payloads of 16 to 128 bytes. `CopyPass`: a call that takes and returns the value, in a loop that carries it (a state passed on unchanged). `CopyNext`: the call narrows, changes one field and builds a new value (a state replaced). Run 2.

| Payload | D struct | A pass ns | D pass | A next ns (allocs) | D next (allocs 0) |
| --- | --- | --- | --- | --- | --- |
| 16 B | 24 B, 3 fields | 1.623 ± 6% | ~ (p=0.073) | 18.06 ± 16% (1) | 2.570 (−85.8%) |
| 32 B | 40 B, 5 fields | 1.591 ± 8% | 17.43 (+996%) | 22.00 ± 20% (1) | 19.32 (−12.2%) |
| 64 B | 72 B, 9 fields | 1.686 ± 16% | 20.31 (+1104%) | 27.83 ± 41% (1) | 21.97 (−21.1%, p=0.002) |
| 128 B | 136 B, 17 fields | 1.610 ± 7% | 20.20 (+1155%) | 53.99 ± 12% (1) | 25.68 (−52.4%) |

- The step at 32 bytes is Go's SSA limit (measured in the toolchain source: `cmd/compile/internal/ssa/value.go`, `CanSSA`): a struct of more than `4 × PtrSize` bytes or more than 4 fields (`MaxStruct = 4`), or with an array of more than one element, is not kept in registers between operations. Such a value goes through the stack, and a loop that carries it pays about 16 to 19 ns per pass on this host (inferred cause: a store-to-load forwarding stall on the copy; not measured with performance counters).
- Run 1 used one `[k]int64` field: then even the 24-byte D value paid 20.7 ns per pass (+1275%), because an array of two elements is never SSA (measured). Slots must be scalar fields.
- A never pays this: it passes two words. It pays an allocation for every new value instead.

**Threshold.** D values of at most 32 bytes and 4 Go fields (the tag counts as one field) gain in every measurement. Above it the result depends on use: building values gains (−12% to −52% here, −38% for Breaker's 56-byte `Decision`, −18% for tidewalk), passing a carried value unchanged loses 16 to 19 ns, and code grows (question 3). So the rule for D is the SSA limit: at most 32 bytes and 4 fields. The 33 to 128 byte band is a follow-up measurement on real programs (R3b), because the machine's `Decision` is in it.

## 6. Narrowing cost

A match arm binds a 64-byte variant `V64{A … H int64}` and passes the record to a function; the other variant `W64{P, Q int64}` shares V64's first two slots in D. Ds keeps each variant's record whole in its own slot (Borgo's layout). Run 2.

| Form | ns/op | vs A |
| --- | --- | --- |
| A (copy out of the box) | 5.335 ± 18% | |
| D (build the record from slots; 72-byte struct) | 8.712 ± 15% | +63.3% (p=0.000) |
| Ds (a record slot per variant; 88-byte struct) | 11.53 ± 4% | +116.0% (p=0.000) |

All three copy 64 bytes into the callee's arguments. D and Ds also pass the wide union itself by value (72 and 88 bytes, above the SSA limit), which is the extra cost (inferred). Below the threshold, narrowing a D value is reading its scalar slots, and the Breaker and churn rows of questions 1 and 4 include it.

## 7. Equality and map keys

- Form A: Go's `==` on two interface values compares the dynamic types, then calls the type's equality. It panics at run time on an incomparable dynamic type; Ergo's checker refuses `==` on a union unless every variant compares ([types.md](../spec/types.md#operators), Operators 1), so the panic is unreachable (measured: the rule in the spec).
- Form D: Go's `==` on the struct compares the tag and every slot. **Invariant:** the tag is one of 1 … n, and every slot that the active variant does not use holds its Go zero value. Then two D values are `==` exactly when they are the same variant with equal fields, which is A's `==` (inferred; the property below checks it). The same holds for map keys: Go hashes every slot, so equal values hash alike under the invariant. A union with a slice variant is not comparable in either form: Go refuses `==` on the D struct at compile time, and Ergo's checker refuses it first.
- Who keeps it: only generated constructors build a D value, a variant conversion (`ConvVariant`) writes the variant's fields into their slots and nothing else, and no Ergo code writes a slot of an existing union value (Ergo values are immutable; `let mut` replaces the whole value).
- Property (measured green): `TestEqualityAgrees` compares `==` on A and D over every pair of Breaker states (including `Open{""}`, whose slots equal a `Closed{0}`'s except the tag) and 20,000 pairs of generated NP and WP values; `wellFormed` checks the invariant on every value the contract tests build. `TestDirtySlotBreaksEquality` shows a value with a dirty slot is the same Ergo value and not `==`. Mutants m2 (a constructor writes an inactive slot), m4 (a step keeps an old field in an inactive slot) and m6 (a `None` of form C keeps a value) are red.
- Cost: `==` on Breaker states, A 7.423 ns ± 16%, D 5.216 ns ± 30% (−29.7%, p=0.000; run 1).

## 8. Copy at a task boundary (Gap 45)

A union `Bag{Xs []int} | Tag{N int}`; the clone of a `Bag` with 16 elements. Run 1.

| Form | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| A: type switch, clone the slice, box the new `Bag` | 70.61 ± 15% | 152 | 2 |
| D: clone each slice slot, no switch | 61.05 ± 13% (~, p=0.063) | 128 | 1 |

The clone plan per static type ([README](../spec/README.md) Gap 45): form A needs a type switch per value to find the slices, and each cloned variant is boxed again. Form D clones its slice and map slots in place; an inactive slot is nil by the invariant, so it costs one compare. One allocation less per value; the time difference is within noise here. `TestCloneFormsAgree` checks that a clone shares no storage in either form (mutant m3 is red).

## 9. Go interop and every place the lowering reads the form

**Go callers today: none outside the generated module** (measured). `ergo build` writes one Go `main` package per program (the tidewalk build's work directory holds one `main.go` beside the runtime), and `examples/go-interop` imports only `runtime/ergo`, not generated code. Owner decision q22 (2026-11-06, wf10 `plan.md`): the union Go form may change; an exported union keeps a stable Go view at the export boundary.

What a Go caller would need with B, C or D: the struct's fields are unexported, so a caller needs constructors (`FromClosed(Closed) State`), narrowing accessors (`(State) Closed() (Closed, bool)`) and a `Kind()`. That is a larger API than today's, where Go code type-switches on the exported variant structs and builds them directly. **Recommendation (inferred):** keep form A as the exported view. When an exported signature exists (a host-driven entry, #240, or a future library build), the lowering emits the A view for it and converts at the edge (one box per crossing value), and the internal form stays the per-shape one. No such boundary exists yet, so R1 to R3 change only internal code.

Every place the lowering reads the union form (measured by reading `internal/sketch/lower` at the base):

| Place | Today (form A) | With a tagged form |
| --- | --- | --- |
| `decls.go` `kindUnion`, `unionMarker` | sealed interface, a marker method per variant | the tagged struct, tag constants, constructors |
| `types.go` `goType`, `typeKey`; `body.go` `isInterface` | the union's Go type is an interface | a struct; `isInterface` false, so the type-switch path no longer applies |
| `expr.go` `convert`, `ConvVariant` | no code: Go assigns the struct to the interface | a constructor call that writes the variant's slots |
| `expr.go` `convert`, `ConvRow` | `err.(U)`: a row value (a Go `error`) into a union | a type switch over the error variants; simpler: a union whose variants are error types keeps form A |
| `body.go` `typeMatch`, `valueMatch`; `match.go` `patternMatch` (`ergoIs`, `.(T)`); M1's `matchtree.go` | type switch, type tests, assertions | `switch u.tag` and slot reads; M1's tree emits a tag switch on a tagged column |
| `expr.go` `patternCatch` | type tests on a Go `error` | unchanged if error-type unions keep A |
| `text.go` text writer (interpolation, error text) | `switch x := v.(type)` per variant | `switch v.tag`, then the variant's text from its slots |
| `literal.go` `litExpr` (the `ergo test` argument printer, [runtime.md](../spec/runtime.md) Tests 5) | type switch per variant | tag switch; the printed literal is the same |
| `tests.go` `genExpr` (property value generator) | builds a variant and lets Go convert it | builds through the constructor |
| `==`, `!=`, map keys | Go `==` on interfaces | Go `==` on the struct, under the zero-slot invariant (question 7) |
| `machine.go` plan `Step` returns `Decision[S, O]` | an interface | a struct (generic slots: `DecisionD[S, O]` is measured in question 1) |
| Machine snapshot copy, task-boundary clone (Gap 45; not lowered yet) | type switch per value | clone per slot (question 8) |

Two more facts the rule must respect (inferred from [types.md](../spec/types.md#unions) Unions 1 and 2): a record can be a variant of several unions, so each union's D layout is its own and a conversion copies the fields into that union's slots; and a recursive union (a variant field of its own union type, directly or through a record) cannot be a flat struct, so it keeps form A.

## 10. X1: `Option[T]` as the first case

Map lookup over 1,024 keys, half present (run 1). `Get` is the body the lowering writes for the prelude's `m.Get(k)`; Cut is std `strings.Cut` over `Index` and slicing.

| Benchmark | A ns (allocs per call) | C | Go comma-ok |
| --- | --- | --- | --- |
| `Get` inlined, `int` ≥ 256 | 9.182 ± 11% (0) | 9.095 (~, p=0.353) | 9.022 (~, p=0.325) |
| `Get` inlined, `int` < 256 | 10.03 ± 13% (0) | 10.60 (~, p=0.393) | 10.26 (~) |
| `Get` inlined, `string` | 10.61 ± 29% (0) | 10.06 (~, p=0.436) | 9.286 (−12.4%, p=0.002) |
| lookup through a call (the Option leaves the function) | 18.40 ± 8% (0.488) | 11.43 (−37.9%, 0) | 10.40 (−43.5%, 0) |
| `Cut` | 22.33 ± 8% (0.481) | 8.503 (−61.9%, 0) | 8.383 (−62.5%, 0) |
| `Cut` through a call | 24.13 ± 8% (0.481) | 16.31 (−32.4%, 0) | 9.459 (−60.8%, 0) |

p = 0.000 where none is given.

- When Go inlines `Get` and the Option stays in the caller, escape analysis keeps form A's box on the stack: no allocation, and A equals C (measured: `escape.txt`, "Some[go.shape.int]{...} does not escape" in `sumGetIntA`). The expected allocation per `Some` (plan §2.4 question 10) appears only when the Option leaves a function that Go does not inline: then form A allocates once per `Some` (0.49 per lookup = one per hit), and C allocates nothing.
- Std `Cut` is not inlined (measured: `escape.txt`, "Some[Halves]{...} escapes to heap" in `cutA`), so every `Some[Halves]` allocates 32 bytes in form A.
- C still trails Go's three results through a non-inlined call (16.3 against 9.5 ns): the 40-byte `OptionC[Halves]` is above the SSA limit (question 5) and goes through memory (inferred). Inlined, C equals Go.

## Verdicts by the criterion (plan §2.5)

Criterion: go when (1) the form removes at least one allocation per operation or cuts dispatch time by 20% or more, (2) the text of the benchmark programs grows by 10% or less, (3) no guarantee is lost, and (4) the change fits behind one `unionRepr` seam.

| Form | (1) | (2) | (3) | (4) | Verdict |
| --- | --- | --- | --- | --- | --- |
| B | dispatch −72% (no allocation to remove: empty variants never allocate in A) | step −52%, `.text` −1,440 B | `==` on a byte; the text and test printers switch on the tag; no slot, so no invariant | yes | **Go** |
| C | 0.49 → 0 allocations per lookup through a call, Cut 0.48 → 0; −32% to −62% | lookup path −32%, Cut path +7.8% | None is the zero value (checked, mutant m6) | yes | **Go** |
| D ≤ 32 B, ≤ 4 fields | Breaker 0.63 → 0 per transition (−38%); churn 1 → 0 (−61%, −73%); 6×6 dispatch −74% | Breaker step + apply −15%; 20-variant step −15% | equality and map keys by the zero-slot invariant (property, mutants m2, m4); clone per slot (fewer allocations) | yes | **Go** |
| D > 32 B | removes allocations (tidewalk −70%, Copy −100%) | tidewalk +12.5%, `narrow` +115% | as above | yes | **No-go for now** (R3b measures the band) |
| Dx | none: +178% (3×4), +55% cyclic 6×6 against A, and slower than nested D everywhere | — | — | — | **No-go** |
| E | none removed (same allocations, +40% bytes) | step +38% | — | — | **No-go** |
| F | none removed; Breaker +25% | step +82% | — | — | **No-go** |
| G | as B, C and D | as B, C and D | as B, C and D | yes | **Go**: the rule below |

**The rule (G).** Per union, by its shape, in one function the seam owns:

1. All variants empty → B.
2. Exactly one variant with fields and one empty variant → C (any `T`; measured gains at 40 bytes for `Option[Halves]`).
3. Otherwise, when the union is not recursive, no variant is an error type, and the D struct (a `uint8` tag plus one scalar slot per field, a slot shared by fields of the same Go type across variants) is at most 32 bytes and 4 fields → D.
4. Otherwise → A.
5. At an exported Go boundary (none exists yet), the Go view is A, converted at the edge.

The rule leaves the machine's `Decision[S, O]` in form A when `S` is a D-form state (56 bytes for Breaker), although Breaker measured −38% with it in D. Raising the limit for such transient results is R3b's question; the simple rule is the safe first step (decided by "prefer the simpler, reversible option" and NORTH_STAR pillar 7, "never slower than today").

## Prior art

| Language | Representation | Match lowering | What Ergo takes |
| --- | --- | --- | --- |
| OCaml | A constant constructor is an immediate integer; a constructor with arguments is a heap block whose header holds the tag; `[@@unboxed]` removes the box of a one-constructor, one-field type (OCaml manual, "Interfacing C with OCaml", representation of data types) | a switch on the immediate or the header tag, a jump table when dense (Le Fessant and Maranget 2001) | B is OCaml's constant constructors. OCaml keeps the box for payloads because its minor-heap allocation is a pointer bump; Go's is not, so D's gain is larger in Go (inferred). |
| ReScript | A constant constructor compiles to a string; a constructor with a payload to an object with a `TAG` field; `option` compiles to the payload itself, `None` to `undefined`; `@unboxed` variants drop the tag when JavaScript can tell the cases apart (rescript-lang.org, Variant and Null, Undefined and Option) | OCaml's matching compiler, then a JavaScript `switch` on the tag | C is ReScript's `option`; G is ReScript's rule of a representation per type. |
| Gleam | Erlang target: a record is a tuple `{tag, fields…}`, a constant constructor an atom. JavaScript target: a class per constructor | Erlang pattern matching; on JavaScript, decision trees since v1.11 (gleam.run, 2025-06-02) | Gleam leaves the representation to the host and does not unbox `Option` (inferred from the generated forms). |
| Elixir, Erlang | tagged tuples and atoms on the process heap | `v3_kernel` match compilation to `select_val` and `select_tuple_arity`, jump tables or binary search | Per-process heaps make each tuple cheap; Go has one shared heap, so boxes cost GC work (question 4). |
| Rust | an enum is a tag plus its variants' payloads overlapped in one area (size: the largest variant plus the tag); niche optimization: `Option<&T>` and `Option<NonZeroU32>` have no tag | a switch on the discriminant; LLVM makes jump tables | D is the part of Rust's layout that Go's GC allows: Go needs an exact pointer map, so payloads of different Go types cannot overlap without `unsafe` (owner rule 2026-11-06: no `unsafe` in generated user code). D overlaps only fields of the same Go type. |
| Swift | like Rust; multi-payload enums put the tag in spare bits; an `indirect` case is boxed | a switch on the tag | Swift boxes recursive cases, as Ergo keeps A for recursive unions. |
| Go | the idiom is a sealed interface (form A); protobuf-go's `oneof` is an interface with a wrapper struct per case (inferred from the generated code's shape). Proposal #57644 (sum types from general interfaces, open) keeps the interface value and notes that an implementation may store values without boxing; #76920 discusses a non-interface union type whose zero value is the first variant | type switch: compares, or a jump table over type hashes (measured here in `dispatchU16A`) | A stays the exported view, as Go users expect. |
| Borgo (compiles to Go) | a struct with an integer tag and a field per variant field, no sharing (`compiler/src/codegen.rs`, `emit_enum`) and `make_` constructors | a switch on the tag | Borgo's layout is Ds (question 6): it grows with the sum of the variants. D shares same-typed slots to stay smaller. |

Optimizations the others use that Ergo's rule matches: constant constructors as integers (OCaml, ReScript, Rust C-like enums → B), the option without a wrapper (ReScript `option`, Rust niche → C), a per-type choice of representation (ReScript, Swift → G), recursive cases boxed (Swift `indirect`, OCaml blocks → A for recursive unions). One Ergo cannot match: Rust's and Swift's overlap of differently typed payloads, which Go's GC forbids without `unsafe`.

## Follow-up units (plan §2.7, revised by the verdicts)

| Unit | What | Depends on | Red check sketch |
| --- | --- | --- | --- |
| R1 repr-seam + X1 | The `unionRepr` seam (one function chooses B, C, D or A by the rule; every place in question 9 reads it), and form C for the option shape by the rule, not by name. Types.md Unions 6 becomes per-shape. | M1 (the tree emits a tag switch on a C column), batch-9 lane C merged (`decls.go`) | (a) A shape test: `ergo build -work` of a program with `m.Get(k)` returned through a function and of `strings.Cut` emits no `isOption` interface; red at base. (b) The M1 property with a repr switch: a `match` over form C selects the same arm and bindings as form A for every value. (c) `testing.AllocsPerRun` of a lookup through a non-inlined function: 0.5 at base, 0 at head. (d) A None is the zero value (property). |
| R2 enum-repr | Form B for all-empty unions | R1 | Shape test: tidewalk's `Move` lowers to `uint8` constants (red at base); the M1 property over enums; the `ergo test` printer still prints `North`; `==` and map keys agree with base output on the corpus. |
| R3 flat-repr | Form D under the rule (≤ 32 bytes, ≤ 4 fields, non-recursive, no error-type variant), with the zero-slot invariant as a property | R2 | (a) Breaker's step: 0.63 allocations per transition at base, 0 at head (`AllocsPerRun`). (b) A generated property: for generated values, `==` on head output equals `==` on base output, and every value's inactive slots are zero (a generated `wellFormed` per union in test builds). (c) Text and test printers print the same as base on the corpus. |
| R3b flat-band (research) | Measure the 33 to 128 byte band on real programs after R3: Breaker and tidewalk `Decision`, tidewalk `Game`; decide whether transient results (a function's result consumed by a match) may use D above 32 bytes | R3 | Benchmarks of the lowered examples at R3's head with the limit at 32 and at 64 bytes; go only by §2.5. |
| R4 product-dispatch | **Dropped**: the combined-index switch is slower than nested switches in every measured case (question 2) | — | — |
| R5 repr-clone | The task-boundary clone plan per form (Gap 45), when that rule lands: D clones slots, A switches | R3, #211 | A clone of a value with a slice slot shares no storage (mutate the source, the clone is unchanged); allocations 2 → 1 for a `Bag`. |

## Limits

- One host, shared, under load 2.3 to 7.7; numbers with a wide interval (`± 15%` and more) are flagged in the tables. Absolute times will differ on another CPU; the allocation counts and code sizes do not depend on the host (inferred).
- The benchmarks are hand-written Go of each form, not the lowering's output; the forms' code follows what the lowering emits today (form A) and what a tagged lowering would emit (the others). The follow-up units measure the real lowering.
- The cause of the 16 to 19 ns pass cost above the SSA limit is inferred, not measured with performance counters.
- The random-order dispatch rows show mostly mispredicted branches; real programs see more regular input, as the cyclic rows show, and there the one-subject integer switch lost to the type switch.
