ready

Protocol: supplied orchestrate worker/implementor protocol (implementor prompt; no protocol.md or safety.md read).
Spec: `/home/exedev/.cache/baton/wf10/v1-L8/plan.md`, P-L8, line 290; owner decisions in `v1-L8/decisions.md`.
Head: `8bf1286185d92803743593997c06c1b67c2ac635`, branch `wf10/p-l8`, isolated worktree `ergo-l8b`.
Base: `a35c4d81` (IG9 handoff). `git diff --stat a35c4d81..HEAD | tail -1`: `13 files changed, 787 insertions(+), 2 deletions(-)`.

## Change, by file (measured)

- `internal/sketch/lower/static.go`: select eligible declared plain frames, keep owned values in stack slots and bits, write direct nested-scope releases and one Go cleanup defer for frame exits. Keep B1 selectable through `releaseLowering=scope` and `SetReleaseLoweringForTest`. No new checker rule. Reassignment, flags, field masks, generated drop glue, held-taking calls, function literals and plans over 64 keys retain B1.
- `lower/{body,decls,std,release,blocking}.go`: frame selection, registration, hand-off, scope-end cleanup and runtime catalog integration.
- `runtime/v1/static.go`: static registration and hand-off, kept returns, cleanup defect accumulation and exit settlement. Reuses checked Close release text and the existing cause protocol.
- `runtime/embed.go`: embed the helper source in emitted modules.
- `lower/static_release_test.go`: seeded equality property, static vs B1 vs independent ownership traces; B1 generator, IG9 host-handle generator and generated kept-return sibling stacks. One dispatcher build; full-tier `needsBuild`. Failures print source, both outputs and replay seed. It builds Go because emitted panic, stop and cleanup edges must execute.
- `lower/testdata/mutants.json`: ten static helper mutants, each explicitly anchored to the property (runtime source is embedded in generated programs, not executed in the test binary).
- `runtime/v1/static_bench_test.go`: matched static/B1/hand-Go local and loop shapes with allocation reporting.
- `docs/spec/resources.md`, `CHANGELOG.md`: emitted form, supported subset, reference selector and honest pending measurements.

## How to run

```
umask 022
GOMAXPROCS=2 GOMEMLIMIT=6GiB GOCACHE=<private> go test -p 2 -timeout 20m -count=1 -run '^TestStaticReleaseAgrees$' ./internal/sketch/lower
```

Bound ad hoc checks with common.md's 8G zero-swap scope. Build a reference compiler with `go build -ldflags '-X ergo.local/prototype/internal/sketch/lower.releaseLowering=scope' -o <reference-ergo> ./cmd/ergo`.

## Evidence (measured)

- Round gate: `/home/exedev/.cache/baton/wf10/v1-L8/P-L8/gate-1.log`, **GATE_EXIT=0**, at 8bf12861: gofmt of changed Go files; vet and short tests of lower/runtime/runtime-v1; uncached acceptance; residue; TestTestCommandsHaveCacheCoverage.
- Acceptance command above: **ACCEPT_EXIT=0** in `accept-1.log`, and **STEP_EXIT=0** in gate-1.log, run at head.
- Red at base: `red-1.log`, **PROBE_EXIT=1**. Copied the acceptance onto an archive of a35c4d81 with a base-only no-op mode-selector shim. It compiles, runs, and fails because B1 emits none of the promised static branches. No production code was copied to the base.
- Mutants: `mutants-1.log`, **MUTANTS_EXIT=0**, `go run ./scripts/mutants -run '^P-L8' -workers 1 -procs 2 -work /tmp/opencode/ig9/pmut -coverage /tmp/opencode/ig9/p-coverage.json.gz lower`: ten judged, ten killed by TestStaticReleaseAgrees. The coverage argument is a focused known-killer scheduling manifest with its unmutated run duration, not a line-coverage claim. The earlier run (`mutants-before-kept.log`) exposed two survivors; generated kept-return cleanup-defect cases now kill both.
- Unique mutants: OwnStatic omits its bit; HandStatic keeps it; ReturnStatic omits kept; StaticBegin drops panic; Part stops after a release defect; Failed never fails; End drops release defects; nested release keeps its bit; frame release order reversed; nested-scope release omitted. No other test exercises these static helpers.
- No tests deleted. New test reads no files, so no closure sweep. One heavy check at a time, share C (GOMAXPROCS=2, -p 2), umask 022, one private cache. Process listing after checks: no own test or mutant descendants remain. Followed common.md's explicit memory-scope departure.

## Timeout attribution (measured; not P-L8 or IG9)

Adjacent-commit bisection: `TestTimeoutDeadlineIsTheProgramScheduler` passes at `71dd4282^` (`9190c614`, timeout-parent.log, PROBE_EXIT=0) and fails at **71dd4282** (timeout-change.log, PROBE_EXIT=1). Owner: **L8 B4 stop-wins**, commit that adds the hidden result releaser. Its emitted call is `After[int](..., env.Scheduler, nil, time.Duration(...), ...)`; the old test at line 103 expects `.Scheduler, time.Duration(...)` with no releaser. Thus the failure already at 235bc8a8 is a stale lowering-shape assertion, not evidence that the program Scheduler is lost. Fix belongs to B4/integration; P-L8 does not change that test.

## Known gaps / batch work

- Quiet benchmark pending: `bench-pending.md` names exact interleaved commands and controls. Host load was 18.43/18.59/21.23 on 16 cores; no timing or hand-Go parity claim. Default static lowering removes per-value list entries, but a speed claim remains unmeasured.
- The frame shares one deferred cleanup body for all exit edges; Go may open-code eligible defers. Nested lexical ends are emitted directly. Aggregate static release is not implemented; B2 remains its reference/control.
- Full example and conformance runs under both compiler selectors are the merged batch's checks, not repeated in this focused gate. X-L8 will run its new example under both.
- No push or main merge. Map update: P-L8's equality and mutant clauses are satisfied; performance parity remains pending the benchmark window.

## Prior art (inferred from pinned project sources)

Go open-coded defers, Rust MIR drop elaboration and Swift SIL destruction motivate stack-local cleanup state. Gleam and ReScript lower explicit cleanup calls to the host and do not add affine drops. Elixir uses Erlang try/after and VM resource ownership, not this static ownership plan. MoonBit has LIFO defer and error-only errdefer; its shared-IR optimization passes are relevant structure, not performance evidence for Ergo. No guarantee was removed for speed.
