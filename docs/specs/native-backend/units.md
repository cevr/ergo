# Ergo Native Backend — Ordered Build Units (`NB-1` to `NB-12`)

Breaks execution of [architecture.md](architecture.md) into seven vertical, parallelizable slices (`NB-1` through `NB-7`), grounded in the existing compiler modules (`compiler/check/`, `compiler/ir/`, `compiler/eval/`, `compiler/bindc/`, `compiler/nativeobj/`, `compiler/driver/`, `compiler/cmd/ergo/`) while freezing Stage-0 Go dependencies (`toolchain/ir/`, `internal/gobind/`, `runtime/v1/`, `runtime/rt/`) until deletion in `NB-7`.

## Dependency Graph

```text
  Existing on main:
    compiler/syntax, compiler/check/*, compiler/ir/*, compiler/eval/*,
    compiler/bindc/*, compiler/nativeobj/amd64.ergo, compiler/driver/*
        |
        +-----------------------------------+
        |                                   |
        v                                   v
  NB-1. compiler/objfmt               NB-2. runtime/native + compiler/nativeabi
  (Direct ELF64/Mach-O64              (16B ErgoHdr Perceus RC, ErgoStr/Slice,
   .o / .a / exe / .so / .dylib        scoped arenas, BEAM/OTP per-actor
   + DWARF + link driver)              bounded heaps + Limits.Bytes)
        |                                   |
        +-----------------+-----------------+
                          |
        +-----------------+-----------------+
        v                                   v
  NB-3. compiler/tsffi + direct C ABI  NB-4. compiler/nativelower
  (outbound extern c via bindc;        (IR -> ELF64/Mach-O64 + Perceus
   ergo build --lib .so/.dylib +        dup/drop/own-reuse + arena lowering
   bun:ffi / Node N-API / .d.ts)        + closed-union niche packing)
        |                                   |
        |                                   v
        |                              NB-5. M:N Fiber Scheduler
        |                              (io_uring / kqueue, Limits.Steps
        |                               reduction counting, std/{task,actor,
        |                               process,net} native bindings)
        |                                   |
        +-----------------+-----------------+
                          v
                    NB-6. WASM + Optimizing Release Tier
                    (wasm32 linear-memory target + optimizing SSA backend
                     with SIMD/LTO + cross-tier Dev/Release/Eval parity)
                          |
                          v
                    NB-7. Stage-1 -> Stage-2 Bootstrap & Go Deletion
                    (Native self-host fixpoint, delete Stage-0 Go backend/
                     fork/gobind/runtime/v1, enforce <250 KB core spec)
                                          |
        +---------------------------------+---------------------------------+
        |                                 |                                 |
        v                                 v                                 v
  NB-8. Law-Driven Opt              NB-9. Provider Devirt             NB-10. SoA[T], Own Actor
  & Static noalloc Proofs           & ISA/Compute Providers           Teleports & --explore-faults
  (given/keeps BCE + SIMD,          (with context region              (Compiler-managed SoA cols,
   Associative/Fuse fusion,          monomorphization, law-verified    O(1) own mailbox handoff,
   static noalloc checks)            AVX/NEON vs scalar, Compute)      turn arenas, fault explorer)
```

---

## Standing Unit Rules

