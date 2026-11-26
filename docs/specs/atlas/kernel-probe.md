# Unit 0: `KERNEL-ENGINE-PROBE` Report

Benchmark and architecture probe comparing **`psychoscript`**, **`Luau`**, and **`ergoscript`** (design note + spike) behind one uniform `KernelEngine` interface (`internal/experimental/kernelprobe`).

## 1. Contract & Prototype Layout

`internal/experimental/kernelprobe` defines the embedder contract matching `docs/specs/atlas/kernel/spec.md` and `docs/specs/atlas/models/kernel/kernel.ergo`:

- `KernelEngine` (`engine.go`): `Name`, `Eval`, `Restore`, `Snapshot`, `Cut`, `Kill`, `BindTool`, `OnAsyncSettle`, `Close`.
- `HandleRegistry` & `SlotPool` (`registry.go`): enforces exclusive `DELIVERS` ownership per handle kind (`ErrDelivererCollision`), single wake delivery (`HandedNeverDoubleWakes`), owner-scoped retire (`OtherOwnerReloadKeepsLiveCalls`), <=160-char bounded lost-binding notices (`NoticeOnlyWhenBindingsLost`), and zero queueing for `RoleRoot` (`SlotPool.Acquire`).
- Uniform C ABI (`native/include/kp_abi.h`) + `dlopen` cgo bridge (`native_cgo.go`, zero `import "unsafe"` in Go, pure-Go `!cgo` stub in `native_stub.go` returning `ErrNativeUnavailable`):
  - **Candidate (a) `psychoscript`**: cloned `https://github.com/anomalyco/psychoscript` (`68598e5`, MIT License) -> C ABI crate `native/psychoscript_c`.
  - **Candidate (b) `Luau`**: cloned `https://github.com/luau-lang/luau` (`97b2f4b`, MIT License) -> C++17 static/shared wrapper `native/luau_c`.
  - **Candidate (c) `ergoscript`**: minimal AST spike (`ergoscript.go`, `ErgoScriptDesignNote`) over `internal/sketch/syntax.Parse` + static role/closure/import checks.
- Benchmark & automated verifier CLI: `internal/experimental/kernelprobe/cmd/kernelprobe` (`bench`, `eval`, `prompts`).

## 2. Candidate (a) `psychoscript` Inspection

- **What it is**: A pure-Rust confined JavaScript-like bytecode VM (`rust/crates/psychoscript`, mirrored against the TypeScript reference in `src/`) built specifically for LLM tool harnesses, with no Node/Bun dependency.
- **License & upstream**: MIT License (`68598e5`), 137 packages in `rust/Cargo.lock` (`oxc_parser` 0.159, `icu_*` 2.4 `compiled_data`, `serde`).
- **Embedding API**: `Runtime::make().globals(...).limits(...)`, `Limits::{timeout_ms, max_output_bytes, max_heap_bytes, clock: Clock::ThreadCpu, slice_ms, max_value_depth}`, `ProgramOptions::bindings`, and `Program::suspender().suspend(...)` -> `RunOutcome::Suspended` -> `to_bytes` / `from_bytes` / `resume_bytes`.
- **Probe integration findings**:
  1. `HostFunction::new_async` futures poll on the VM thread inside `Tasks::spawn`, so concurrent Go tool calls require offloading to a worker pool (`PoolFuture` in `psychoscript_c`).
  2. Top-level `const`/`let`/`var` bindings inside finished cells are not automatically exported by `psychoscript` alone; `psychoscript_c` instruments top-level declarations via `oxc_parser` (`__kp_bind`) and rejects closures/functions (`FunctionMarker`) into `lost_bindings`.
  3. Mid-execution bytecode suspend/resume works via `Suspender::suspend` + `to_bytes` (`TestPsychoscriptBytecodeSuspendAndSnapshot`).

## 3. Reproducible Benchmark Measurements (Metrics 1–6)

Reproduced with:

```sh
KERNELPROBE_LIB_DIR=/tmp/kernelprobe/lib GOMAXPROCS=4 GOMEMLIMIT=6GiB \
  go run ./internal/experimental/kernelprobe/cmd/kernelprobe bench --iters 50 --sizes --out /tmp/kernelprobe/bench_raw.json
```

