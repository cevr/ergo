# Ergo Native Backend Architecture

This document defines Ergo's end-to-end native execution architecture following the [2026-11-27 Native Backend Pivot](../../decisions.md#native-backend-pivot-elfmach-o-perceusarenas-c--bun-ffi-2026-11-27) and [NORTH_STAR.md](../../../NORTH_STAR.md).

Ergo replaces Go as its product runtime and object format with direct native code generation (ELF64 on Linux, Mach-O64 on macOS, `.wasm` on browser/edge) over zero-GC deterministic memory management (Perceus RC + compile-time `own` unique reuse, scoped O(1) bump arenas, and BEAM/OTP per-actor bounded heaps) and standard platform C ABI interoperability. Go remains **Stage-0 bootstrap only** until self-hosted Ergo passes identical `Stage-1 -> Stage-2` native bootstrap in [`NB-7`](units.md#nb-7-stage-1---stage-2-native-bootstrap--stage-0-go-deletion).

Related documents:
- [units.md](units.md) — ordered vertical implementation slices (`NB-1` through `NB-12` and their lettered sub-units), owned paths, and verification gates
- [Budgets and settled forks](#budgets-and-settled-forks-2026-11-27) — compile-time, store, actor-density and RC budgets, each with the unit whose gate carries it
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
                      x86_64/arm64 baseline       native SSA + SIMD + LTO;
                                                  decided: Ergo's own)
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
   - Consumes the identical typed SSA IR and checked optimization facts (`given`/`gives`/`keeps`, `own` uniqueness, proved laws, loop fusion) through Ergo's own optimizing SSA backend, `compiler/ssaopt` ([decided 2026-11-27](#release-optimizer-ergos-own-compilerssaopt)), with register allocation, instruction scheduling, SIMD auto-vectorization/kernels, dead-code elimination, and LTO.
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

1. **Isolated slab allocator:** Every `actor` and `process.Run` owns an `ErgoActorHeap` backed by 64 KiB–2 MiB OS slabs (`mmap`/`VirtualAlloc`/pool slabs), after a 256 B first chunk from a shared size-classed pool ([actor density](#fiber-stacks-and-actor-density)). No two actors share mutable heap slabs.
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
  - Each `ErgoFiber` holds: a pooled, contiguous, fixed-reserve stack held only while a task or turn is parked mid-function ([decided 2026-11-27](#fiber-stacks-and-actor-density)), saved callee-saved registers (`x86_64` SysV / `arm64` AAPCS64), `int64_t steps_remaining`, `ErgoActorHeap *heap`, `ErgoArena *arena`, cancellation/mask state (I1/I2), and a local lock-free Chase-Lev work-stealing deque.
- **OS Event Reactors:**
  - **Linux:** **`io_uring`** (`io_uring_enter` batched SQE/CQE ring) handling non-blocking TCP/Unix sockets (including `SCM_RIGHTS` FD passing), timers (`IORING_OP_TIMEOUT`), files, pipes, signals (`signalfd`), and cross-thread `eventfd` wakes.
  - **macOS:** **`kqueue`** (`kevent64`) handling BSD sockets, pipes, `EVFILT_TIMER`, `EVFILT_SIGNAL`, and `EVFILT_USER` wakes.
  - **WASM (`wasm32`):** Single-threaded cooperative trampoline bridged to JS Promises / WASI `poll_oneoff`.
- **Native Reduction Counting (`Limits.Steps`):**
  - Each fiber decrements `fiber->steps_remaining` at non-leaf function prologues and loop back-edges (a single register/TLS decrement + `js`/`jle` slow-path call, costing ~1 cycle when non-zero).
  - **Cooperative preemption:** A fiber starts each scheduler slice with a cooperative quantum (e.g., `2000` reductions). When `steps_remaining <= 0` and no `Limits.Steps` cap is exhausted, the slow path yields to the worker deque and reschedules fairly so tight compute loops never starve peer actors or I/O completions. A fiber inside an `extern c` call is not interrupted; its worker's queue is handed to another thread ([Preemption and blocking foreign calls](#preemption-and-blocking-foreign-calls)).
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

## Budgets and Settled Forks (2026-11-27)

The owner's goal, verbatim (2026-11-27): "what I really desire is basically fast a very fast language as fast as possible to the floor a language with guarantees, you know, whether it be memory, whether it be types and safety like that we're doing with Ergo with a kind of like beam/OTP inspired architecture and et cetera, all the things that we've planned and fast compile times, like very fast compile times, sub-second minimum, ideally, you know, sub millisecond and no huge caches, you know, having a like the kind of the shared native work tree binaries that we're doing, not having to have like a hundred or two hundred gigabytes like Rust, like two orders of magnitude below that, you know, even three if we can".

This section turns that goal into budgets that a unit's gate carries, and settles the forks the sections above left open. Every budget is a count (user-mode instructions of the process tree, counted work, or bytes), never wall time ([AGENTS.md](../../../AGENTS.md), Tests). The decision record with its evidence is [docs/decisions.md](../../decisions.md#native-backend-budgets-and-settled-forks-2026-11-27); the carrying units are in [units.md](units.md#budget-and-fork-units-2026-11-27).

**Conventions.**

- **The subject** is the self-hosted compiler: the package closure of `compiler/cmd/ergo`. Measured at `2c72383f6`: 453 `.ergo` files under `compiler/`, 247,872 lines and 7,861,324 bytes without tests, about 8,600 top-level functions.
- **One second is 3,000 M `instructions:u`; one millisecond is 3 M.** This is the recorded conversion of the owner's times into the repository's unit (one core at 3 GHz and one instruction per cycle, the conservative end). A budget is the whole process tree's total, so more cores never hide work. A warm `ergo` process start alone is 2.9 to 3.0 M ([compile-speed receipt](../../receipts/compile-speed.md), "Process start"): the millisecond budget can only be met inside a resident process.
- **A target** is the owner's number. **A first step** is what the carrying unit's gate enforces when it lands. Between them the measured value is a ratchet: a landing may not raise it.

### Build budgets

| Budget | Target (owner) | First enforceable step | Gate |
| --- | --- | --- | --- |
| Cold `ergo build` of the subject, Dev tier, empty store | tree total <= 3,000 M instr: front (parse, check, IR) <= 1,950 M, backend (`compiler/perceus`, `compiler/nativelower`, `compiler/objfmt` encode) <= 800 M, link and write <= 250 M | Counted work at small sizes: each IR instruction is visited at most 3 times by the backend (`lower.visits <= 3 * ir.insts`), every function is encoded once, no stage reads a file twice. The tree total and the three cells are recorded and ratcheted. | `NB-4` (counted work), `NB-7` (the instruction ceilings, on `ergo-stage2`) |
| Rebuild after a one-function body edit, in the resident compiler | <= 3 M instr on the daemon for the request (one millisecond) | Counted work: 1 file read, 1 declaration checked, 1 function lowered, 1 function's bytes patched, 0 other functions touched, 0 processes started. | `NB-11` (counted work), `NB-7` (the 3 M ceiling) |
| Link of the subject | cold <= 250 M instr; after a one-function edit: bytes written <= the function's new code plus its relocations and line-table rows | In-process link (0 processes started), work linear in relocations; the patched executable runs the same as a fresh one. | `NB-1` (in-process link), `NB-11` (patch) |

- **The dev tier links in process.** `compiler/objfmt` lays out and writes the executable itself; `ld.lld` and `ld64` stay for `--release`, for `--lib` where a platform demands it, and as a cross-check. A linker process cannot meet the budget: wild and mold each spend about 600 M instructions linking ripgrep ([wild BENCHMARKING.md](https://github.com/davidlattimore/wild/blob/main/BENCHMARKING.md): 596.8 M and 638.4 M), twice the whole link cell, and neither links incrementally (mold's author rejected it; wild plans it).
- **Two layouts.** A dev build leaves padding after each function and calls through stable addresses so that an edit rewrites one function in place (Zig's incremental design: its self-hosted backends exist "because we can dramatically outperform LLVM at compilation speed", and incremental compilation "will allow small changes to rebuild in milliseconds", [Zig devlog 2025-06-08 and 2025-06-14](https://ziglang.org/devlog/2025/)). `ergo build --fresh` and every `--release` build write the canonical dense layout; the byte-identical fixpoint of `NB-7` is stated on the canonical layout only.
- **Reference point.** Zig builds its own compiler with its own x86-64 backend in 191 G instructions and 10 s (same devlog); the Ergo target is 64 times fewer instructions for a compiler a fraction of the size. Stage 0 today: the first build of a ten-package program is 18,777 M instr and a body edit 6,409 M ([compile-speed receipt](../../receipts/compile-speed.md)).

### Store and artefact budget

Measured today (2026-11-27, `du -sh` of the cache root's folders on the workbox): `go` 60 G, `modules` 30 G, `toolchain` 21 G, `std` 14 G, `irc` 2.5 G, `gomod-examples` 557 M, `laws` 106 M, the rest under 50 M: about 128 GB, which is the size the owner names for Rust.

| Budget | Target (owner) | First enforceable step | Gate |
| --- | --- | --- | --- |
| Every Ergo artefact on one machine, all repositories and work trees | <= 10 GiB by default (owner, 2026-11-27: "Larger: 10 GiB"; settable with `ERGO_CACHE_MAX`); the store refuses to grow past its cap and evicts the least recently used entries at write time | The cap law on the native artefact classes as they appear (objects, function code, checked tables, linked images): bytes in the store <= cap at every quiescent point of a seeded sequence of builds and trims; an entry a running build holds is never evicted. | `NB-12` |
| The subject's scenario: cold build, `ergo test` of its packages, then ten workspaces each with one body edit and a rebuild | cache root <= 200 MiB (three orders below the 200 GB the owner names; derived from that figure, not from the machine cap, so the 10 GiB answer does not loosen it) | The scenario's bytes are recorded per class and ratcheted; a second workspace adds only the bytes of what differs (content-addressed, function-granular). | `NB-12` (scenario), `NB-7` (the ceiling, once the Go classes are deleted) |
| Inside a work tree | 0 bytes of Ergo artefacts other than the outputs the command names; an output is a clone of the store's image where the file system clones | Counted: files written in the work tree per build = the named outputs. | `NB-12` |

The Go classes (`go`, `toolchain`, `modules`, `std`, `irc`: 127.5 of the 128 GB) are Stage-0 and are deleted in `NB-7`; no native unit may add a class that is not content-addressed in the one store.

### The resident compiler

The compiler is a service: `ergo dev`, the daemon that exists ([tooling](../../tooling.md), "ergo dev": one per workspace and release, socket and lock under `dev/` in the cache root, lifecycle model `models/dev`). Today it answers only a warm check from memory and holds no checked program. `NB-11` makes it hold the checked tables, each function's semantic hash and each function's machine code, recompile only the functions whose hash changed, and patch the dev-layout output in place. `ergo build`, `ergo run` and `ergo test` start it by themselves (owner, 2026-11-27: "Yes, start it automatically"); `ERGO_DEV=off` refuses. It is the same daemon, never a second one (the model's first invariant), and the CLI stays its client with the in-process answer as the fallback. Its laws: the daemon's answer equals the cold in-process answer for every seeded edit sequence, and the counted work of the build-budget table.

### Release optimizer: Ergo's own (`compiler/ssaopt`)

Decided: the Release tier is an SSA optimizer written in Ergo. Cranelift and QBE are rejected; LLVM is a measuring control (`clang -O2` on the matched C kernel), never a linked dependency and never on the bootstrap path.

| Criterion | LLVM | Cranelift | QBE | Ergo's own |
| --- | --- | --- | --- | --- |
| Carries `given`/`gives`/`keeps`, `own`, proved laws | Partly, by translation to `llvm.assume`, `noalias`, range metadata; a law-driven fusion or a tree reduction must be done before it | No such input | No such input | Native: the facts are on the IR it reads |
| Build time | Zig measured `hello` at 8.50 G instr through LLVM against 3.21 G through its own backend, and its compiler at 75 s against 20 s | "approximately an order of magnitude faster than" LLVM (its README) | "super quick" | Ours to budget and ratchet |
| Dependency weight | A C++ toolchain in the compiler and in the bootstrap | A Rust toolchain in the bootstrap | C, 10% of the code of an industrial backend, but it writes assembly text: needs an assembler process | None: self-hosted, under Ergo's own laws and mutants |
| Code quality | The reference | "~14% slower than" LLVM on Wasm workloads (its README, citing arXiv 2011.13127); built for JIT compile speed | Aims at "70% of the performance of industrial optimizing compilers"; no SIMD, no inlining | To be measured: `NB-6C` |

The guarantee that rules out a linked external backend is the `NB-7` fixpoint with no foreign toolchain, and the owner's cache and compile-time goal. What stays open is measured, not asked: `NB-6C` builds the optimizer's first passes and counts instructions against `clang -O2` on the repository's matched kernels. Its acceptance is a ratio per kernel class (<= 1.10 of the control); a class that loses names the missing pass as a unit. Only if a class still loses by more than 1.25 after its named passes land does an optional emitter of LLVM IR text for an external `clang` open, outside the bootstrap.

### Fiber stacks and actor density

Decided, against a stated capacity: **1,000,000 live actors on one node, 100,000 of them parked in the middle of a turn.**

- **An idle actor has no stack.** An actor between turns is its state, its mailbox and its heap. A turn borrows a stack from its worker's pool and gives it back when the turn returns. Only a task or a turn that parks mid-function keeps its stack.
- **Stacks are pooled, contiguous and fixed in reserve.** A stack is carved from a large mapped region, 256 KiB of address space by default, PROVISIONAL until `NB-5D` measures real programs (owner, 2026-11-27: "fixed but should mwasure with real programs"; a spawn may state another reserve), committed by the kernel only as pages are touched, and handed back to the pool with its pages released. Overflow is caught by a limit compare in the prologue of a non-leaf function, next to the reduction decrement, so no stack needs its own guard mapping: a guard page per stack is two mappings per stack, and 100,000 parked fibers would pass Linux's default `vm.max_map_count` of 65,530. Past its reserve a task ends with the `StackOverflow` defect, which ends that actor only.
- **Not segmented, not copied.** Segmented stacks pay at every hot boundary crossing. Growing by copying needs a map of every pointer into the stack, and this backend has no stack maps and passes pointers to stack slots into C.
- **`extern c` runs on the worker's own thread stack**, which is large and guard-paged by the OS, so the fiber reserve never has to fit a C library's frames.
- **Actor heaps start small.** The first chunk of an `ErgoActorHeap` is 256 B from a shared size-classed pool and grows geometrically to the 64 KiB to 2 MiB slabs named above; teardown stays O(1) in the number of chunks, which is logarithmic in the heap's size. (A 64 KiB first slab would cost 64 GB at the stated count.)

| Budget | Number | Reference | Gate |
| --- | --- | --- | --- |
| Runtime-owned bytes per idle actor (struct, mailbox header, first heap chunk; not the user's state past it) | <= 512 B, at 1,000,000 actors | BEAM: 2,616 B (327 words) for a new process ([Erlang efficiency guide, Processes](https://www.erlang.org/doc/system/eff_guide_processes.html)) | `NB-5D` |
| Stack bytes held by an idle actor | 0 | | `NB-5D` |
| Committed bytes per fiber parked at depth 1 | <= one page (4 KiB; 16 KiB on Apple arm64) + 256 B | | `NB-5D` |
| Mappings | <= 64 for 100,000 parked fibers | `vm.max_map_count` 65,530 | `NB-5D` |

### Preemption and blocking foreign calls

- **Ergo code is preempted by count.** The reduction decrement at prologues and back-edges is compiler-inserted, so a loop in Ergo cannot hold a worker past its quantum. No signal is used: `--lib` promises a library with no signal handlers, which rules out Go's signal-based asynchronous preemption.
- **A foreign call cannot be interrupted; its worker is handed off instead.** Every `extern c` call is bracketed by two stores that mark the worker "in foreign code since reduction tick T". A monitor thread that finds a worker past its quantum in foreign code moves that worker's run queue and reactor ring to a spare thread, which continues the queued fibers. When the C function returns, its thread finds it no longer owns a worker, queues its fiber as ready and joins the spare pool. This is Go's syscall and cgo hand-off (an M "can be blocked or in a syscall w/o an associated P", [runtime/proc.go](https://github.com/golang/go/blob/master/src/runtime/proc.go)), chosen over BEAM's dirty schedulers because BEAM's guarantee rests on the author's flag: an unflagged long NIF "degrades responsiveness of the VM" and a misclassified one lets "dirty I/O schedulers ... starve ordinary schedulers" ([erl_nif](https://www.erlang.org/doc/apps/erts/erl_nif.html)). Here the guarantee holds for every foreign call, declared or not.
- **A declaration only saves the wait.** A `CONTRACT` entry may mark a function `blocking`; such a call goes straight to the blocking pool (Tokio's `spawn_blocking`, BEAM's dirty I/O), so its peers do not wait one quantum for the monitor. A contract may also name an `interrupt` function (as `sqlite3_interrupt`) that the runtime calls when the fiber is stopped (I1).
- **Hand-off keeps non-atomic RC sound.** The fibers that move are all parked; their actors move whole, heap and all, with the queue (the agreed core-affine design: whole-actor migration when parked).
- **A foreign call that never returns** holds one thread and its one fiber, nothing else. Killing it is the job of `process.Child` (an OS process with `KillAlwaysLands`), not of the scheduler.

Law (`NB-5C`): with one worker configured, fiber A calls a C function that blocks reading a pipe; fibers B1..Bk are queued behind it and the last writes the pipe. The test passes when A returns, which can only happen if the queue ran while A was blocked; without hand-off it never finishes (a deadline names the hang, nothing is timed). Counted: hand-offs = 1, threads created <= 1; and over 1,000,000 short foreign calls hand-offs = 0 and the bracket costs <= 4 instructions a call. The hand-off's interleavings (the C function returns exactly as the monitor moves the queue) are an explored model, `models/handoff`.

### Perceus RC cost

Assumed until now, measured from `NB-4E` on. The workloads are named: **W1**, `compiler/syntax` and `compiler/format` run natively over every `.ergo` file under `compiler/` (available at the `NB-4` gate); **W2**, `ergo-stage2` building the subject (`NB-7`).

| Count | Acceptable | Why this number | Gate |
| --- | --- | --- | --- |
| Atomic RC operations executed on a single-actor workload | 0 | By construction (`NB-4C`). Koka measured 5% to 59% slowdown from atomic counts alone ([Perceus, PLDI 2021](https://www.microsoft.com/en-us/research/wp-content/uploads/2021/06/perceus-pldi21.pdf), section 4). | `NB-4E` |
| Instructions spent in `dup` and `drop` that write memory, as a share of the run's `instructions:u` | <= 2% | The matched control (C or Rust with ownership) spends 0; 2% is the declared loss ceiling and anything above it is a bug with a named `compiler/perceus` follow-up (the floor rule). | `NB-4E` |
| Reuse sites that take the in-place path at run time | >= 95% of the statically paired sites; 100% of `own` sites (no count test is emitted) | Koka without reuse analysis is more than 2 times slower on `rbtree` (same paper). | `NB-4E` |
| RC operations removed: Perceus `dup` + `drop` emitted against the scoped baseline (a `dup` at every copy, a `drop` at every scope exit), over the subject's IR | Reported and ratcheted from the first receipt | No prior number transfers to Ergo's DoD-heavy code; the first receipt sets it. | `NB-4E` |

The receipt is `docs/receipts/rc-cost.md` (written by `NB-4E`); the static counts come from `compiler/perceus`'s `TransformStats`, the dynamic ones from counters in a stats build of `runtime/native`.

### The Go route as the compliance oracle

Owner, 2026-11-27: "use the Go backend as a sort of compliance test so that we can write our own backend code against the Go compiler since we know the Go compiler works pretty much everywhere".

Decided: until `NB-7`, the Stage-0 Go route is the differential oracle of the native route. The same checked program is built both ways and run on the same cases, and the observable results must be equal.

- **Compared:** standard output and standard error bytes; the exit code; each typed failure (its variant and payload) and each defect (its kind and `.ergo` position); every law's verdict and counterexample; and, under `ergo test --explore --schedule TOKEN`, the sequence of observable events for the same token. The last one binds `NB-5`: choice points are numbered by [the runtime spec](../../spec/runtime.md), not by a runtime, so one token means one schedule on both.
- **Never compared:** memory, allocation counts, addresses, timing, reduction counts, the bytes of the two executables, or any order the spec leaves open.
- **Three oracles while Go lives.** Where `compiler/eval` can run the program (pure code, laws), eval, the Go route and the native route must all agree, and two against one names the faulty one. Where eval cannot (I/O, tasks, actors, `extern c`, the std suites, the examples), the Go route is the oracle alone.
- **A known Stage-0 fault** is a listed divergence with its reason, in a sites file written by the tool (as `check_agree.sites`); nothing is skipped silently and the list may only shrink.
- **What replaces it.** `NB-7` deletes Go only at a head where this differential is green over the whole corpus. From then the oracles are `compiler/eval` for pure code; the cross-tier agreement suites (eval, Dev tier, Release tier, `wasm32`: independent lowerings of one IR); agreement of the same suites across `linux/amd64`, `linux/arm64` and `darwin/arm64` (the portability the Go compiler gave); and, for effectful and concurrent behaviour, the std law suites, the explored models and schedule replay.

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
