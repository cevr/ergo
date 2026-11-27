# Ergo Native Backend — Ordered Build Units (`NB-1` to `NB-7`)

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

---

### `NB-6`. WASM Target & Optimizing Release Tier

- **Owns:** `compiler/objfmt/wasm/**` (`wasm32.ergo`, `encode.ergo`), `compiler/ssaopt/**` (or `compiler/nativelower/opt/**` + optimizing SSA backend adapter), `compiler/tsffi/wasm/**`, `runtime/wasm/**` (compact linear-memory Perceus RC + arena runtime).
- **Dependencies:** `NB-3`, `NB-4`, `NB-5`.
- **Scope:**
  - **`.wasm` Build Target (`ergo build --platform wasm32`):**
    - Lower typed SSA IR to `wasm32` binary module + compact linear-memory Perceus RC & arena runtime (zero WasmGC lock-in, tiny binary size suitable for browser & edge workers) + typed `.d.ts`/ESM JS loader from `compiler/tsffi`.
  - **Optimizing Release Tier (`ergo build --release`):**
    - Lower Ergo typed SSA IR + proved optimization facts (`given`/`gives`/`keeps`, `own` uniqueness, purity, fused `Transform` laws, `std/simd` vector loops) through an optimizing SSA backend (Cranelift/QBE/LLVM or native SSA optimizer + LTO) targeting `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, and `wasm32`.
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
