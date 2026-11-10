ready

Protocol: implementor worker protocol supplied in the worker prompt, Implementor and Rounds; protocol.md and safety.md were not read.
Spec: /home/exedev/.cache/baton/wf10/g1-http/brief.md, G1, with decisions.md's required body/header budgets. There is no separate spec.md.
Head (checked source): 5124191c5ebe07d8549a7d51fe920c7837c9dc7e
Base: 31b9dbebb59c969e9692009a553d58715ae34e52
Source diff: 19 files changed, 1094 insertions(+), 7 deletions(-).

The branch also receives a request-only commit after this source head.

## Changes by file

- `internal/sketch/std/http/http.ergo`: sealed transport Listener, Ambient Listen, required positive Limits, typed InvalidLimits, buffered Request/Response, closed body results, case-insensitive header lookup, and the ordinary row-preserving Serve wrapper.
- `internal/sketch/std/http/router.ergo`: pure Match, path parameters, distinct refusals, explicit HEAD precedence and GET fallback, and the construction law.
- `runtime/v1/http.go`: net/http transport; request child tasks on the existing runtime; disconnect stops; serving shutdown closes the listener and joins handlers and their releases; defects stay defects. Half-closed failed reads still reach the handler as typed body values.
- `runtime/v1/http_test.go`: seeded loopback map model, body/header budgets, repeated query/header values, HEAD and empty bodies, counted concurrent requests, event-driven stop/drain, client abort and composite defects.
- `runtime/v1/http_bench_test.go`: alternating adapter/net/http benchmark. The Go control uses request contexts, shutdown cancellation and joined workers; tracing is off.
- `runtime/v1/testdata/mutants.json`: seven runtime mutants.
- `runtime/embed.go`, `runtime/v1/files.go`: runtime embedding and native record bindings.
- `internal/sketch/std/std.go`: embed the new package.
- `internal/sketch/std/extern_callers_test.go`, `std_test.go`: reviewed host inventories, without exemptions from authority checking.
- `internal/sketch/lower/blocking.go`: native binding policies.
- `internal/sketch/lower/runtime199_blocking_native_test.go`, `runtime199_recovery_test.go`: identify the HTTP lifetime laws and guarded child boundary.
- `internal/sketch/lower/std_http_test.go`, `table_programs_test.go`: in-process handler adapter typecheck and one native property program for the router and a handler under time.Manual.
- `internal/sketch/lower/testdata/mutants.json`: four library/adapter mutants.
- The existing residue fixture under `internal/residue/testdata`: update the runtime embed inventory; no new residue exemption.
- `docs/spec/stdlib.md`: public contract, law ledger, prior art and Effect-strengths row.

## Decisions

Limits stays the required record of two int fields. L6 refines scalar defined types, not record-field predicates; the native record binding supplies int fields, not per-field refined conversions. The permitted typed Serve refusal preserves this shape without an HTTP-specific checker or lowering rule. A non-positive budget is never an unbounded default. InvalidLimits leaves the transport open for retry or Close.

Effect HttpServer's intent is a scoped server with a handler's dependencies intact. Ergo uses ordinary function handlers and needs R instead of Effect instructions, gen and service tags; typed records/unions and exhaustive match instead of TaggedError and tag dispatch; existing child tasks instead of passed Scope builders. Match is independent of host I/O.

## Checks and reproduction

Run in `/home/exedev/Developer/personal/.worktrees/ergo-g1` on wf10/g1. Checks used umask 022, GOMAXPROCS=1, go -p 2, GOMEMLIMIT=6GiB and one private GOCACHE. Later checks used nice -n 19. The cache and the isolated base checkout were removed after the final gate.

Gate log: `/home/exedev/.cache/baton/wf10/g1-http/gate-1.log`.
Exit line: `EXIT 0`.

The focused gate ran:

- gofmt of changed Go files and `ergo fmt --check` of both new Ergo files: EXIT 0.
- `go test -p 2 -short -timeout 20m ./runtime ./runtime/v1 ./internal/sketch/std ./internal/sketch/lower ./internal/residue`: `PACKAGE EXIT 0`.
- `go test -p 2 -timeout 10m ./internal/sketch/lower -run '^(TestHTTPHandlerRows|TestStdHTTPLawsRun)$'`: `ACCEPTANCE EXIT 0`, at the source head above.
- `go test -p 2 -race -timeout 5m ./runtime/v1 -run '^TestHTTP'`: `RACE EXIT 0`, at that same head. This includes the generated server/model sequences and concurrent stop checks.
- `go test -p 2 -timeout 10m ./scripts/gate -run '^TestTestCommandsHaveCacheCoverage$'`: `CACHE EXIT 0`.
- `go vet -p 2 ./runtime ./runtime/v1 ./internal/sketch/std ./internal/sketch/lower`: `VET EXIT 0`.

The corrected base acceptance run is `red-base.log`: TestHTTPHandlerRows at 31b9dbeb fails with `no package "http"`, `EXIT 1`. Earlier exploratory diagnostics are not the base receipt. The first focused gate found only residue inventory/comment issues; those were fixed. No assertion was removed and no failure was labelled a load flake.

All eleven ledger mutants were demonstrated individually, then restored. `../mutants.md` names their guards, logs and observed failures: header/body budgets, empty content type, swapped fields, truncated bodies, serialized handlers, ignored disconnects, missing R, wrong route parameters, missing HEAD fallback, and case-sensitive header lookup. Runtime checks run in process; the existing global mutant runner does not register runtime/v1, so its full coverage sweep was not run.

After every process-starting check, ps was inspected. A final cwd-filtered process check found no G1 check process left in either owned workspace. No other worker's process was signalled.

## Prior art and limits of the evidence

Documentation inspected: Mist 6.0.3 has a function handler, a required size at read_body, and an eager read_request_body adapter with a supplied refusal response. Wisp 2.2.2 has body helpers and handle_head. Plug 1.20.3 passes a connection through functions, normalizes headers and reads bodies lazily. These execute on BEAM; this core uses net/http and owned Go tasks. The brief's buffered-by-default description is not treated as a claim about current Mist/Wisp defaults.

ReScript's direct uncurried host callbacks and Elixir/Gleam's BEAM lowering are comparisons from PRIOR_ART.md's canonical source index, not new performance measurements. MoonBit task groups are the ownership comparison, not an HTTP support claim. No new language lowering was introduced. Finite laws and models are tested, not proved.

## Known gaps and map update

- G1's core acceptance checks are green. N1 is a separate stacked example unit and was not started; its example/laws and macOS run are not claimed here.
- The RPC owner must migrate its duplicate Listener onto this transport. No RPC file or other branch was edited.
- Quiet-host performance is pending in `../bench-pending.md`; no parity or speedup is claimed. Adding net/http to the shared runtime/v1 package also has an unmeasured initialization/retention cost; runtime package splitting remains a follow-up, not a zero-cost claim.
- The batch owner runs the full/merge gate and macOS check. Main is still at batch-14 fix f6802de7, before the batch-15 landing named by the brief; no rebase or main edit was made.
- common.md lines 42–44 request a worker-owned systemd scope, contrary to the supplied worker protocol and without a Departs-from line. The higher-priority protocol was followed: no custom launcher or scope was created.

No paid API/model calls, publishing, owner-data access or GitHub map edits were made. Suggested map update: record the HTTP core evidence, keep N1 and the RPC migration separate, and retain the benchmark/retention follow-ups.