Host: Linux x86_64 (`16` vCPU, `64 GiB` RAM), Go `go1.27.4` (`-p 4`), Rust `rustc 1.99.1`, `g++ -O2 -std=c++17`.

| Metric | `psychoscript` (Rust VM + `dlopen`) | `Luau` (C++17 VM + `dlopen`) | `ergoscript` (Pure Go AST spike) |
|---|---:|---:|---:|
| **1. Cold start** (`first` / `med` / `p95`, n=10) | `2.264` / `0.792` / `2.264 ms` | `3.086` / `0.356` / `0.698 ms` | `0.311` / `0.020` / `0.311 ms` |
| **2a. Trivial cell** (`med` / `p95`, n=50) | `0.440` / `1.006 ms` | `0.218` / `0.432 ms` | `0.027` / `0.045 ms` |
| **2b. 10-tool loop cell** (`med` / `p95`, n=50) | `1.463` / `9.151 ms` | `0.312` / `1.591 ms` | `0.078` / `0.208 ms` |
| **3a. Snapshot** (`med` / `p95`, n=20, 20-key state) | `0.034` / `0.084 ms` | `0.016` / `0.024 ms` | `0.015` / `0.018 ms` |
| **3b. Restore** (`med` / `p95`, n=20) | `0.031` / `0.139 ms` | `0.012` / `0.030 ms` | `0.015` / `0.018 ms` |
| **3c. Snapshot payload size** | `343 B` | `302 B` | `344 B` |
| **4a. Unfinished-tool cut latency** | `4.547 ms` | `0.308 ms` | `0.077 ms` |
| **4b. Async settle -> `OnAsyncSettle` signal** | `14.13 us` | `6.72 us` | `2.84 us` |
| **4c. Restore + wake eval latency** | `5.225 ms` | `0.354 ms` | `0.063 ms` |
| **5a. Static cgo delta** (stripped vs `baseline_cgo`) | `+19.32 MiB` (`21.81 MiB` total) | `+1.00 MiB` (`3.48 MiB` total) | `N/A` (pure Go) |
| **5b. Shared `.so` / Static `.a` size** | `14.90 MiB` `.so` / `60.29 MiB` `.a` | `1.81 MiB` `.so` / `3.63 MiB` `.a` | `0 B` native (`+0` inside `./cmd/ergo` `77.71 MiB`; `8.19 MiB` standalone CLI) |
| **6. Build complexity** | `cgo` + `rustc` 1.99 + 137 Cargo pkgs; `.llvmbc` must be stripped (`-C embed-bitcode=no` / `objcopy`) for Go cgo static link | `cgo` + C++17 (~40 `.cpp` files, zero crates); trivial cross-compile (`zig c++`/`clang++`) | Pure Go (`CGO_ENABLED=0` + `vetgoos` clean; zero FFI or cross-linkers) |
| **7. Reference tasks passing** (`eval --all`) | `10 / 10` | `10 / 10` | `10 / 10` |

Baseline sizes for Metric 5: `baseline_purego` (`CGO_ENABLED=0`, `encoding/json` + `sync` + `fmt`) = `2,580,640 B` (`2.46 MiB` stripped, `3.74 MiB` unstripped); `baseline_cgo` = `2,602,280 B` (`2.48 MiB` stripped, `3.65 MiB` unstripped).

## 4. Candidate (c) `ergoscript` Design Note & Spike Summary

Captured verbatim in `ergoscript.go` (`ErgoScriptDesignNote`):

1. **Front-end reuse**: Parse cells via `internal/sketch/syntax.Parse` (`compiler/syntax`) and type/effect-check cells against a `CellTools` capability row once connected to `compiler/check`.
2. **Static guarantees**: Refuses ambient imports (`StaticCapabilityError`), worker calls to root-only tools (`StaticRoleDenied`), and closures assigned into persistent session bindings (`NonSerializableBinding`) *before* execution starts.
3. **Observed ergonomics gaps in spike**: Ergo currently forbids untyped object literals (`{k: v}` -> requires `Args{k: v}`), treats `map` and `default` as global reserved keywords (`tools.Map` and `"default": "yes"` needed in Tasks 8–9), and requires `[]any{...}` slice syntax. Any production `ergoscript` kernel would first add lightweight cell record/array sugar to keep LLM retry rates competitive.