1. **Zero Go leakage into native units:** No unit in `NB-1`–`NB-6` introduces `goobj`, Go stack-maps (`PCDATA`/`FUNCDATA`), Go write barriers, `R14` goroutine registers, `GOCACHE`, or `purego` trampolines.
2. **Data-oriented compiler style:** All new Ergo packages (`compiler/objfmt`, `compiler/nativeabi`, `compiler/tsffi`, `compiler/nativelower`) follow Ergo's dense SoA layout conventions (`std/dense`, `uint32` IDs, zero pointer-keyed maps, zero `int32`/`int64` record fields where disallowed by `scripts/dodfloor`).
3. **Cross-tier agreement:** Every lowering slice keeps [`compiler/eval`](../../../compiler/eval/vm.ergo) as its semantic oracle and runs seeded property suites + `--mutants` under `ERGO_FALLBACK=off`.
4. **Prior-art grounding:** Each unit compares against the standing peer designs ([architecture.md](architecture.md#memory-tiers), [PRIOR_ART.md](../../../PRIOR_ART.md)): Zig direct object emission, Koka/Lean 4 Perceus reuse, Erlang/OTP per-process heaps and reductions, Rust `extern "C"` / zero-runtime DSOs, and MoonBit `.wasm`/native lowering.
5. **Budgets are gates:** a unit whose gate carries a [budget](architecture.md#budgets-and-settled-forks-2026-11-27) hands back the measured count beside the budget. Every budget is `instructions:u`, counted work or bytes; none is a wall time.

---

## Unit Specifications

### `NB-1`. `compiler/objfmt` (Direct ELF64 & Mach-O64 Object Emitter + Link Driver)

- **Owns:** `compiler/objfmt/**` (`elf64.ergo`, `macho64.ergo`, `archive.ergo`, `dwarf.ergo`, `amd64.ergo`, `arm64.ergo`, `link.ergo`), `compiler/cmd/nativeobj/**` (adding native ELF64/Mach-O64 CLI probes alongside frozen Stage-0 `goobj`).
- **Dependencies:** `compiler/ir`, `compiler/ir/handoff`, and x86_64 encoder primitives seeded from [`compiler/nativeobj/amd64.ergo`](../../../compiler/nativeobj/amd64.ergo).
- **Scope:**
  - Emit standard relocatable `.o` objects and static `.a` archives directly from Ergo without `goobj`, `as`, or `GOCACHE`:
    - **ELF64 (`linux/amd64`, `linux/arm64`):** `.text`, `.rodata`, `.data`, `.bss`, `.symtab`/`.strtab`, `.rela.text` (`R_X86_64_PC32`, `R_X86_64_PLT32`, `R_X86_64_64`, `R_X86_64_GOTPCREL`; `R_AARCH64_CALL26`, `R_AARCH64_ADR_PREL_PG_HI21`, `R_AARCH64_ADD_ABS_LO12_NC`).
    - **Mach-O64 (`darwin/amd64`, `darwin/arm64`):** `MH_OBJECT` header, `LC_SEGMENT_64` (`__TEXT`/`__DATA`/`__RODATA`), `LC_SYMTAB`, `LC_DYSYMTAB`, `X86_64_RELOC_*` / `ARM64_RELOC_*`, `LC_BUILD_VERSION`.
    - **Debug & Unwind:** Standard DWARF `.debug_line` / `__debug_line` and `.eh_frame` / `__compact_unwind` mapping machine PCs back to `.ergo` spans.
  - Provide direct platform link driver invoking `ld.lld` / `ld64` (or direct minimal static/DSO emission) to produce executables (`exe`) and shared libraries (`.so`/`.dylib`).
  - Zero Go object headers (`\x00go120ld`) and zero Go `pclntab`/`FUNCDATA`.
- **Runnable Verification Gate:**
  - `ergo test --fresh compiler/objfmt` (round-trip parser/structural laws verifying ELF64 and Mach-O64 headers, section alignment, symbol tables, relocation offsets, and DWARF line programs).
  - Native link smoke test emitting `.o` objects for both `x86_64` and `arm64` (cross-checked by `readelf -a` / `objdump -dr` on Linux and `otool -l` / `nm` on macOS, plus linking and running a real multi-object native executable calling `libc` `write`/`exit`).
  - `ergo test --mutants compiler/objfmt` (100% mutant kill rate).
- **Budget gate ([link budget](architecture.md#build-budgets)):** the dev tier links in process. `LinkStartsNoProcess`: a dev-tier link of a multi-object program starts 0 processes and its counted work is linear (`link.relocs_applied == relocations in the inputs`, each input section copied once). `ld.lld`/`ld64` stay as the `--release` and `--lib` driver and as the cross-check of the in-process link (same symbols, same run output). Command: `ergo test --fresh compiler/objfmt` (the law), and `go run ./scripts/buildcost native -subject compiler/cmd/ergo -cells` for the link cell once `NB-B0` lands (ceiling 250 M instr at `NB-7`).

---

### `NB-2`. `runtime/native` + `compiler/nativeabi` (C ABI, Perceus RC, Arenas, Per-Actor Bounded Heaps)

- **Owns:** `runtime/native/**` (`ergo_rt.h`, `rc.c`, `arena.c`, `actor_heap.c`, `str_slice.c`, `map.c`, `trap.c`, `rt_test.c`), `compiler/nativeabi/**` (`abi.ergo`, `layout.ergo`, `header_check.ergo`).
- **Dependencies:** None (executes in parallel with `NB-1`).
- **Scope:**
  - Define the canonical C ABI header `runtime/native/ergo_rt.h` and mirrored Ergo SoA layout tables in `compiler/nativeabi/`:
    - Exact 16-byte `ErgoHdr` (`rc`, `type_id`, `flags`, `alloc_bytes`) with `_Static_assert` layout verification (`ERGO_F_IMMORTAL`, `ERGO_F_ARENA`, `ERGO_F_ACTOR_HEAP`, `ERGO_F_SHARED`, `ERGO_F_HAS_DROP`).
    - Exact `ErgoStr` (24 B), `ErgoBytes`/`ErgoSlice` (32 B), `ErgoMap` (16 B), `ErgoClosure` (16 B), flat union tag+slot rules, pointer-niche `Option`, and non-unwinding tagged error results `(err_tag, payload)`.
    - Perceus helpers: `ergo_dup`, `ergo_drop`, `ergo_reuse_or_alloc`, `ergo_slice_append_own` (`rc == 1` in-place mutation without allocator round-trip).
    - Scoped O(1) bump arena (`ErgoArena`: `ergo_arena_push`, `ergo_arena_alloc`, `ergo_arena_pop`).
    - Per-actor isolated slab heap (`ErgoActorHeap`): exact `Limits.Bytes` accounting (`used <= limit_bytes`), `OverBytes` typed trap before OOM, O(1) whole-heap slab free on actor exit/`die`, and cross-actor deep-copy / unique-`own` transfer.
  - Extend [`std/process`](../../../internal/sketch/std/process/process.ergo) contract surface for `Limits.Bytes` and `OverBytes { Limit int }` natively (implemented in `NB-5` / `NB-2`).
- **Runnable Verification Gate:**
  - C ABI & sanitizer suite compiled with `clang -fsanitize=address,undefined -Wall -Wextra -Werror` exercising: `_Static_assert` agreement with `compiler/nativeabi` layout dump, `rc == 1` in-place slice/record reuse (verified 0 `malloc` calls on reuse), arena mark/reset leaks == 0, `Limits.Bytes` triggering `OverBytes` reliably without host OOM, and O(1) actor heap teardown after simulated `die` mid-allocation.
  - `ergo test --fresh compiler/nativeabi` + `ergo test --mutants compiler/nativeabi` + C test mutants.

---

### `NB-3`. `compiler/tsffi` + Direct `extern c` (`compiler/bindc`) (`--lib` + `bun:ffi` / Node N-API)

- **Owns:** `compiler/tsffi/**` (`contract.ergo`, `bunffi.ergo`, `napi.ergo`, `dts.ergo`, `cheader.ergo`), `compiler/bindc/**` (native C-ABI call emission replacing `purego` Go stubs), `compiler/cmd/ergo/build_cmd.ergo` (`--lib` native output wiring).
- **Dependencies:** `NB-1`, `NB-2`, existing [`compiler/bindc`](../../../compiler/bindc/abi.ergo) and `compiler/check/summary`.
- **Scope:**
  - **Outbound `extern c`:** Connect `compiler/bindc`'s parsed C11 header + contract JSON directly to `compiler/nativeabi` so `extern c` emits direct SysV AMD64 / AAPCS64 `CALL` sequences (with `(ptr, len)` zero-copy marshaling for `in_buffer`/`reads` slices and strings) — zero `purego` and zero Go cgo overhead.
  - **Inbound `ergo build --lib -o DIR`:**
    - Emit compact native `.so` / `.dylib` + flat C header (`DIR/include/<pkg>.h`) with zero Go runtime, zero background GC threads, and zero `SIGURG`/`SIGSEGV` signal handlers.
    - From checked Ergo summaries (`compiler/check/summary`), auto-generate in `compiler/tsffi/`:
      1. `bun:ffi` bindings (`*.bun.ts`) with zero-copy `ptr` / `CString` / `Uint8Array` marshaling and typed `{ ok, value | error }` closed-union decoding.
      2. Thin Node N-API C shim + TS wrapper (`*_napi.c`, `*.node.ts`) sharing the exact same TypeScript API.
      3. Strict `.d.ts` declarations preserving closed unions, `Option`, and nominal types (no `any`).
  - Keep `internal/tsproj` (`ergo tsmap` / `std/rpc`) explicitly scoped to HTTP/NDJSON/WebSocket RPC boundaries.
- **Runnable Verification Gate:**
  - Outbound test: Ergo module binds a real C library fixture (e.g., SQLite or custom C struct/callback library) via `ergo bind c`, compiles natively via `NB-1`/`NB-2`, and runs under ASan with zero leaks and zero `purego`.
  - Inbound test: `ergo build --lib` produces a `.so`/`.dylib` (verified via `ldd`/`otool -L` to link only `libc`/`libSystem`, zero Go symbols), then executed directly from **Bun** (`bun test` importing generated `bun:ffi` module), **Node** (N-API test), and checked by `tsc --noEmit` on generated `.d.ts`.
  - `ergo test --fresh compiler/tsffi compiler/bindc` + `ergo test --mutants compiler/tsffi`.

---

### `NB-4`. `compiler/nativelower` (Typed IR -> ELF64/Mach-O64 + Perceus & Arena Lowering)

- **Owns:** `compiler/nativelower/**` (`lower.ergo`, `perceus.ergo`, `reuse.ergo`, `arena.ergo`, `union.ergo`, `codegen_amd64.ergo`, `codegen_arm64.ergo`), `compiler/native/**`.
- **Dependencies:** `NB-1`, `NB-2`, `compiler/ir`, `compiler/check/*`, `compiler/lower/*`.
- **Scope:**
  - Lower checked `compiler/ir` functions, records, closed unions, pattern matches, closures, `try`/`catch`, loops, and `extern c` calls into Dev-tier `amd64`/`arm64` machine code emitted via `compiler/objfmt`.
  - **Perceus RC pass:** Insert precise `dup` on shared reads and deterministic last-use `drop` across all control-flow paths (including `try` failure unwind paths and loop `break`/`continue` exits).
  - **Static `own` unique-reuse pass:** Transform `own` parameters and last-use updates (`Append`, `Put`, record spread `T{..r, f: e}`, `match` arm reconstruction) into zero-allocation in-place mutation or conditional `rc == 1` reuse.
  - **Scoped arena pass:** Identify non-escaping frame/scope temporaries from ownership/escape facts and route them to `ErgoArena` with O(1) scope-exit reset.
  - **Flat unions & niches:** Lower closed unions using [`docs/spec/types.md`](../../spec/types.md#unions) rule 6 shared slots and pointer/handle `Option` niches.
  - Emit DWARF line/frame tables mapping every trap/backtrace to `.ergo` source positions.
- **Runnable Verification Gate:**
  - `TestNativeLowerAgreesWithEval`: seeded generated corpus (scalars, records, flat & recursive unions, closures, `own` loops, `Append`/`Put`, `try`/`catch`, defects) compiled natively to ELF64/Mach-O64 and compared output-for-output, failure-for-failure, and exit-for-exit against [`compiler/eval`](../../../compiler/eval/vm.ergo) under `ERGO_FALLBACK=off`.
  - Memory & reuse receipts: ASan/LSan leak check == 0 across all corpus tests, plus measured instruction/allocation count proving `rc == 1` reuse loops perform O(1) heap allocations vs Stage-0 Go baseline.
  - `ergo test --mutants compiler/nativelower`.
- **Budget gate ([build budgets](architecture.md#build-budgets), [RC cost](architecture.md#perceus-rc-cost)):**
  - `BackendWorkIsLinear` (a law in `compiler/nativelower`, counted at small sizes, never timed): for seeded programs of 1, 2, 4 and 8 times a base size, `lower.visits <= 3 * ir.insts`, `encode.funcs == funcs`, and `lower.visits` doubles when the program doubles. Command: `ergo test --fresh compiler/nativelower`.
  - The backend cell (`compiler/perceus` + `compiler/nativelower` + `compiler/objfmt` encode) of a cold build of the subject is recorded and ratcheted from this unit on, with the ceiling of 800 M instr enforced at `NB-7`. Command: `go run ./scripts/buildcost native -subject compiler/cmd/ergo -cells`.
  - The RC counts of [`NB-4E`](#nb-4e-rc-cost-receipt) on workload W1.
- **Compliance gate ([the Go route as oracle](architecture.md#the-go-route-as-the-compliance-oracle)):** `TestNativeAgreesWithGoRoute` (`internal/selfhostcheck`; seeded, a failing seed prints the Ergo source, both observations and the replay command; builds Go because the Go-built program is the oracle; `needsBuild`, one-binary helper, kernel memory bound): each program of the seeded corpus and each of `examples/` is built by the Go route and by the native route and run on the same cases; output, exit code, typed failures, defects and law verdicts are equal, and where eval can run the program all three agree. Command: `go test ./internal/selfhostcheck -run TestNativeAgreesWithGoRoute` (under the crunch rule a unit runs it on its own example only; the whole corpus is owed).

---

### `NB-5`. M:N Fiber Scheduler (`io_uring` / `kqueue` + `Limits.Steps` + Actors)

- **Owns:** `runtime/native/sched.c`, `runtime/native/fiber.c`, `runtime/native/io_linux_uring.c`, `runtime/native/io_darwin_kqueue.c`, `runtime/native/actor.c`, `runtime/native/explore.c`, plus native runtime bindings in `internal/sketch/std/{task,actor,process,net,exec,signal,time}/**`.
- **Dependencies:** `NB-2`, `NB-4`.
- **Scope:**
  - Cooperative M:N work-stealing fiber scheduler: worker threads pinned to CPU cores, per-worker Chase-Lev deques, fiber stack pools with guard pages, and `x86_64`/`arm64` context switchers.
  - Native async I/O reactors: **`io_uring`** on Linux (`SQE`/`CQE` sockets, Unix `SCM_RIGHTS` FD passing, files, timers, pipes, `signalfd`, `eventfd` wakes) and **`kqueue`** on macOS (`kevent64`, sockets, timers, signals, `EVFILT_USER`).
  - **Native reduction counting (`Limits.Steps`):** Prologue + back-edge `steps_remaining` decrement driving fair cooperative preemption (quantum yield) and exact `Limits.Steps` -> `OverSteps{Limit}` enforcement.
  - **Per-actor isolation & supervision:** Connect `std/actor` (`Actor`, `Start`, `Supervise`, `Watch`, `Link`, bounded mailboxes) and `std/process` (`Run`, `Limits{Time, Steps, Bytes}`, `OverTime`, `OverSteps`, `OverBytes`, `Crashed`) onto `ErgoActorHeap` with O(1) crash reclamation and I1/I2 interruption/mask semantics.
  - **Deterministic exploration:** Native `ErgoSchedHooks` supporting `ergo test --explore [dfs|pct|random]` and `--schedule TOKEN` exact replay.
- **Runnable Verification Gate:**
  - Native stdlib law suites (`std/task`, `std/actor`, `std/process`, `std/net`, `std/time`, `std/sync`, `std/queue`) compiled and executed natively under ASan/TSan (`ERGO_FALLBACK=off`).
  - Explicit isolation properties: `OverBytesEndsActorOnly`, `OverStepsEndsProcessOnly`, `CrashReclaimsActorHeapInO1`, `I1StopCancelsBlockedSocketRead`, `I2MaskFinishesCleanup`, and `--explore` deterministic replay parity.
  - Concurrent HTTP/socket echo & actor ping-pong benchmark comparing native `io_uring`/`kqueue` fiber runtime against matched Stage-0 Go and C/Rust controls.
  - Native scheduler/runtime C + Ergo mutant suites.
- **Budget gate ([stacks and density](architecture.md#fiber-stacks-and-actor-density), [preemption](architecture.md#preemption-and-blocking-foreign-calls)):** the stack model and the hand-off are decided; this unit builds them and its gate includes the laws of [`NB-5C`](#nb-5c-foreign-call-hand-off) and [`NB-5D`](#nb-5d-actor-density). "Fiber stack pools with guard pages" above reads: pooled contiguous stacks with a prologue limit compare, no guard mapping per stack.
- **Compliance gate:** `TestNativeAgreesWithGoRoute` widens to the std suites this unit binds (`std/task`, `std/actor`, `std/process`, `std/net`, `std/time`, `std/sync`, `std/queue`) and adds `-explore`: for seeded schedule tokens, the observable event sequence of the native runtime equals the Go runtime's for the same token. Command: `go test ./internal/selfhostcheck -run TestNativeAgreesWithGoRoute -explore`.

---

### `NB-6`. WASM Target & Optimizing Release Tier

- **Owns:** `compiler/objfmt/wasm/**` (`wasm32.ergo`, `encode.ergo`), `compiler/ssaopt/**` (or `compiler/nativelower/opt/**` + optimizing SSA backend adapter), `compiler/tsffi/wasm/**`, `runtime/wasm/**` (compact linear-memory Perceus RC + arena runtime).
- **Dependencies:** `NB-3`, `NB-4`, `NB-5`.
- **Scope:**
  - **`.wasm` Build Target (`ergo build --platform wasm32`):**
    - Lower typed SSA IR to `wasm32` binary module + compact linear-memory Perceus RC & arena runtime (zero WasmGC lock-in, tiny binary size suitable for browser & edge workers) + typed `.d.ts`/ESM JS loader from `compiler/tsffi`.
  - **Optimizing Release Tier (`ergo build --release`):**
    - Lower Ergo typed SSA IR + proved optimization facts (`given`/`gives`/`keeps`, `own` uniqueness, purity, fused `Transform` laws, `std/simd` vector loops) through Ergo's own SSA optimizer, `compiler/ssaopt` ([decided 2026-11-27](architecture.md#release-optimizer-ergos-own-compilerssaopt); its floor is measured by [`NB-6C`](#nb-6c-ssa-optimizer-floor)), with LTO, targeting `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, and `wasm32`.
    - Cross-tier safety invariant: Release Tier optimizations never alter observable outputs, typed failures, defects, or `Limits` enforcement.
- **Runnable Verification Gate:**
  - Cross-tier agreement suite: every example, law, and seeded corpus test produces identical results across `compiler/eval`, Dev Tier (`NB-4`), Release Tier (`--release`), and `wasm32` (executed in Bun/Node WebAssembly runtime).
  - Binary size & benchmark receipts: `.wasm` minimal/codec module stripped size receipt + Release Tier throughput/allocation/instruction receipt against Dev Tier and C/Rust/Go controls.
  - `ergo test --mutants compiler/ssaopt compiler/objfmt/wasm`.

---

### `NB-7`. Stage-1 -> Stage-2 Native Bootstrap & Stage-0 Go Deletion

- **Owns:** `compiler/cmd/ergo/**`, `compiler/driver/**`, `docs/spec/**` (`<250 KB` core spec audit), plus removal of frozen Stage-0 Go artifacts (`toolchain/ir/**`, `internal/gobind/**`, `internal/sketch/std/gostd/**`, `internal/sketch/lower/**`, `runtime/v1/**`, `runtime/rt/**`, `internal/linked/**`, `compiler/nativeobj/goobj.ergo`).
- **Dependencies:** `NB-1` through `NB-6`, plus language surface simplification (`RSI-*`) and self-hosted compiler readiness.
- **Scope:**
  1. **Stage-1 build:** Stage-0 `bin/ergo` compiles `compiler/cmd/ergo` via `NB-1`–`NB-6` into standalone native executable `ergo-stage1` (ELF64 on Linux, Mach-O64 on macOS).
  2. **Stage-2 & Stage-3 fixpoint:** Run `ergo-stage1 build ./compiler/cmd/ergo -o ergo-stage2` and `ergo-stage2 build ./compiler/cmd/ergo -o ergo-stage3` with **no Go toolchain on `PATH`**; verify deterministic byte-identical output (`sha256(ergo-stage2) == sha256(ergo-stage3)`, and `sha256(ergo-stage1) == sha256(ergo-stage2)`).
  3. **Delete Stage-0 Go backend & interop:** Remove Go fork patches (`toolchain/ir/`), `goobj` emission (`compiler/nativeobj/goobj.ergo`), `internal/gobind/`, `internal/sketch/std/gostd/`, `internal/sketch/lower/`, `runtime/v1/`, `runtime/rt/`, `internal/linked/`, and Go-interop syntax/types (`import go`, `extern go`, `go.Ref`, `go.New`, `go.Zero`, `go.Any`, `go.Error`, `go.Foreign`), along with `go.mod` / `GOCACHE` coupling in `compiler/driver/`.
  4. **Spec diet (`<250 KB`):** Retire Go-interop specification surface ([`docs/spec/interop.md`](../../spec/interop.md) Go rules, Go-runtime internals across `docs/spec/*.md`) and land simplified language specs so total byte count of `docs/spec/*.md` excluding [`docs/spec/stdlib.md`](../../spec/stdlib.md) is strictly `< 250 KB` (`< 256,000` bytes).
- **Runnable Verification Gate:**
  - Clean `PATH` (no `go` binary) native bootstrap test: `sha256(ergo-stage2) == sha256(ergo-stage3)` on Linux (`amd64`/`arm64`) and macOS (`arm64`), `ldd`/`otool -L` confirming zero Go runtime linkage.
  - Full repository suite under `ergo-stage2`: `ergo test --fresh ./...` across stdlib, `compiler/`, `modules/`, `examples/`, conformance corpus, and `--mutants` with `ERGO_FALLBACK=off`.
  - Spec byte budget check: `python3` / `wc -c` verifying sum of `docs/spec/*.md` excluding `docs/spec/stdlib.md` is `< 256000` bytes.
- **Budget gate (the owner's ceilings, measured on `ergo-stage2`; [budgets](architecture.md#budgets-and-settled-forks-2026-11-27)):**
  - Cold build of the subject: `go run ./scripts/buildcost native -subject compiler/cmd/ergo -cells -ceiling` exits 0 only when the tree total is <= 3,000 M `instructions:u` and the cells are within front 1,950 M, backend 800 M, link and write 250 M.
  - One-function edit in the resident compiler: `go run ./scripts/buildcost native -subject compiler/cmd/ergo -edit body -resident -ceiling` exits 0 only when the daemon's request is <= 3 M instr.
  - Store: `go run ./scripts/cachecost -scenario subject -ceiling` exits 0 only when the cache root is <= 200 MiB after the scenario and holds none of the Stage-0 classes (`go`, `toolchain`, `modules`, `std`, `irc`); the machine cap is 2 GiB.
  - RC cost on workload W2 within the [`NB-4E`](#nb-4e-rc-cost-receipt) counts.
  - Compliance: the last `go test ./internal/selfhostcheck -run TestNativeAgreesWithGoRoute -explore` over the whole corpus is green at the head that deletes Go, with an empty divergence list or each remaining row shown to be a Stage-0 fault. After the deletion the oracles are `compiler/eval`, the cross-tier agreement suite of `NB-6`, and the same suites agreeing across `linux/amd64`, `linux/arm64` and `darwin/arm64`.
  - A ceiling that is not met at this gate does not weaken: the unit stays open with the measured cell and the named follow-up that lowers it (the floor rule: slower is a bug).

---

## Budget and Fork Units (2026-11-27)

These units carry the [budgets and settled forks](architecture.md#budgets-and-settled-forks-2026-11-27). They land before or beside the code generator, so that each budget is a gate from the first line of `compiler/nativelower`. Under the backend crunch rule each unit runs only its own law and its packages' focused tests; the instruction campaigns are listed as owed and run after the backend swap.

```text
  NB-B0 (harness) ──► every budget command below
  NB-1 ─► in-process link ─┐
  NB-4 ─► NB-4E (RC receipt)
  NB-4 + SEMANTIC-FUNC-HASH-V1 + NB-1 ─► NB-11 (resident compiler) ─► NB-7 ceilings
  NB-1 + NB-4 ─► NB-12 (one store, byte cap) ─► NB-7 ceilings
  NB-2 + NB-4 ─► NB-5 ─► NB-5C (foreign hand-off), NB-5D (actor density)
  NB-4 ─► NB-6C (ssaopt floor) ─► NB-6 release tier, NB-8
```

### `NB-B0`. Budget Harness (`buildcost native`, `cachecost`)

- **Owns:** `scripts/buildcost/native.go` (the `native` subcommand), `scripts/cachecost/**`, `docs/receipts/native-budgets.md`.
- **Dependencies:** none for the harness; each cell reads "unmeasured: waits `NB-n`" until that unit lands (never 0, as in [native-ab](../../receipts/native-ab.md)).
- **Scope:** `buildcost native -subject PKG [-cells] [-edit body|signature] [-resident] [-ceiling]` runs one `ergo build` (or one resident request) of the subject under the tree instruction counter that `buildcost count` already has, splits the total into the cells front, backend, link and write by the compiler's stage counters (`ERGO_STAGE_REPORT`), and checks conservation: the cells sum to the tree's total exactly, with the residual as its own cell. `cachecost [-scenario subject] [-ceiling]` prints bytes per class of the cache root (`du -sb`, one line a class) and runs the store scenario. An unknown subcommand of `buildcost` exits 2 (today an unknown word starts the default scenarios).
- **Acceptance law (red now):** `go run ./scripts/buildcost native -subject compiler/cmd/nativeab/testdata/subject -cells` prints a conserved table (today: no such subcommand); `go run ./scripts/cachecost` prints the class table (today: no such script). `TestNativeCellsConserve`: for seeded synthetic stage reports, the cells sum to the total and a lost or duplicated record fails.
- **Affected checks:** `go build ./scripts/...`, `go test ./scripts/buildcost ./scripts/cachecost`.

### `NB-4E`. RC Cost Receipt

- **Owns:** `compiler/cmd/rccost/**`, `runtime/native/ergo_stats.c` and the `ERGO_RT_STATS` counters in `runtime/native/ergo_rc.c`, `docs/receipts/rc-cost.md`.
- **Dependencies:** `NB-4A` (on main: `compiler/perceus` with `TransformStats`), `NB-4` for the dynamic half, `NB-4C` for the atomic count.
- **Scope:** `rccost static PKG...` lowers each package's IR through `compiler/perceus` and prints, per package and in total: allocations, `dup` and `drop` emitted, reuse pairs, arena promotions, and the scoped baseline (a `dup` at every copy of a counted value, a `drop` at every scope exit) computed from the same IR. `rccost run WORKLOAD` runs a native binary built with `ERGO_RT_STATS` and prints the executed counts (`dup`, `drop`, atomic operations, allocations, reuse hits, reuse fallbacks) beside the run's `instructions:u`. Workloads W1 and W2 are defined in [the architecture](architecture.md#perceus-rc-cost).
- **Acceptance law:** `RcCountsAgree`: for the seeded corpus of `compiler/perceus`, the executed counts of the native run equal the counts of `compiler/perceus`'s verifier (`VerifyStats`) on the same path. Then the gate counts: atomic operations 0 on W1; RC instruction share <= 2%; in-place reuse >= 95% of paired sites and 100% of `own` sites; the removed-operation ratio recorded and ratcheted.
- **Commands:** `ergo run ./compiler/cmd/rccost -- static ./compiler/...` (red now: no such package; the static half needs only what is on main), `ergo run ./compiler/cmd/rccost -- run W1`.
- **A count over its number** is a bug with a named `compiler/perceus` follow-up (borrow inference of a parameter, a missed pairing), not a note.

### `NB-5C`. Foreign-Call Hand-Off

- **Owns:** `runtime/native/handoff.c`, the foreign-call bracket in `compiler/nativelower` (two stores around each `extern c` call), the `blocking` and `interrupt` fields of the `compiler/bindc` contract, `models/handoff/**`.
- **Dependencies:** `NB-5` (scheduler), `NB-3` (direct `extern c`).
- **Scope:** as decided in [Preemption and blocking foreign calls](architecture.md#preemption-and-blocking-foreign-calls): the in-foreign mark, the monitor, the move of a worker's run queue and reactor ring to a spare thread, the return path of the blocked thread, the blocking pool for calls a contract marks `blocking`, the `interrupt` hook on I1 stop. Under `--explore` the monitor's decision is a scheduler hook, so a schedule token replays a hand-off.
- **Acceptance laws:**
  - `models/handoff` (explored exhaustively, `ergo test models/handoff`): a run queue has exactly one owning thread at every step; no fiber runs on two threads; every ready fiber is owned by a thread that is not in foreign code after finitely many steps; the interleaving "the C function returns while the monitor moves the queue" loses and duplicates no fiber.
  - `BlockedForeignCallStarvesNoPeer` (native, one worker): fiber A blocks in C on a pipe read; B1..Bk queued behind it run and the last writes the pipe; the test ends when A returns. Counted: hand-offs = 1, threads created <= 1. No time is compared; a deadline only names a hang.
  - `ShortForeignCallsNeverHandOff`: 1,000,000 calls of a C function that returns at once: hand-offs = 0, and the bracket adds <= 4 `instructions:u` a call against the same loop with the bracket compiled out.
  - `HandOffKeepsCountsLocal`: under TSan, a hand-off with 1,000 parked actors on the moved queue reports no race on any non-atomic count.
- **Commands:** `ergo test models/handoff`; `make -C runtime/native test-handoff` (ASan and TSan builds).

### `NB-5D`. Actor Density

- **Owns:** `runtime/native/stack_pool.c`, the first-chunk size classes in `runtime/native/ergo_actor.c`, the prologue stack-limit compare in `compiler/nativelower`, `runtime/native/density_test.c`.
- **Dependencies:** `NB-2`, `NB-5`.
- **Scope:** as decided in [Fiber stacks and actor density](architecture.md#fiber-stacks-and-actor-density): stackless idle actors, pooled contiguous fixed-reserve stacks with a prologue limit compare, `extern c` on the worker's thread stack, a 256 B first heap chunk.
- **Acceptance law (`DensityHolds`, counted bytes from the runtime's own counters, never RSS):** spawn 1,000,000 actors that each hold one `int` and wait for a message: `runtime_bytes / actors <= 512`, `stacks_held == 0`. Park 100,000 fibers at depth 1: `stack_bytes_committed / fibers <= page + 256`, `mappings <= 64`. A task that recurses past its reserve ends with `StackOverflow`, and its 999 peers finish (`StackOverflowEndsActorOnly`). The same law at 1,000 and 10,000 actors shows the bytes per actor do not grow with the count.
- **Command:** `make -C runtime/native test-density` (the kernel-enforced memory bound of the test is 1 GiB).

### `NB-6C`. SSA Optimizer Floor

- **Owns:** `compiler/ssaopt/**` first passes (inlining, register allocation, dead code and bounds-check elimination from `given`/`keeps`), `compiler/cmd/optfloor/**`, `docs/receipts/opt-floor.md`.
- **Dependencies:** `NB-4`.
- **Scope:** settle by measurement what [the decision](architecture.md#release-optimizer-ergos-own-compilerssaopt) leaves open: how far Ergo's own optimizer is from `clang -O2`. The kernels are the repository's matched controls ([bench](../../specs/bench.md)) in four classes: scalar loops, slice and bounds-heavy code, calls and closures, allocation and reuse.
- **Acceptance law:** `optfloor run` builds each kernel with `--release` and its C control with `clang -O2`, runs both on the same input, checks the outputs equal, and prints `instructions:u` of each. Accepted when every class is <= 1.10 of its control, or when each class above it has its missing pass named as an opened unit. A class still above 1.25 after its named passes land opens `NB-6D` (an emitter of LLVM IR text for an external `clang`, outside the bootstrap). Cross-tier agreement (`NB-6`) holds for every kernel.
- **Command:** `ergo run ./compiler/cmd/optfloor -- run` (red now: no such package). Compile cost of `--release` is recorded beside it with `buildcost native -release`.

### `NB-11`. Resident Compiler (`ergo dev` builds)

- **Owns:** `compiler/resident/**` (the kept program: checked tables, per-function hash, per-function code), `compiler/objfmt/patch.ergo` (dev layout, in-place patch), the `build`, `run` and `test` requests of the `ergo dev` protocol in `compiler/cmd/ergo/**`, `models/dev/**` (the lifecycle model gains the kept program).
- **Dependencies:** `NB-1` (in-process link), `NB-4`, `SEMANTIC-FUNC-HASH-V1` (the function-granular hash, with its separate span hash so a moved line updates the line table without recompiling), the daemon on main ([tooling](../../tooling.md), "ergo dev").
- **Scope:** the one daemon per workspace and release keeps the checked program in memory and serves `build`, `run` and native `test`. On a request it re-reads only files whose stat changed, re-checks only the declarations whose text changed and their dependents by signature hash, lowers only the functions whose semantic hash changed, and patches the dev-layout output in place (a function that outgrew its slot moves to the end; its callers reach it through its stable address). The CLI is the client and answers in process when no daemon serves it. Kept memory is bounded: the daemon's counted bytes for the subject are <= 512 MiB, and it exits when idle, as today.
- **Acceptance laws:**
  - `ResidentAgreesWithCold` (model-based, seeded; a failing seed prints the edit sequence and the replay command): for a random sequence of edits (body, signature, add, delete, rename, comment, file move) to a generated program, after each edit the daemon's diagnostics equal the in-process ones and the patched executable's output, failure and exit equal those of `ergo build --fresh`.
  - `OneBodyEditRebuildsOneFunction` (counted): after one body edit, `files_read == 1`, `decls_checked == 1`, `funcs_lowered == 1`, `funcs_patched == 1`, `bytes_patched <= new code + its relocations + its line rows`, `processes == 0`. A comment or format edit: `funcs_lowered == 0`. A signature edit: `funcs_lowered` equals the function and its direct callers.
  - `models/dev`: at most one daemon per workspace and release still holds; a request is never answered from a program older than its inputs.
- **Commands:** `ergo test --fresh compiler/resident`, `ergo test models/dev`, `go run ./scripts/buildcost native -subject compiler/cmd/ergo -edit body -resident` (recorded here, ceiling 3 M at `NB-7`).

### `NB-12`. One Store, Byte Cap

- **Owns:** `compiler/build/store_cap.ergo`, `compiler/build/trim.ergo` (the byte cap beside today's age and entry caps), `compiler/build/native_store.ergo` (the native classes: function code, objects, checked tables, linked images), the scenario of `scripts/cachecost`.
- **Dependencies:** `NB-1`, `NB-4`; `NB-B0` for the command.
- **Scope:** one content-addressed store for a machine's user, shared by every repository and work tree, with a byte cap (default 2 GiB, `ERGO_CACHE_MAX`). A write that would pass the cap first evicts least-recently-used entries no running build holds. Native artefacts are keyed by content at function grain, so a second work tree adds only what differs. A build writes nothing into the work tree except its named outputs, each a clone of the store's image where the file system clones.
- **Acceptance laws:**
  - `StoreNeverPassesItsCap` (model-based, seeded, against a reference model of the store): after any sequence of builds, edits, trims and concurrent builds in several work trees, `store_bytes <= cap` at every quiescent point; no entry held by a running build is evicted; a build after an eviction is correct (it rebuilds, it never reads a missing entry).
  - `SecondWorkTreeAddsOnlyTheDifference` (counted): building an identical tree in a second workspace writes 0 store bytes; with one body edit it writes that function's entries and one image.
  - `WorkTreeHoldsOnlyOutputs` (counted): files created in the work tree by `ergo build -o X` = `{X}`.
- **Commands:** `ergo test --fresh compiler/build`; `go run ./scripts/cachecost -scenario subject` (recorded here; ceiling 200 MiB and the absence of the Stage-0 classes at `NB-7`).

---

### `NB-8`. Law-Driven Optimization & Static `noalloc` Floor Proofs

- **Owns:** `compiler/ssaopt/laws.ergo`, `compiler/ssaopt/bce.ergo`, `compiler/perceus/noalloc.ergo`, plus contract metadata hooks in `compiler/ir/` and `compiler/check/facts/`.
- **Dependencies:** `NB-4`, `NB-6` (uses proved `given`/`gives`/`keeps` and `law` metadata; feeds `compiler/ssaopt` and static checks).
- **Scope:**
  - **Bounds & branch elimination:** Propagate verified `given`/`keeps` length/range facts (`0 <= i && i < xs.Len()`, matched SoA lengths) into Release SSA to drop redundant slice bounds checks and unreachable branches.
  - **Law-driven fusion & SIMD tree reductions:** Fuse chained pure transforms (`Map`/`Filter`/`Fold`) and lower `Associative` + `Identity` folds into multi-lane SIMD reductions; hoist/CSE `needs`-empty calls across modules safely.
  - **Static performance-floor checks:** Enforce compile-time `noalloc` / zero-heap-escape / `own`-reuse checks on marked hot functions using `compiler/perceus` facts so allocation regressions fail check time.
- **Runnable Verification Gate:**
  - Cross-tier parity across `compiler/eval`, Dev Tier, and `NB-8` optimized Release Tier on fused/reduced/BCE kernels, plus compile-fail negative tests proving a synthetic heap-escaping regression in a `noalloc` function is rejected at check time.
  - Benchmark receipts showing bounds-check elimination, zero-alloc tight loops, and vector reduction speedups vs C/Rust `-O2/-O3` controls.

---

### `NB-9`. Whole-Region Provider Devirtualization, Verified ISA Providers & Compute Providers

- **Owns:** `compiler/ssaopt/devirt.ergo`, `std/simd/` / hardware provider contracts, `std/compute/` (`Compute` execution provider).
- **Dependencies:** `NB-4`, `NB-5`, `NB-6` (builds alongside `NB-8`).
- **Scope:**
  - **Whole-region `with context` devirtualization:** Specialize closed `with context { ... }` call subtrees to concrete provider implementations and inline across the provider boundary (eliminating dynamic vtable/interface dispatch while retaining full test mockability).
  - **Hardware ISA as verified providers:** Model `AVX-512` / `AVX2` / `NEON` / scalar kernels behind ordinary capability contracts, selected once at boot/target and proved bit-identical to pure scalar implementations by Ergo's law suite.
  - **`Compute` providers:** Execute pure (`needs` empty) pipelines with proved `Associative` laws identically under sequential `compiler/eval`, local SIMD/multi-core worker pools, or distributed sharded runners behind one provider interface.
- **Runnable Verification Gate:**
  - Disassembly/IR inspection receipt proving devirtualized `with context` regions emit zero indirect provider calls, plus cross-ISA law runs proving SIMD providers agree bit-for-bit with scalar reference laws and `Compute` providers match sequential `eval`.

---

### `NB-10`. Transparent `SoA[T]`, Zero-Copy `own` Actor Teleports, Turn Arenas & `--explore-faults`

- **Owns:** `std/soa/` (`SoA[T]` / `DenseTable[T]` layout), `compiler/nativelower/soa.ergo`, `runtime/native/teleport.c`, `runtime/native/turn_arena.c`, `compiler/testexec/faults.ergo` (`--explore-faults`), boundary memory policy hooks.
- **Dependencies:** `NB-2`, `NB-4`, `NB-5`.
- **Scope:**
  - **Transparent `AoS -> SoA`:** Compiler-managed `SoA[T]` storage packing struct fields into cache-line-contiguous columns with hot/cold field splitting and stride-1 vector scans behind normal record syntax (safe because Ergo value semantics never expose raw element/field pointers).
  - **Zero-copy `own` actor teleports & turn reset arenas:** Transfer `own` buffers across `ErgoActorHeap` mailboxes in O(1) via ownership/byte-count handoff (preserving exact `Limits.Bytes`, zero copies, zero locks), and reset per-turn actor scratch arenas in one cursor write on `Step` completion.
  - **Boundary-scoped memory policies & `--explore-faults`:** Provide explicit boundary arena scopes, deterministic alloc counters / OOM fault injection, opt-in buffer pools (`std/arena`, `io_uring` registered buffers), and automated `ergo test --explore-faults` replaying happy paths against every legal `needs` `fails` variant, `OverBytes`, and I1/I2 stop without production failpoint annotations.
- **Runnable Verification Gate:**
  - ASan/UBSan + property tests proving O(1) cross-actor `own` teleport keeps `Limits.Bytes` exact across sender/receiver and turn arenas reclaim 100% of turn scratch in 1 store.
  - `ergo test --explore-faults` receipt catching an injected buggy rollback handler across multi-step external effects (`fs`/`net`/`lock`), plus cache-stride benchmark comparing `SoA[T]` vs naive AoS and C structs.
