ready

Protocol: supplied implementor worker protocol (not protocol.md or safety.md).
Spec: `/home/exedev/.cache/baton/wf10/cli-units/brief-3.md`, C6, round 1 (no separate spec.md); orchestrator decision 2026-11-10 (decisions.md, last entry); common.md.
Head: `10dbf47af45c8eaf89b6d3aa47e3680587c20e4a` (the implementation; the commit after it only records this request).
Base: `ec6f1be2` (CU5 ready head). Branch: `wf10/cu6`. Workspace: `/home/exedev/Developer/personal/.worktrees/ergo-cu4`. No rebase, no push.
Measured diff: `20 files changed, 816 insertions(+), 46 deletions(-)` (`git diff --stat ec6f1be2..10dbf47a | tail -1`).

## The question first: no #197 needed (inferred from the source, then measured by the tests)

The sink is borrowed and the body is consumed inside the call, as the orchestrator asked. `put` (a function) and `root` (an `fs.HostRoot` the caller opened) stay the caller's. Every exit of the call (end, failure, sink failure, stop) cancels the exchange and joins its reader before it returns, so nothing of the exchange outlives the call. The new file of FetchFile is held by Go `defer` inside one native call, so it needs no frame registration and no resource return. A returned `Seq` of parts was not built: it would hold the connection after the call, which is #197's owned resource return. When #197 lands, a scoped Seq form can be added; nothing here changes.

## Changes by file