## 5. Metric 7: Automated Verifier, Prompts, and Blind Authoring Trial

### 5a. Automated Verifier & 10-Task Hydrogen Prompts

All 10 tasks derived from `docs/specs/atlas/eval-from-sessions.md` §4 can be verified non-interactively via `eval`:

```sh
KERNELPROBE_LIB_DIR=/tmp/kernelprobe/lib go run ./internal/experimental/kernelprobe/cmd/kernelprobe eval --all --lang psychoscript
KERNELPROBE_LIB_DIR=/tmp/kernelprobe/lib go run ./internal/experimental/kernelprobe/cmd/kernelprobe eval --all --lang luau
go run ./internal/experimental/kernelprobe/cmd/kernelprobe eval --all --lang ergoscript
```

Dump the 10 Hydrogen task prompts and per-task verification commands directly with:

```sh
go run ./internal/experimental/kernelprobe/cmd/kernelprobe prompts --json
```

Task summary for Hydrogen trials (replace `<cell.src>` or `<cell1.src>` + `<cell2.src>` for multi-cell tasks 3, 7, 8):

1. `parallel-status-sweep`: Fetch 12 units via `tools.subagents`/`tools.read`, partition `ready` vs `blocked`, return `{ready, blocked, table}`.
2. `fanout-start-handles`: Launch 6 `tools.start.shell` jobs (`lane-1..lane-6`) immediately without waiting, return `{handles}`.
3. `cut-and-wake-resumption`: Multi-cell (`cell1`, `cell2`) — bind `T0`, call `tools.shell({command:"slow-gate"})` (`Unfinished` -> cut before `tools.write`), restore + inspect `tools.handles.get`, return `{workspace, status, passed}`.
4. `role-subagent-routing`: Dispatch 3 workers via `tools.start.subagent`, send `tools.message` + `tools.answer_subagent`; verifier also enforces `RoleWorker` denial on `tools.subagent` (W8).
5. `persistent-queue-drain`: Read `tools.queue`, deduplicate by `unit` preserving order, start top 2, replace remainder.
6. `code-only-read-edit`: `tools.read` + `tools.edit` (`oldMode` -> `newMode`) + re-read confirm, zero OS spawns.
7. `session-state-and-closure-reject`: Multi-cell (`cell1`, `cell2`) — carry `T0` + `results` across `Snapshot`/`Restore` while dropping/refusing closure `helper` cleanly (`<=160` char notice or static rejection).
8. `map-partial-cut-resume`: Multi-cell (`cell1`, `cell2`) — `tools.map` over `L1..L8` cuts on `L4`, resumes `kernel.cut.maps[0].remaining` (`L4..L8`) cleanly with 9 total calls (zero recompute of `L1..L3`).
9. `nonblocking-ask-user-async`: `tools.ask_user_async` (<=30-char header + `default`) followed immediately by `tools.start.job`.
10. `declarative-job-monitor`: `tools.start.job` watch without `tools.sleep`, returning `{monitor_id, kind:"job", sleeps:0}`.

### 5b. Metric 7b (Blind Hydrogen Authoring Reliability Trial)

Evaluated in a single blind session (`30` task-language pairs, Tasks `1..10` across all 3 engines, strictly from `kernelprobe prompts --json` with up to 3 attempts per `(lang, task)`):

| Engine | `pass@1` | `pass@3` | Attempts | Retries | Error Breakdown by Class |
|---|---:|---:|---:|---:|---|
| **`Luau`** | `10 / 10` | `10 / 10` | `10` | `0` | `none` |
| **`psychoscript`** | `7 / 10` | `8 / 10` | `15` | `5` | `assertion_mismatch: 6`, `runtime_error: 1` |
| **`ergoscript`** | `4 / 10` | `6 / 10` | `20` | `10` | `type_or_literal_syntax: 5`, `syntax: 3`, `assertion_mismatch: 3`, `reserved_keyword: 2`, `runtime_error: 1` |

Per-language findings observed during the blind authoring pass:

- **`Luau` (`10/10` pass@1, `0` retries):** Single `{...}` table constructor handled list and map shapes cleanly, no `async`/`await` boilerplate was needed, and `tools.map` cut deterministically after `L3` leaving `L4..L8` in `kernel.cut.maps[1].remaining` (Task 8 passed attempt 1). Key authoring habits to document in the system prompt: 1-based indexing (`[1]`, `i <= 2`), omitting `local` for persistent cell globals (`T0`, `results`), quoting reserved keys (`["until"]`), and using `~= nil` rather than falsy checks on booleans.
- **`psychoscript` (`7/10` pass@1, `8/10` pass@3, `5` retries):** Passed straight-line JS tasks immediately (`1, 2, 4, 5, 6, 9, 10`) and Task 7 on attempt 2, but failed Tasks 3 and 8 within 3 attempts:
  - *Task 3*: bare `T0 = "/workspaces/ergo"` raised `ReferenceError: Unknown identifier 'T0'` (attempt 1); attempts 2–3 persisted `const T0` cleanly, but missed the nested Cell 2 `passed` field shape (`map[passed:false status:settled workspace:/workspaces/ergo]`).
  - *Task 8*: `tools.map` ran items concurrently with a **non-deterministic timing race** across identical `cell1.src` runs (`finished=3/remaining=5`, then `finished=7/remaining=1`, then `finished=2/remaining=6`), causing flaky partial-cut counts.
- **`ergoscript` (`4/10` pass@1, `6/10` pass@3, `10` retries):** Tasks `4, 5, 6, 10` passed attempt 1 and Tasks `7, 9` on attempt 2, while Tasks `1, 2, 3, 8` surfaced seven concrete language UX gaps blocking blind model authoring:
  1. Untyped `{}` object literals rejected (`Map {}` or typed `Args{}` required).
  2. Untyped `[a, b]` list literals rejected (`[]Any{}` required).
  3. `tools.map` rejected by keyword guard (`Ergo has no map keyword`).
  4. `default:` key inside map literals rejected by switch keyword guard (`write case _:`).
  5. Multiline `Map { ... }` without trailing commas failed ASI (`expected "," or "}" in a composite literal, found newline`).
  6. `if ... { ... } else { ... }` does not evaluate as a value expression (returns `<nil>`).
  7. Nested `fn helper()` declarations rejected inside cells, and `tools.Map` rejected `fn(lane) { ... }` (`TypeError: tools.map requires func literal`).

*Methodology note:* The single blind session authored in fixed order `psychoscript -> luau -> ergoscript`, so `Luau` Tasks 3 and 7 benefited from multi-field return/status handling learned during the `psychoscript` pass; `Luau` Task 8's deterministic `tools.map` cut and Metrics 1–6 are order-independent.

## 6. Phase 1 Decision & Architecture Seam

**Phase 1 Decision: `Luau`** behind the swappable `KernelEngine` seam (`internal/experimental/kernelprobe` -> production `modules/atlas/kernel`).

- **Why `Luau` wins Phase 1 across Metrics 1–7b:**
  - `+1.00 MiB` static cgo delta (`3.48 MiB` stripped vs `2.48 MiB` `baseline_cgo`, or `1.81 MiB` `.so`) vs `+19.32 MiB` for `psychoscript`.
  - Fastest native VM across cold start (`0.356 ms` median), tool loops (`0.312 ms` for 10 calls), `Snapshot`/`Restore` (`0.016`/`0.012 ms`), and `Unfinished` cuts (`0.308 ms`) via `lua_yield(L, 0)` + `pcall`-proof `lua_callbacks->interrupt`.
  - Pure C++17 upstream (~40 `.cpp` files, zero Cargo crates, no `.llvmbc` static linking restriction).
  - `10/10 pass@1` authoring reliability in Metric 7b with deterministic sequential `tools.map` partial-cut behavior (paired with brief prompt guidance on 1-based indexing, `["until"]` quoting, global vs `local` persistence, and `~= nil` boolean checks).
- **Long-term swap-in (`ergoscript`):** `KernelEngine` keeps the interpreter pluggable so `ergoscript` can swap in cleanly in pure Go once cell literal/keyword/expression ergonomics (`{}`, `[]`, `.map`, `default:`, multiline ASI, value `if-else`) are resolved.
