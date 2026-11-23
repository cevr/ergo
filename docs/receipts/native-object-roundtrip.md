# Receipt: NATIVE-OBJECT-ROUNDTRIP (route C, layer four), round 1

Unit: research NATIVE-TOOLCHAIN, section NATIVE-OBJECT-ROUNDTRIP. Pin: Go go1.27.4, linux/amd64. Branch `native-object-roundtrip`.

## What exists

An Ergo producer (`compiler/nativeobj`) that turns one checked Ergo package into a Go package archive with both members, written by Ergo code alone:

- `__.PKGDEF`: the stock header line (read from a stock archive and checked against the pin, `CheckHeader`), then unified export data (pkgbits V5, no sync markers): package, file position base, types (int, pointers, func types), and each exported function with its extension record: its SymIdx in the object, ABIInternal, an escape note per parameter that says it escapes, and no inline body. The private root says whether the package has an inittask. The fingerprint is the first 8 bytes of the SHA-256 of the export data.
- `_go_.o`: a goobj object (`\x00go120ld`) carrying the same fingerprint. It holds indexed definitions for the functions, the variables (SNOPTRBSS) and the inittask (SNOPTRDATA, with R_ADDR to each init function and R_INITORDER to each imported package that has an init). Each function has a FuncInfo, args and locals stack maps, pcsp, pcfile, pcline and two pcdata tables (UnsafePoint, StackMapIndex). Runtime and foreign symbols are referenced by name, each with its ABI.
- Machine code: amd64 ABIInternal. The prologue compares SP with g.stackguard0 and branches to a morestack path, which spills the register arguments to the caller's spill words around `runtime.morestack_noctxt`; that path is an unsafe span at stack-map index -1. The frame is set up with PUSHQ BP / MOVQ SP,BP / SUBQ. Every value lives in a frame slot. A closure call puts the context in RDX and calls 0(RDX). A heap pointer store goes through the write barrier (`CMPL runtime.writeBarrier`, `CALL runtime.gcWriteBarrier2`, buffer pair), with the whole sequence one unsafe span. Each call gets its own stack map: the pointer slots live across it, by backward dataflow.

The checked input is a small fixture generator (`fixture.ergo`, `Tiny`) of the hand-off's shape. NATIVE-TYPED-HANDOFF's frozen wire replaces it.

## Laws (in process, `ergo check` / `ergo test ./nativeobj`)

| Law | Holds |
| --- | --- |
| VarintsRoundTrip, TablesRoundTrip, SlotsAndBitsAgree | primitives, pcvalue tables, slot-to-bit layout |
| LivenessAgrees | the lowering's dataflow liveness equals an independent per-slot path model on drawn functions with loops and branches |
| Design.Safe / MissingMap.Breaks / MovedAndReturned.Exists | the planner's stack-move model (Quint `native_object_r2`, ported to Ergo machines under NO QUINT). Design's Marked bit is read from the bitmap the producer emits for the fixture's Anchor, so an omitted entry breaks Safe. MissingMap shows the model catches the omission, and Exists shows Safe is not vacuous |
| ArchiveMembers, FingerprintsAgree, ArgsInABIRegisters, SafePointsIndexed, LiveSlotsMarked, PointerStoresBarriered, RelocsNameTargets, InitTaskOnce | the archive, read back by independent decoders (ar, export data, object, an x86 decoder of the emitted forms), holds each contract against the checked input (`verify.ergo`) |

Mutants: `compiler/nativeobj/testdata/mutants.json`, 8 rows (the spec's 8), each with its killing law. All are marked not-run-crunch (CRUNCH rule 2).

## Round trip (`ergo run ./cmd/nativeobj -- verify --seed 17`, from `compiler/`)

1. The stock compiler builds `testdata/dep`. Its archive supplies the header line, dep's fingerprint and its has-init bit (read by the same decoders).
2. The producer writes `tiny.a`, and `nativeobj.Verify` must report nothing.
3. The stock compiler builds `testdata/main` against `tiny.a`, and the stock linker links it. The same main is built again against the stock reference `testdata/tinyref`, compiled under the same import path with the seed's constants.
4. Both programs run on the stock runtime with `GODEBUG=gccheckmark=1`, a 300 s hang deadline and an 8 GiB RLIMIT_AS. Their outputs must be equal.

The program exercises:

| Function | What it shows |
| --- | --- |
| Diff | argument order |
| Twice | an ABIInternal call into a stock package |
| Apply | a Go closure call |
| Hold | a heap pointer kept only by our frame across a callback that runs GC; a weak pointer would clear if the slot were unmarked |
| Anchor | a pointer into our own frame, moved when the callback grows the stack |
| Publish | pointer stores under concurrent GC with checkmark |
| Spin | a call-free loop that only async preemption can stop for STW GC |
| InitRuns and dep.Inits | init runs once, in import order |

Results are recorded in the unit's `gate-1.log` (home disk, unit folder).

## Gaps (follow-up rows)

- No register allocation, and no frameless leaf beyond the empty function. Speed is not this unit's claim.
- Exported named Ergo types (type descriptors, `type:` symbols) and generics are not produced. The fixture uses predeclared int, pointer and func types only.
- No DWARF and no inline bodies. Every pointer parameter is reported as escaping.
- Symbol tables in goobj and export are keyed by rendered names (Map[string,int]). Under the DATA-ORIENTED rule they become dense ids in the next unit that touches them.