- `internal/sketch/std/http/network.ergo`: `SinkFailed{Reason}`; `Network` gains `SendURLInto(r, put func(part string) fails SinkFailed) Response fails RequestFailed | OriginRefused | RedirectLimit | SinkFailed`; `Remote` provides it; `FetchInto(r, put)`; `FetchFile(root fs.HostRoot, path string, r URLRequest)` with the `fs` failures; `BodyFile { Put, partOnly() }`, sealed, made only by the `intoFile` extern.
- `runtime/v1/http_client.go`: `httpStreamURL`: a reader goroutine hands the head and parts (at most 32 KiB) over an unbuffered channel; `put` runs on the caller's task, in order; a deferred cancel-and-drain joins the reader on every exit, a panic included. A declared Content-Length over the budget fails TooLarge before any part (checked after the redirect decision, so a followed redirect's own body never counts); a growing body fails before the part that crosses the budget. `httpDoErr` and `httpBodyTooLarge` are shared with the buffered exchange (no behaviour change).
- `runtime/v1/http_network.go`: the policy loop is `httpFetch(..., exchange)`, shared by `HTTPFetch` and the new `HTTPFetchInto`; `httpLocation`. `HTTPIntoFile` makes `.<name>.part-<random>` beside the path with O_EXCL under the `os.Root`, runs the fetch, then syncs, closes and renames; any other exit closes and removes it. `BodyFile.Put`, `HTTPSinkFailed`.
- `runtime/v1/files.go`: binds `http.SinkFailed`. `internal/sketch/lower/blocking.go`: `HTTPFetchInto` callerWait, `HTTPIntoFile` and `BodyFile.Put` task-only. `runtime199_blocking_native_test.go`: `HTTPFetchInto` joins `hostWaitLaws`, naming its runtime law test.
- `internal/sketch/check/extern_test.go`: sealed inventory rows for `http.Network` (open) and `http.BodyFile` (sealed by `partOnly`). Note: `TestStdProtocolSealed` was already red at the base ec6f1be2 (`enumerate/http.Network has no row`, measured in a detached base worktree); CU5 added Network without a row. This row fixes it.
- `internal/sketch/std/extern_callers_test.go`: the two new host externs in the pinned list; `heldTypes` also holds a type that no other package builds and that an extern whose written row lists Ambient returns (`fs.OpenRoot`'s `HostRoot`), so `FetchFile`, which takes a root, has the authority without listing Ambient. This is one rule, not a name list.
- `runtime/v1/http_sink_test.go` (new): `TestHTTPFetchIntoMatchesModel` (acceptance property) and `TestHTTPFetchIntoStops`.
- `internal/sketch/lower/std_http_network_test.go`: the existing source property now draws a sink per seed (`Fetch`, `FetchInto`, `FetchFile` into a temp root, read back); same model outputs.
- `examples/download`: `Downloader.Save(url, path)`, `domain.Store`, provider `Save` through `http.FetchFile`; main opens the working directory as a root it owns and passes it to the provider; `--out`; case `denied-save`; README.
- `docs/spec/stdlib.md`: http item 12 (API, guarantees, tests, prior art, the #197 note).
- Ledgers: 7 runtime, 2 lower, 1 example mutant; `cu5-no-redirects-ignored` and `cu5-domain-origin-lost` re-anchored (their text moved; same edits).

Decided by simpler/reversible (also in state.md): `FetchFile`'s root comes first, because a std function whose first parameter is `URLRequest` and that reaches a host extern makes `URLRequest` a host provider (measured: `TestStdHostProvidersHaveAuthority`). `BodyFile` is an exported sealed type because the lowering resolves an operation type's runtime body by the same Go type name (measured ICE with a lower-case name). FetchFile hands its callback a `BodyFile`, not a `put` function: the lowering refuses a std callback whose function parameter has a row (measured ICE, "a std callback's function parameter with a result or a row").

## How to run

Share C: GOMAXPROCS=2, `-p 2`, GOMEMLIMIT=6GiB, umask 022, private GOCACHE `/tmp/opencode/cu6/gocache`. Ad hoc checks ran in `systemd-run --user --scope -p MemoryMax=8G -p MemorySwapMax=0 nice -n 19` per common.md's memory rule, which departs from the protocol's no-own-scope line (stated there with a Departs-from line). One heavy check at a time; after each, `ps` showed none of my processes left.

- Acceptance: `go test -p 2 -count=1 -run '^TestHTTP(FetchInto|Network|Client)' ./runtime/v1 ./internal/sketch/lower` → `STEP_EXIT=0`, `ACCEPT_EXIT=0` at the head (gate log line 3–4).
- Replay: `ERGO_HTTP_SINK_SEED=<n> go test -run '^TestHTTPFetchIntoMatchesModel$' ./runtime/v1`; source: `ERGO_HTTP_NETWORK_SOURCE_SEED=<n> go test -run '^TestHTTPNetworkRuns$' ./internal/sketch/lower`.
- Round gate: `/tmp/opencode/cu6/run.sh sh /tmp/opencode/cu6/gate.sh` → `/home/exedev/.cache/baton/wf10/cli-units/cu6/gate-1.log`, exit lines `GATE_EXIT=0`, `ROUND_GATE_EXIT=0`. It runs the acceptance tests, the runtime body conventions, `-short` runtime/v1, runtime and std, the sealed/open protocol and extern rules of check, the runtime199 binding checks of lower, vet, `./internal/residue`, `TestTestCommandsHaveCacheCoverage`, gofmt of the changed Go files, `git diff --check`, `ergo fmt --check`, the API probe and the download example with its cases. The first run of the gate failed at its probe step only because the probe file still used the old argument order; the probe was fixed and the whole gate rerun green (the log holds the rerun).
- Red at base: in a detached ec6f1be2 worktree, `go run ./cmd/ergo check /tmp/opencode/cu6/probe.ergo` → `BASE_API_EXIT=1` ("http.FetchInto is not an exported declaration", "http.SinkFailed …"): `/home/exedev/.cache/baton/wf10/cli-units/cu6/evidence/check-red.txt`. The same probe checks clean at the head (gate step).
- Closure sweep: not run. The new tests read and write only `t.TempDir()` directories, outside the module (inferred from the code).

## Mutants and test value (measured: `/home/exedev/.cache/baton/wf10/cli-units/cu6/mutants.log`, `CU6_MUTANTS_EXIT=0`)

Each was applied on a copy and run with its first killer; each failed by an assertion (log shows the message), not by a build error.

- `TestHTTPFetchIntoMatchesModel` kills `cu6-budget-ignored`, `cu6-declared-length-ignored`, `cu6-redirect-body-streamed`, `cu6-partial-file-kept`, `cu6-rename-skipped`. No existing test streams a body, so no existing test can see them.
- `TestHTTPFetchIntoStops` kills `cu6-stream-not-joined` (the peer never sees the hang-up) and `cu6-stop-ignored`. The model property never stops a caller. Both mutants hang until the test's own deadline watchdog (`testwait.After`), as `TestHTTPNetworkStops` does; no fixed wait.
- The source property's sink modes kill `cu6-into-hops-lost` and `cu6-file-parts-dropped` (Ergo provider code that the runtime property does not run). It builds Go for the reason its doc comment states (typed catches at the boundary run only in a built program).
- `denied-save` kills `cu6-save-origin-lost`.
- Re-anchored CU5 mutants still killed: `cu5-no-redirects-ignored`, `cu5-caller-lost`, `cu5-domain-origin-lost`.

## Intent, Effect workarounds and prior art

Intent of Effect `HttpClientResponse.stream`: read a body larger than memory as it arrives. Its `Stream` needs the response's `Scope` to hold the connection (a library-level lifetime). Not taken: the Stream/Scope value (Ergo consumes the body inside the call, so no scope value exists to forget), the untyped stream error (here the sink's failure is `SinkFailed` in the row), `Effect.gen`/pipe and layers (an ordinary operation on `Network`, bound by context). Elixir Req `into:` (hexdocs, read 2026-11-10): a function (`FetchInto`), a collectable such as `File.stream!` used only for a 200 (`FetchFile`, which writes any final status and adds the new-file rename that a collectable lacks), or `:self`, an async body that outlives the call (not taken: #197). Gleam httpc is buffered only; ReScript uses its host's client; neither has a lowering path for this. Go control: `io.Copy` from `resp.Body` into a file and a rename, which is what the runtime runs on the same transport. No speed claim and no benchmark.

## Known gaps

- `FetchFile` writes the final body whatever the status; a caller that wants only 2xx checks the returned status (a file with an error page then exists). Req writes a collectable only for 200. Guess: a status-guard option is a follow-up if a program needs it.
- The fixed-origin `Net`/`Client` has no streamed form; only `Network` does (C6 names `Net.Fetch`; `Fetch` is the Network function). Follow-up if needed.
- The download example's real save makes an outbound request, so its recorded cases cover the refused path only; the success path is covered by the source property and the runtime property.
- The new Ergo law exemptions (`Network`, `BodyFile`) name their runtime tests.
- When #197 merges: no code here depends on it; a scoped `Seq` form can be added then.
