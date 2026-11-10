ready

Protocol: orchestrate implementor worker protocol carried in the prompt. Spec: `/home/exedev/.cache/baton/wf10/s-sys/brief.md`, item 3; no separate spec.md.
Head: `fe43e710db8ebda2b3e8cc505c4e3616896df640`, branch `wf10/s-atomic`.
Base: `015c9e49480b9a222441d191132a1fe54946db14`, the corrected S-embed head merged forward as a605255e, without rebase. Original stack base 97707b44.
`git diff --stat 015c9e49..HEAD | tail -1`: `15 files changed, 434 insertions(+), 3 deletions(-)` (measured).

## Changes by file

- `internal/sketch/std/atomic/atomic.ergo`, `std.go`: sealed Int64/Bool handles and NewInt64/NewBool; Go-named scalar operations, four laws including increments in tasks.Set.
- `runtime/v1/atomic.go`, `runtime/embed.go`: pointer handles over Go typed atomics; no callbacks, waits or unsafe code in Ergo's bodies; ordinary operation-type task ABI.
- `internal/sketch/lower/blocking.go`, `internal/sketch/check/extern_test.go`: nonwaiting native bindings and existing exhaustive sealed-protocol fixtures.
- `runtime/v1/atomic_test.go`: seeded full-width integer/boolean reference histories; real task-set increments with unique tickets, final total and all nonoverlapping invocation/return pairs; concurrent flag claims have one winner. Failure prints source, expected/actual history and replay.
- `internal/sketch/lower/std_laws_test.go`: source-law probes cover bindings, aliases and captures. Shared success parser now accepts a proved finite domain; Bool's four cases exposed its old refusal. `runtime/v1/testdata/mutants.json` and `internal/sketch/lower/testdata/mutants.json`: seven guards added before tests/fix.
- `runtime/v1/atomic_bench_test.go`: matched Add and flag Swap versus direct sync/atomic. `docs/spec/stdlib.md`, `GLOSSARY.md`, `docs/previews/2026-11-10-atomic-handles.md`: API, law/strengths rows and worked source. Context-residue inventory under `internal/residue/testdata` updates the existing exact runtime source-list row.

## Run and receipts

- Gate log `/home/exedev/.cache/baton/wf10/s-sys/s-atomic/gate-1.log`: final `EXIT 0`. Reproduce with `bash /tmp/opencode/s-sys/atomic-gate.sh <log>`. Actual prose-fix continuation is `/tmp/opencode/s-sys/atomic-gate-finish.sh`, retaining passed checks. Changed-package short suites/vet, gofmt, residue, TestTestCommandsHaveCacheCoverage and native laws pass.
- Acceptance at head: `go test -p 2 -timeout 180s -count=1 -run '^TestAtomicLinearizableHistory$' ./runtime/v1`, `SECTION EXIT 0`; `go test -p 2 -timeout 180s -run '^TestStdLawsRun$' ./internal/sketch/lower`, `SECTION EXIT 0`. Build CLI with `go build -p 2 -o /tmp/opencode/s-sys/ergo ./cmd/ergo`; `/tmp/opencode/s-sys/ergo test /tmp/opencode/s-sys/atomic-accept` gives `PASS (1 tests)`, `SECTION EXIT 0`.
- Replay property: ERGO_ATOMIC_SEED=93 with the property command above. No wall-clock assertions. Source-law probes cover compiler/ABI/alias/capture risks; native reference histories cover full-width values, wraparound and real concurrency. No production test hook, no new file-reading Go test, no test deletion.
- Red base: `/home/exedev/.cache/baton/wf10/s-sys/s-atomic/red-base.log`, `BASE CHECK EXIT 1 (expected 1: atomic absent at source base bb0f4cf8 / request head 97707b44)`. Same final acceptance source, unknown atomic diagnostic, no Go compile failure.
- Seven mutants killed: atomic-constructor, atomic-add-old-value, atomic-swap-old, atomic-cas-always, atomic-bool-cas-always, atomic-add-split and atomic-finite-law-proof. `/home/exedev/.cache/baton/wf10/s-sys/s-atomic/mutants.log`, `EXIT 0`; runner `bun /tmp/opencode/s-sys/atomic-mutants.ts`. Split Add gives lost tickets at seed 93 draw 0. Its broad edit only adds runtime.Gosched to a split read/store, without a production hook.
- First gate found only prose inconsistencies: two law statements differed from formatted source, and the inherited lower request had a residue-sensitive path spelling. Lower owner fixed its request at 015c9e49, passed residue and merged forward. Canonical ledger/map spellings fixed at fe43e710. Assertions unchanged; no semantic flake.

## Intent, prior art, speed and gaps

Effect Ref.ts solves shared mutable state. Go typed atomics supply scalar counters/flags. Ergo keeps that intent, not Ref.modify tuples, dual-call functions, pipe or service registries (W3/W4/W5/W14): ordinary statements, operation types and laws state the contract. sync.Ref remains the compound arbitrary-value operation. Pure explorer steps cannot enumerate native Go atomic instruction interleavings; the spec's real seeded-history option is used.

Inferred from standing sources: Gleam/Elixir normally share state through BEAM processes; BEAM atomics are a host facility. ReScript targets JavaScript; worker sharing uses host Atomics/SharedArrayBuffer. Ergo targets Go and directly uses sequentially consistent sync/atomic.Int64 and Bool, with no alternate fast path or unsafe lowering.

No speed claim on a loaded host. Quiet-window command and matched BenchmarkAtomic controls: `/home/exedev/.cache/baton/wf10/s-sys/bench-pending.md`. umask 022, nice 19, GOMAXPROCS=1, go -p 2, GOMEMLIMIT=6GiB and kernel RLIMIT_DATA 8GiB. common.md lines 42–44 request a worker-created scope contrary to the carried protocol; inherited kernel limits were used instead. No paid calls, pushes or rebases. Full/merge/conformance and Darwin belong to the batch. Final process and private-cache cleanup are recorded in state.md.

Extended request/state: `/home/exedev/.cache/baton/wf10/s-sys/s-atomic/round-1-request.md`, `/home/exedev/.cache/baton/wf10/s-sys/s-atomic/state.md`.
