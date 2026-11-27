# Ergo Native Backend Architecture

This document defines Ergo's end-to-end native execution architecture following the [2026-11-27 Native Backend Pivot](../../decisions.md#native-backend-pivot-elfmach-o-perceusarenas-c--bun-ffi-2026-11-27) and [NORTH_STAR.md](../../../NORTH_STAR.md).

Ergo replaces Go as its product runtime and object format with direct native code generation (ELF64 on Linux, Mach-O64 on macOS, `.wasm` on browser/edge) over zero-GC deterministic memory management (Perceus RC + compile-time `own` unique reuse, scoped O(1) bump arenas, and BEAM/OTP per-actor bounded heaps) and standard platform C ABI interoperability. Go remains **Stage-0 bootstrap only** until self-hosted Ergo passes identical `Stage-1 -> Stage-2` native bootstrap in [`NB-7`](units.md#nb-7-stage-1---stage-2-native-bootstrap--stage-0-go-deletion).

Related documents:
- [units.md](units.md) — ordered vertical implementation slices (`NB-1` through `NB-7`), owned paths, and verification gates
- [NORTH_STAR.md](../../../NORTH_STAR.md) — pillars, tiebreaks, and native performance floor
- [docs/decisions.md](../../decisions.md#native-backend-pivot-elfmach-o-perceusarenas-c--bun-ffi-2026-11-27) — recorded pivot decisions and superseded Go-only priors
- [docs/spec/types.md](../../spec/types.md) — value semantics, `own`/`mut`, flat unions, and laws
- [docs/spec/runtime.md](../../spec/runtime.md) — tasks, I1/I2 interruption/masks, actors, and deterministic explore schedules

---

## Compilation Pipeline

Every target shares one frontend, one typed IR, and one check-time evaluator:

```text
  .ergo source + ergo.mod + content mappers
        |
        v
  [compiler/syntax] -> [compiler/check/*] (SoA dense tables, facts, footprints)
        |
        +-------------------------------+-------------------------------+
        |                               |                               |
        v                               v                               v
  [compiler/eval]                 [compiler/ir]                   [compiler/tsffi]
  In-process VM                   Typed SoA/SSA IR                Checked contracts ->
  <50ms incremental               + compiler/ir/handoff           bun:ffi / N-API / .d.ts
  tests, laws, mutants            |                               (for --lib & .wasm)
                                  +-------------------+
                                  |                   |
                                  v                   v
                      Dev / Test Tier             Release Tier
                      [compiler/nativelower]      Optimizing SSA Backend
                      Perceus RC + Arenas +       (Cranelift/QBE/LLVM or
                      x86_64/arm64 baseline       native SSA + SIMD + LTO)
                                  |                   |
                                  v                   +----------+
                      [compiler/objfmt]               |          |
                      Direct ELF64 / Mach-O64         v          v
                      .o / .a / exe / .so / .dylib  ELF64/Mach-O64  .wasm
                                  |                   |          |
                                  +---------+---------+          |
                                            |                    |
                                            v                    v
                                    [runtime/native]      [runtime/wasm]
                                    Perceus + Arenas +    Linear-memory RC +
                                    Actor Heaps +         Host event bridge
                                    io_uring / kqueue
```

### Existing Foundation vs. Native Pivot Packages

| Layer | Existing on `main` | Native Backend Disposition |
| --- | --- | --- |
| Frontend & Check | `compiler/syntax`, `compiler/format`, `compiler/front`, `compiler/check/*`, `compiler/contentmap`, `compiler/lawcache`, `compiler/lawledger` | Retained and extended with static `own`/escape/arena facts; zero Go dependency |
| Check-time VM | [`compiler/eval/vm.ergo`](../../../compiler/eval/vm.ergo) (`compiler/eval/*`) | Retained as primary `<50ms` incremental runner for pure tests, laws, and mutants |
| Typed IR | [`compiler/ir/ir.ergo`](../../../compiler/ir/ir.ergo), [`compiler/ir/handoff.ergo`](../../../compiler/ir/handoff.ergo) | Evolves from Stage-0 Go-IR JSON/EIR handoff into target-neutral typed SSA + ownership/arena annotations |
| Baseline Object Emission | [`compiler/nativeobj/amd64.ergo`](../../../compiler/nativeobj/amd64.ergo), `compiler/nativeobj/*` | `amd64` instruction encoder and buffer layout seed `compiler/objfmt/` (ELF64/Mach-O64); `goobj.ergo` (`\x00go120ld`), Go stack-maps, and `R14` register conventions freeze in Stage-0 and delete in `NB-7` |
| Native Lowering | `compiler/lower/`, `compiler/native/` | `compiler/nativelower/` adds Perceus RC insertion, `own` unique reuse, arena lowering, reduction checkpoints, and C-ABI calling conventions |
| C FFI Generator | [`compiler/bindc/abi.ergo`](../../../compiler/bindc/abi.ergo), `compiler/bindc/*` | Retains C11 header/contract parser and SysV/AAPCS64 classifier; emits direct native C-ABI calls instead of `purego` Go trampolines |
| TS / RPC Interop | `internal/tsproj`, `cmd/ergo/tsmap.go`, `tools/ts/` | `tsproj` remains for network/IPC RPC; `compiler/tsffi/` adds in-process `bun:ffi`, Node N-API, `.d.ts`, and `.wasm` glue |
| Driver & CLI | `compiler/driver/`, `compiler/cmd/ergo/` | Eliminates `GOCACHE`, `go tool compile`, `go tool link`, and Go pin management after Stage-1 cutover |

### Two-Tier Execution Model

1. **Dev / Test Tier (Default during development):**
   - Pure tests, laws, and mutant suites with no foreign OS/FFI dependencies execute immediately inside [`compiler/eval`](../../../compiler/eval/vm.ergo) (`<50ms` incremental loop, zero backend compile or link).
   - Native tests (`--native`/FFI/actor/I/O) and `ergo run` / `ergo build` lower through `compiler/nativelower/` directly into `compiler/objfmt/`, emitting standard ELF64 (`.o`/executable/`.so`) or Mach-O64 (`.o`/executable/`.dylib`) bytes in memory with **zero `goobj`, zero Go stack-maps (`PCDATA`/`FUNCDATA`), zero write barriers, zero `GOCACHE`, and zero `toolchain/ir` patched Go compiler**.
   - Emits standard DWARF `.debug_line` / `__debug_line` and `.eh_frame` / `__unwind_info` tables so `lldb`, `gdb`, `perf`, and crash backtraces resolve directly to `.ergo` file:line:col spans.
2. **Release Optimizing Tier (`ergo build --release` and `--platform wasm32`):**
   - Consumes the identical typed SSA IR and checked optimization facts (`given`/`gives`/`keeps`, `own` uniqueness, proved laws, loop fusion) through an optimizing SSA backend (Cranelift, QBE, LLVM, or Ergo's native SSA pass pipeline) with register allocation, instruction scheduling, SIMD auto-vectorization/kernels, dead-code elimination, and LTO.
   - Targets `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, and `wasm32`.
   - Every optimization preserves identical semantics to Dev Tier and `compiler/eval`, enforced by seeded cross-tier agreement suites.

---

## Binary ABI and Value Representation

The binary contract between compiler codegen (`compiler/nativeabi/`) and the C runtime (`runtime/native/ergo_rt.h`) uses standard platform C ABIs with zero custom runtime register reservations.

### Platform Calling Conventions

- **Linux x86_64:** System V AMD64 ABI (`RDI, RSI, RDX, RCX, R8, R9` for integer/pointer arguments; `XMM0..XMM7` for float/SIMD; `RAX, RDX` / `XMM0, XMM1` returns; 16-byte stack alignment at call). No `R14` goroutine pointer; no Go stack split prologue.
- **Linux & macOS arm64:** AAPCS64 (`X0..X7`, `V0..V7`, Homogeneous Floating-point Aggregates in `V0..V3`, Darwin variadic stack spill rules — matching the classification already modeled in [`compiler/bindc/abi.ergo`](../../../compiler/bindc/abi.ergo)).
- **Effect & Task Context:**
  - Pure Ergo functions (`needs` empty, no `fails`) have **exact C ABI signatures** matching external C functions and `ergo build --lib` exports directly.
  - Effectful functions (`needs R` / fiber-capable) take an explicit hidden first argument `ErgoFiber *fiber` (carrying current actor heap, scoped arena pointer, step/reduction counter, and mask/cancel word).
  - Error-returning functions (`fails E`) return a compact tagged result struct in registers `(uint64_t err_tag, payload)` (or sret pointer for >16B payloads): `err_tag == 0` is success, `err_tag != 0` is a typed failure of `E`. Normal `fails`/`try`/`catch` uses a single register test and branch with **zero DWARF stack unwinding**.

### 16-Byte Perceus Reference-Counted Header (`ErgoHdr`)

Every heap-allocated dynamically sized buffer, shared record box, or escaping closure is preceded by a 16-byte `ErgoHdr` (payload at `hdr + 16`, maintaining 16-byte alignment when slab/arena allocations are 16-byte aligned):

```c
// runtime/native/ergo_rt.h (exact 16-byte ABI layout)
enum ErgoHdrFlags {
    ERGO_F_IMMORTAL   = 1 << 0, // static .rodata literal: rc ops are no-ops
    ERGO_F_ARENA      = 1 << 1, // scoped arena backed: drop is a no-op; O(1) bulk reset
    ERGO_F_ACTOR_HEAP = 1 << 2, // owned by current actor's ErgoActorHeap
    ERGO_F_SHARED     = 1 << 3, // cross-actor immutable/shared: uses atomic rc
    ERGO_F_HAS_DROP   = 1 << 4, // payload holds nested RC fields (consult type_id drop fn)
};

typedef struct ErgoHdr {
    uint32_t rc;          // refcount (>= 1 live; 0xFFFFFFFF if ERGO_F_IMMORTAL)
    uint16_t type_id;     // index into .rodata ErgoTypeDesc table (0 = raw POD/bytes)
    uint16_t flags;       // ErgoHdrFlags bitmask
    uint64_t alloc_bytes; // total allocation size incl. 16B header (for Limits.Bytes & reuse)
} ErgoHdr;

_Static_assert(sizeof(ErgoHdr) == 16, "ErgoHdr must be 16 bytes");
```

**Fast-path semantics:**
- **Static literals (`ERGO_F_IMMORTAL`) & Arena allocations (`ERGO_F_ARENA`):** `ergo_dup` / `ergo_drop` branch once (or are statically omitted when `owner == NULL` / arena-proven) with zero atomic operations and zero heap mutations.
- **Actor-local values (`ERGO_F_ACTOR_HEAP`, `!ERGO_F_SHARED`):** `rc++` and `--rc` are plain non-atomic `ADD`/`SUB` instructions because each actor owns an isolated heap with single-writer execution.
- **Shared immutable values (`ERGO_F_SHARED`):** use `LOCK XADD` / `LDADDAL`/`STADDL` atomic refcounting.

### Core Aggregate Layouts

| Ergo Type | C ABI Struct (`runtime/native/ergo_rt.h`) | Size | Key Invariants |
| --- | --- | --- | --- |
| `bool`, `int8..64`, `uint8..64`, `float32/64`, `duration` | Native C integer/float (`duration` = `int64_t` ns) | 1–8 B | Passed in GP/FP registers; zero heap header |
| `string` (`ErgoStr`) | `{ const uint8_t *ptr; uint64_t len; ErgoHdr *owner; }` | 24 B | Static `.rodata` strings set `owner = NULL` (zero RC); substrings `s[a:b]` slice `ptr`/`len` and share `owner` zero-copy; `(ptr, len)` passes straight to C FFI |
| `[]byte` (`ErgoBytes`) | `{ uint8_t *ptr; uint64_t len; uint64_t cap; ErgoHdr *owner; }` | 32 B | Same layout as `ErgoSlice` with element size 1 |
| `[]T` (`ErgoSlice`) | `{ void *ptr; uint64_t len; uint64_t cap; ErgoHdr *owner; }` | 32 B | Interior reslice `xs[a:b]` shares `owner`; `Append` mutates backing buffer in-place when `owner != NULL && owner->rc == 1 && ptr == (uint8_t*)owner + 16` |
| `Map[K, V]` | `{ void *table; ErgoHdr *owner; }` (or header-inline Swiss/open-addressing table) | 16 B | Value semantics: `Put`/`Delete` mutate in place when `rc == 1` (`own` or dead local), else copy-on-write |
| Closed union (`type U = A \| B{...}`) | `{ uint32_t tag; ...shared typed slots... }` | Variable | Follows [docs/spec/types.md](../../spec/types.md#unions) rule 6 flat shared-slot packing; boxed under `ErgoHdr` only if recursive or exceeding inline slot threshold (128 B) |
| Niche `Option[P]` | `void *ptr` (`NULL` = `None`, non-null = `Some`) | 8 B | Zero tag overhead when payload is a non-null pointer, `ErgoHdr*`, or opaque C handle |
| `fails E` return | `{ uint32_t err_tag; uint32_t pad; payload_or_err; }` | 16 B+ | `err_tag == 0` means success (`payload`), `err_tag == i` names variant `i` of closed failure row `E` |
| `func(...)` (`ErgoClosure`) | `{ void *fn_ptr; ErgoHdr *env; }` | 16 B | Non-capturing functions set `env = NULL` (zero allocation, zero RC); escaping closures store captures under `ErgoHdr` |

### `own` Unique-Reuse Rules (Perceus)

Following Ergo's value semantics ([docs/spec/types.md](../../spec/types.md#bindings)) and Perceus reuse analysis:

1. **Deterministic last-use drops:** `compiler/nativelower/` tracks every binding's liveness across control flow (including early `return`, `try` failure edges, and loop exits). No `dup` is emitted on move/last use; `drop` is inserted immediately after the last read, never deferred to function exit unless held by a live borrow.
2. **Statically guaranteed reuse (`own` parameter / dead fresh local):** When a value is `own`-qualified or provably unshared (`rc == 1` by construction), mutating constructs (`xs = xs.Append(v)`, `T{..r, f: e}`, `m = m.Put(k, v)`, `match` arm reconstruction) rewrite the backing buffer or heap slot **in place** with zero `rc` branch and zero allocator round-trip.
3. **Dynamic uniqueness fallback (`rc == 1` check):** Where static uniqueness is unknown across branches or aliases, codegen emits:
   ```c
   if (hdr != NULL && !(hdr->flags & (ERGO_F_IMMORTAL | ERGO_F_ARENA)) && hdr->rc == 1) {
       // reuse hdr storage directly (zero malloc/free)
   } else {
       // allocate replacement (or realloc) and drop old reference
   }
   ```
   When `hdr->alloc_bytes` matches or exceeds the target record/slice capacity, the allocation is reused in place with zero allocator round-trip.

*Prior art:* Koka's Perceus algebraic reference counting and Lean 4's `rc == 1` in-place constructor/array reuse (without tracing GC); Swift's ARC copy-on-write uniqueness checks (`isKnownUniquelyReferenced`). Unlike Swift and Objective-C, Ergo values cannot form arbitrary cyclic RC graphs because mutability follows value semantics and explicit graph structures use generational handles in [`std/arena`](../../../internal/sketch/std/arena/arena.ergo).

---

## Memory Tiers

Ergo partitions memory across four explicit tiers so normal code pays zero GC pause, zero write barriers, and predictable cache-local layouts:

| Tier | Mechanism | Typical Use | Reclamation Cost |
| --- | --- | --- | --- |
| **1. Stack & DoD Dense Tables** | Registers, stack frames, flat unions, [`std/dense`](../../../internal/sketch/std/dense/dense.ergo) SoA columns (`[]uint32`, `[]uint8`) | Compiler AST/type stores, numeric kernels, state machines, small records | O(1) stack unwind / single contiguous buffer drop (`ERGO_F_HAS_DROP == 0`) |
| **2. Perceus RC + `own` Reuse** | `ErgoHdr` 16B header + compile-time last-use `drop` + `rc == 1` in-place reuse | Strings, slices, `Map`, closures, boxed recursive ADTs, `--lib` exported values | Deterministic at last use; zero allocator cost on `own` reuse |
| **3. Scoped O(1) Bump Arenas** | `ErgoArena` slab bump pointer (`cur += aligned_size`) | Request-scoped HTTP/RPC parsing, compiler pass scratch, non-escaping frame temporaries | Single O(1) cursor reset on scope exit; zero per-object `drop` |
| **4. Per-Actor Bounded Heaps** | `ErgoActorHeap` isolated slab allocator per `actor` / `process.Run` | Long-lived actor state, bounded actor execution, isolated untrusted/plugin sub-tasks | Exact `Limits.Bytes` accounting; **O(1) whole-heap slab reclamation** on `die`/`OverBytes`/`OverSteps` |

### Scoped O(1) Bump Arenas

- When `compiler/nativelower/` proves an allocation cannot escape a lexical scope, request handler, or function frame (by `escaping`/`own` rules and `compiler/check/facts`), it lowers the allocation to `ergo_arena_alloc(&arena, size, align)` instead of `ergo_heap_alloc`.
- Arena allocations carry `ERGO_F_ARENA` in `ErgoHdr.flags` (or omit `ErgoHdr` altogether when non-escaping and never converted to a dynamic slice/closure).
- Exiting the scope resets the arena cursor in O(1) (`arena->cur = saved_mark`) and runs destructors only for registered `Closer` OS handles (`fd`, sockets) if any, never scanning pure data.

### Per-Actor Bounded Heaps (`Limits.Bytes`) & `die` Isolation

Extending [`std/process`](../../../internal/sketch/std/process/process.ergo) (`Limits{Time, Steps}` -> adding `Bytes int` and failure `OverBytes { Limit int }`) and `std/actor`:

1. **Isolated slab allocator:** Every `actor` and `process.Run` owns an `ErgoActorHeap` backed by 64 KiB–2 MiB OS slabs (`mmap`/`VirtualAlloc`/pool slabs). No two actors share mutable heap slabs.
2. **Exact accounting:** Every allocation on `heap` increments `heap->live_bytes += hdr->alloc_bytes` and verifies `heap->live_bytes <= heap->limit_bytes` (when `limit_bytes > 0`). Every freed `ErgoHdr` decrements `heap->live_bytes -= hdr->alloc_bytes`.
3. **`OverBytes` enforcement:** If an allocation exceeds `limit_bytes`, the actor does not abort the host process. The runtime trips an actor-local fault (`OverBytes{Limit}`), executes masked exit cleanup (`Closer` OS handles) with a bounded cleanup reserve, and delivers `OverBytes` cleanly to the supervisor or `process.Run` caller.
4. **O(1) crash reclamation:** On any actor termination — normal `Done`, `die`/defect (`Unreachable`, assertion fault), `OverBytes`, or `OverSteps` — the scheduler returns every slab in `actor->heap` to the OS/slab pool in O(1) time without walking individual objects.
5. **Cross-actor message passing:** Sending a message (`Send`, `Ask`, `Reply`) across actor boundaries either transfers a uniquely owned (`own` / detached `rc == 1`) slab-independent buffer or deep-copies the payload into the receiver's `ErgoActorHeap` (or shares static `.rodata` / `ERGO_F_SHARED` immutable handles), preserving strict heap isolation and zero cross-actor pauses.

*Prior art:* Erlang/OTP BEAM's per-process isolated heap and O(1) process exit reclamation (extended with explicit `Limits.Bytes` hard caps and Perceus `own` transfer instead of tracing per-process minor GC).

---

## Fiber and Actor Scheduler (`runtime/native/`)

Ergo maps structured concurrency (`task.Go`), actors (`std/actor`), and bounded processes (`std/process`) onto a cooperative M:N fiber scheduler:

- **Worker Pool & Fibers:**
  - `N` OS worker threads (defaulting to available physical/cgroup cores, configurable via runtime flags) execute `M` lightweight fibers (`ErgoFiber`).
  - Each `ErgoFiber` holds: stack chunk (guard-paged native stack or segmented/pool stack), saved callee-saved registers (`x86_64` SysV / `arm64` AAPCS64), `int64_t steps_remaining`, `ErgoActorHeap *heap`, `ErgoArena *arena`, cancellation/mask state (I1/I2), and a local lock-free Chase-Lev work-stealing deque.
- **OS Event Reactors:**
  - **Linux:** **`io_uring`** (`io_uring_enter` batched SQE/CQE ring) handling non-blocking TCP/Unix sockets (including `SCM_RIGHTS` FD passing), timers (`IORING_OP_TIMEOUT`), files, pipes, signals (`signalfd`), and cross-thread `eventfd` wakes.
  - **macOS:** **`kqueue`** (`kevent64`) handling BSD sockets, pipes, `EVFILT_TIMER`, `EVFILT_SIGNAL`, and `EVFILT_USER` wakes.
  - **WASM (`wasm32`):** Single-threaded cooperative trampoline bridged to JS Promises / WASI `poll_oneoff`.
- **Native Reduction Counting (`Limits.Steps`):**
  - Each fiber decrements `fiber->steps_remaining` at non-leaf function prologues and loop back-edges (a single register/TLS decrement + `js`/`jle` slow-path call, costing ~1 cycle when non-zero).
  - **Cooperative preemption:** A fiber starts each scheduler slice with a cooperative quantum (e.g., `2000` reductions). When `steps_remaining <= 0` and no `Limits.Steps` cap is exhausted, the slow path yields to the worker deque and reschedules fairly so tight compute loops never starve peer actors or I/O completions.
  - **Exact `Limits.Steps` budget:** Inside a budgeted `process.Run` (`Limits{Steps: N}`), reaching `0` total reductions triggers `OverSteps{Limit}`, runs masked exit cleanup (I2), and returns `OverSteps` to the parent without disturbing peer actors.
- **Deterministic Exploration (`--explore`):**
  - `ergo test --explore [dfs|pct|random]` swaps the scheduler's ready-queue pick and event-settle hooks through a test-build scheduler vtable (`ErgoSchedHooks`), preserving full deterministic schedule replay and token reproduction (`--schedule TOKEN`) on native binaries.

---

## FFI and Interop Symmetry

Ergo provides one symmetric, zero-GC FFI boundary in both directions, plus RPC for network seams:

```text
  Outbound (Ergo -> C / Rust / Zig / OS)        Inbound (Bun / Node / Swift / Go / Browser -> Ergo)
  ---------------------------------------        ---------------------------------------------------
  C header + contract.json                       Checked Ergo package (compiler/check/summary)
           |                                              |
           v                                              +--------------------+--------------------+
  [compiler/bindc]                                        |                    |                    |
  (ergo bind c HEADER CONTRACT)                           v                    v                    v
           |                                     ergo build --lib      --platform wasm32     ergo tsmap /
           v                                     (.so/.dylib + .h)     (.wasm + ESM glue)    std/rpc
  extern c declarations                                   |                                         |
  -> direct SysV/AAPCS64 CALL                    [compiler/tsffi]                            [internal/tsproj]
  (zero purego trampoline)                       -> bun:ffi bindings                         HTTP/NDJSON/WS
                                                 -> Node N-API addon                         network RPC
                                                 -> strict .d.ts
```

### Outbound: `extern c` via `compiler/bindc/`

- Existing [`compiler/bindc/`](../../../compiler/bindc/abi.ergo) parses the strict C11 header subset, evaluates constant macros on LP64 rules, verifies struct offsets/alignments, and classifies pointer ownership (`Value`, `InBuffer`, `OutBuffer`, `Handle`, `Callback`) via `CONTRACT` JSON.
- In native builds, `compiler/nativelower/` lowers `extern c` calls **directly to standard C ABI calls** (`SysV AMD64` / `AAPCS64` / `wasm32`) — eliminating the Stage-0 `purego` Go reflection/trampoline layer (`internal/bindc/emitgo.go`) entirely.
- `ErgoStr` and `ErgoBytes`/`ErgoSlice` pass `(ptr, len)` straight into C buffers with zero copy when annotated `reads` / `in_buffer`.
- Enables zero-overhead native linking to SQLite, DuckDB, `libvips`, OpenTUI, BoringSSL/`rustls`, OS syscalls, and C-ABI libraries written in Rust or Zig.

### Inbound: `ergo build --lib` + `compiler/tsffi/` + `.wasm`

- **Shared/Static Native Libraries (`ergo build --lib -o DIR`):**
  - Emits compact `.so` (Linux), `.dylib` (macOS), or `.a` static library exposing flat C ABI symbols (`ergo_<pkg>_<fn>`) alongside a generated C header (`.h`).
  - **Zero runtime conflicts:** Contains no Go runtime, no background GC threads, and no `SIGURG`/`SIGSEGV` signal handlers — safe to `dlopen` inside Bun, Node, Swift, Python, Rust, C++, or Go binaries.
  - Exposes `ergo_rt_free` / `ergo_str_drop` / `ergo_slice_drop` helpers in the header so foreign callers can release owned return buffers deterministically, or accepts caller-provided output buffers.
- **Auto-Generated TypeScript Bindings (`compiler/tsffi/`):**
  - Reads checked Ergo signatures, records, closed unions, nominal types, and `fails` rows from `compiler/check/summary`.
  - Emits:
    1. **`bun:ffi` wrapper (`*.bun.ts`):** zero-copy `ptr`/`CString` marshaling of strings and `Uint8Array` views, decoding closed failure tags directly into discriminated TS result unions (`{ ok: true, value } | { ok: false, error }`).
    2. **Node N-API bridge (`*_napi.c` + `*.node.ts`):** builds or loads a thin N-API wrapper matching the identical TypeScript API surface.
    3. **Strict `.d.ts` declarations:** preserves closed discriminated unions, branded/nominal wrappers, and `Option` without `any` escape hatches.
- **Browser & Edge `.wasm` (`ergo build --platform wasm32`):**
  - Emits compact `.wasm` binaries (linear memory + Perceus RC + bump arena; no Go runtime megabytes) plus typed `.d.ts` + ESM loader glue for browser and edge front-ends.
- **Network / IPC Boundaries (`tsproj`):**
  - `internal/tsproj` (`ergo tsmap` / `std/rpc`) stays the contract-driven HTTP/NDJSON/WebSocket RPC generator whenever the boundary crosses a network or OS process boundary rather than in-process memory.

### Stage-0 Go Interop Disposition

- `import go`, `extern go`, `internal/gobind/`, `internal/sketch/std/gostd/`, and `go.Ref`/`go.New`/`go.Zero`/`go.Any` remain **Stage-0 bootstrap only**.
- No new language features, runtime shims, or std packages depend on Go packages that cannot be expressed in pure Ergo + `extern c` once `NB-5` lands.

---

## Native Floor Optimizations & Boundary Policies

Five compiler and runtime levers lower the native floor without weakening contracts (`NB-8`–`NB-10` in [units.md](units.md)):

1. **Law-Driven Optimization (`NB-8`):** Verified `given`/`gives`/`keeps` invariants (`0 <= i && i < xs.Len()`, equal-length columns) eliminate bounds checks and unlock auto-SIMD cleanly; proved `Associative`/`Identity`/`Fuse` interface laws fuse chained pipelines and SIMD-tree-reduce associative folds; pure (`needs` empty) calls hoist and parallelize safely.
2. **Whole-Region Provider Devirtualization & Verified ISA Providers (`NB-9`):** Lexical `with context` regions specialize/devirtualize and inline the concrete provider across entire call trees (zero vtable overhead at runtime while preserving clean mockability); SIMD kernels (`AVX-512`/`AVX2`/`NEON`/scalar) implement one checked provider interface and are verified bit-identical to the pure scalar model via Ergo laws.
3. **Transparent `AoS -> SoA` Packing (`NB-10`):** Because Ergo's value semantics never hand out interior element or field pointers, `SoA[T]`/`DenseTable[T]` containers store fields in contiguous cache-line columns with stride-1 vector scans and hot/cold field splitting under ordinary record syntax.
4. **Actor-Turn Reset Arenas & Zero-Copy `own` Teleports (`NB-10`):** `Send(own buf)` transfers an `ErgoHdr` slab allocation between `ErgoActorHeap` owners in O(1) (decrementing sender `live_bytes`, crediting receiver `live_bytes`, zero copy, zero lock); actor `Step` handlers execute inside a per-turn scratch arena reset with one pointer write on exit.
5. **Static `noalloc` Floor Proofs & Compute Providers (`NB-8`, `NB-9`):** Static Perceus/escape facts enforce `noalloc` / `own`-reuse checks on marked hot loops at compile time; pure functions (`needs` empty) plus proved `Associative` laws run interchangeably in `compiler/eval`, local SIMD worker pools, or sharded workers via a `Compute` provider.

### Companion Policy & Specialization Rules

- **Boundary-scoped memory policy providers:** Ordinary functions take no `mem.Alloc` capability and have zero GC knobs; boundaries (handlers, parsers, tests) enter explicit scoped bump arenas, enforce `Limits.Bytes`, inject deterministic alloc faults / zero-alloc checks, or bind opt-in backing pools (`std/arena`, `io_uring` registered buffers, `mmap`).
- **Zero-annotation `--explore-faults`:** Replays test executions across compiler-known `needs` failure edges (`fails` rows, `OverBytes`, I1/I2 stop) to verify cleanup without manual failpoint hooks in production code.
- **Typed static specialization (no Zig-style `comptime`):** Closed-union subsets compose upward from standalone nominal variant types; conditional code uses typed `const` dead-code elimination (disabled branches still typecheck); platform backends bind via `needs` + `under <platform>`; compile-time tables use pure `const` eval and `derive`.

---

## Stage-0 -> Stage-1 -> Stage-2 Bootstrap & Go Deletion Criteria

| Stage | Compiler Host | Backend Used | Purpose & Exit Criteria |
| --- | --- | --- | --- |
| **Stage-0 (Current Frozen Bridge)** | Go-compiled `bin/ergo` | Go toolchain + `toolchain/ir` + `runtime/v1` + `runtime/rt` | Keeps CI/tests running while `NB-1`..`NB-6` are built; Go text path and runtime shims are frozen |
| **Stage-1 (First Native Compiler)** | Stage-0 `bin/ergo` compiles `compiler/cmd/ergo` via `NB-1..NB-6` | Native ELF64/Mach-O64 (`compiler/nativelower` + `compiler/objfmt`) + `runtime/native` | Produces standalone native binary `ergo-stage1` linking zero Go runtime (`ldd`/`otool` shows libc/libSystem only) |
| **Stage-2 (Self-Hosted Fixpoint)** | `ergo-stage1` compiles `compiler/cmd/ergo` natively | Native ELF64/Mach-O64 (`compiler/nativelower` + `compiler/objfmt`) + `runtime/native` | Produces `ergo-stage2`; **`ergo-stage2` builds `ergo-stage3` byte-identically** (`sha256(stage2) == sha256(stage3)`, and `sha256(stage1) == sha256(stage2)` when Stage-0 drives identical linked native emission) with **zero Go toolchain on `PATH`** |

### Go Deletion Criteria (`NB-7`)

The Stage-0 Go backend and Go-interop surface are deleted **in full** once all of the following hold simultaneously:

1. **Identical native bootstrap:** `ergo-stage1 -> ergo-stage2 -> ergo-stage3` completes natively on both Linux (`amd64`/`arm64`) and macOS (`arm64`) with deterministic byte-identical binaries and zero Go toolchain invocation.
2. **Full suite parity under `ERGO_FALLBACK=off`:** Every std package law suite (`std/*`), every example program (`examples/*`), the conformance suite, `modules/*`, and `--mutants` passes on the native runtime (`runtime/native/`).
3. **Deleted Stage-0 trees (`NB-7`):** Remove `toolchain/ir/`, `internal/gobind/`, `internal/sketch/std/gostd/`, `internal/sketch/lower/`, `runtime/v1/`, `runtime/rt/`, `internal/linked/`, `compiler/nativeobj/goobj.ergo`, and Go-specific CLI/language forms (`import go`, `extern go`, `go.Ref`, `go.New`, `go.Zero`, `go.Any`, `go.Error`, `go.Foreign`).
4. **Core spec budget (`<250 KB`):** Remove retired Go-interop sections from `docs/spec/` (primarily `docs/spec/interop.md` Go rules and Go-runtime internals across `docs/spec/*.md`) and land surface-diet units so total byte size of `docs/spec/*.md` excluding `docs/spec/stdlib.md` is **`< 250 KB`** (`< 256,000` bytes).
