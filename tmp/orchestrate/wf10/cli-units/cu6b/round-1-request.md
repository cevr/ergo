ready

Protocol: supplied implementor worker protocol (not protocol.md or safety.md).
Spec: `/home/exedev/.cache/baton/wf10/cli-units/brief-3.md`, C6, with the orchestrator decision of 2026-11-10 "CU6 review" (`decisions.md`, last entry); `common.md`. No separate spec.md. Round 1 of CU6b.
Head: `ad0c2d45968f8438e92c178fc3951d3ce85f08dd` (the commit after it only records this request).
Base: `5a0a6ae4` (CU6 request commit; CU6 implementation `10dbf47a`). Branch: `wf10/cu6b`. Workspace: `/home/exedev/Developer/personal/.worktrees/ergo-cu4`. No rebase, no push.
Measured diff: `17 files changed, 600 insertions(+), 160 deletions(-)` (`git diff --stat 5a0a6ae4..ad0c2d45 | tail -1`).
Commits: `54af5d0f` feat(http): give a sink only the body of an accepted status; `61d6e657` test(http): run every sink of each network source case; `ad0c2d45` style(http): format the InMemorySendIntoIsSend law.

## Red first (measured)

`evidence/red.txt`, at `5a0a6ae4`: a scratch test (not committed; removed by script) fetched a 404 page with `HTTPIntoFile` + `HTTPFetchInto`: `RED: status 404 err <nil>; out.bin holds the error page "<html>not found</html>"`, `RED_EXIT=1`. The same case is now a generated case of `TestHTTPFetchIntoMatchesModel` (status 404, refused under 2xx), green at the head.

## The rule (decided by simpler/reversible; also in state.md)

- `type Accept = Success | Statuses{Values []int}` and `Accepts(a, status) bool` (client.ergo) are the one rule. `Success` is 200–299 and is the default of every sink form, stated once there. The runtime does not restate it: the providers hand `func(status int) bool { Accepts(accept, status) }` to the host externs.
- `StatusRefused{Status int, Prefix string}`: the final status the caller does not accept; `Prefix` is the body's first bytes, at most 1024 (runtime `httpPrefixBytes`, Ergo `refusedPrefix`). No part reaches `put`; no file is made or replaced.
- Order: a redirect that `Network` follows is not final (the follow rule comes first); then the status rule; then the declared-length budget.
- Operations take the rule: `Network.SendURLInto(r, accept, put)`; new `Net.SendInto(r Request, accept, put) Response fails RequestFailed | StatusRefused | SinkFailed`.
- Defaults (2xx): `FetchInto(r, put)`, `FetchFile(root, path, r)`, new `DoInto(r, put)`, new `DoFile(root, path, r)` (needs `Net`).
- Explicit set: call the operation; for a file, `IntoFile(root, path, fetch)` (the extern `intoFile`, now exported as `IntoFile`; `FetchFile` and `DoFile` are built on it). This keeps one file mechanism for both operation types, without four `…Accepting` variants. Reversible: a variant can be added later.
- One implementation: `Client.SendInto` runs the runtime's `HTTPDoInto`, which is `httpStreamURL` (CU6's streamed exchange) with a fixed-origin URL (`httpRequestURL`, now shared with `HTTPDo`) and no follow (a redirect is a response there, so its status meets the rule). `Handler` (in memory) answers, then gives an accepted body in one part.
- "No file is created": `BodyFile` now makes its new file at the first `Put` (or at the end, for an accepted empty body), so a refused status never touches the directory. A new file that cannot be made is kept as `failed` and `HTTPIntoFile` returns its `fs` failure (not `SinkFailed`).

## Changes by file

- `internal/sketch/std/http/client.ergo`: `Accept`, `Accepts`, `StatusRefused`, `SinkFailed` (moved from network.ergo), `Net.SendInto`, `DoInto`, `DoFile`, `Client.SendInto` + extern `sendInto` (`HTTPDoInto`), `Handler.SendInto`, `refusedPrefix`; `import fs`.
- `internal/sketch/std/http/network.ergo`: `SendURLInto` takes `accept` and fails `StatusRefused`; `FetchInto`/`FetchFile` use `Success`; `IntoFile` exported; `Remote.SendURLInto` hands the rule to `fetchInto`.
- `internal/sketch/std/http/laws.ergo`: law `InMemorySendIntoIsSend`.
- `runtime/v1/http_client.go`: `HTTPStatusRefused`, `httpPrefixBytes`, `HTTPDoInto`, `httpRequestURL` (HTTPDo's checks, shared), `httpStreamURL` gains `accepts`, `httpRefusedStatus` reads the bounded prefix (stop-aware; ends at the reader's end, failure or budget).
- `runtime/v1/http_network.go`: `HTTPFetchInto` takes `accepts`; `BodyFile` opens lazily (`open`); `HTTPIntoFile` keeps a failed open as the fs failure and opens at commit for an empty body.
- `runtime/v1/files.go`: binds `http.StatusRefused`. `internal/sketch/lower/blocking.go`: `HTTPDoInto` callerWait. `runtime199_blocking_native_test.go`: `HTTPDoInto` in `hostWaitLaws` (its wait is `httpStreamURL`'s, held by `TestHTTPFetchIntoStops`).
- `internal/sketch/check/extern_test.go`: the open `http.Net` row gains `SendInto`; the `http.Network` row's `SendURLInto` signature.
- `internal/sketch/std/extern_callers_test.go`: pinned host externs: `http.IntoFile` (renamed), `http.sendInto` (new).
- `runtime/v1/http_sink_test.go`: the model property gains statuses, accepted sets, the fixed-origin client, a missing directory and the peer-side "no new file yet" check.
- `internal/sketch/lower/std_http_network_test.go`: the source property draws a final status and runs each case through all five sinks (`fetch`, `into`, `file`, `dointo`, `dofile`).
- `internal/sketch/lower/std_http_test.go`: the law's runner restatement and statement.
- `examples/download/network/download.ergo`: `Save` catches `StatusRefused` ("status N").
- `docs/spec/stdlib.md`: http item 13 (rule, API, tests, prior art); item 12's signature and its Req sentence; the ledger row of the law.
- Ledgers: runtime/v1 +8 mutants, lower +8 mutants; `cu6-partial-file-kept` and `cu6-rename-skipped` re-anchored (their text moved to `file.f`/`file.tmp`; same edits).

## How to run

Share C: GOMAXPROCS=2, `-p 2`, GOMEMLIMIT=6GiB, umask 022, private GOCACHE `/tmp/opencode/cu6b/gocache`. Ad hoc checks ran in `systemd-run --user --scope -p MemoryMax=8G -p MemorySwapMax=0 nice -n 19` per common.md's memory rule, which departs from the protocol's no-own-scope line (stated there with a Departs-from line). One heavy check at a time; after each, `ps` showed none of my processes left.

- Acceptance: `go test -p 2 -count=1 -run '^Test(HTTPFetchInto|HTTPNetwork|HTTPClient|StdHTTPLawsRun)' ./runtime/v1 ./internal/sketch/lower` → `STEP_EXIT=0`, `ACCEPT_EXIT=0` at the head (gate log lines 3–4).
- Replay: `ERGO_HTTP_SINK_SEED=<n> go test -run '^TestHTTPFetchIntoMatchesModel$' ./runtime/v1`; `ERGO_HTTP_NETWORK_SOURCE_SEED=<n> go test -run '^TestHTTPNetworkRuns$' ./internal/sketch/lower`.
- Round gate: `/tmp/opencode/cu6b/run.sh sh /tmp/opencode/cu6b/gate.sh` → `/home/exedev/.cache/baton/wf10/cli-units/cu6b/gate-1.log`, exit lines `GATE_EXIT=0`, `ROUND_GATE_EXIT=0` at `ad0c2d45`. Steps: acceptance, runtime body conventions, `-short` runtime/v1 + runtime + std (includes `TestStdLawLedger`), check's protocol and extern rows, lower's runtime199 bindings, vet, `./internal/residue`, `TestTestCommandsHaveCacheCoverage`, gofmt of the changed Go files, `git diff --check`, `ergo fmt --check`, the API probe (`/tmp/opencode/cu6b/probe.ergo`: `IntoFile` with `Statuses`, `DoFile`, `DoInto`, `Accepts`, `StatusRefused`) and the examples `download` and `jobs` (jobs binds `Client` as `Net`). The first gate run failed only at `ergo fmt --check` of laws.ergo (two operator spacings); `ad0c2d45` formats it and the whole gate was rerun green (the log holds the rerun).
- Closure sweep: not run. The new test cases read and write only `t.TempDir()` directories (inferred from the code).

## Mutants (measured: `mutants.log` full run at `61d6e657`'s parent tree, `mutants-2.log` rerun after the fix)

Each was applied on a copy of the worktree and run with its first killer; each failed by an assertion (the log shows the message).

- `TestHTTPFetchIntoMatchesModel` kills `cu6b-status-ignored`, `cu6b-prefix-unbounded`, `cu6b-prefix-dropped`, `cu6b-eager-file` (peer sees `.out.bin.part-…` when the request arrives), `cu6b-open-failure-lost`, `cu6b-empty-file-skipped`, `cu6b-fixed-follows`, `cu6b-fixed-accepts-lost`, and the re-anchored `cu6-partial-file-kept`, `cu6-rename-skipped`. No other test streams a refused status or an empty, missing or fixed-origin sink.
- `TestStdHTTPLawsRun` (law `InMemorySendIntoIsSend`) kills `cu6b-statuses-ignored`, `cu6b-inmemory-prefix`, `cu6b-inmemory-ignores-accept`: Ergo code no runtime test runs.
- `TestHTTPNetworkRuns` kills `cu6b-success-wide`, `cu6b-remote-accept-lost`, `cu6b-client-accept-lost`, `cu6b-dofile-parts-dropped`, `cu6b-status-row-unbound`, and `cu6-file-parts-dropped`: std callers and the boundary binding run only in a built program.
- `TestHTTPFetchIntoStops` still kills `cu6-stop-ignored` (hang until the test's deadline watchdog, as in CU6).
- The first full run had two survivors, `cu6-file-parts-dropped` and `cu6b-dofile-parts-dropped`: one random sink per case left too few accepted file cases. `61d6e657` runs every sink for each case; `mutants-2.log` shows both killed (`CU6B_MUTANTS_EXIT=0`), with `cu6b-remote-accept-lost` and `cu6b-client-accept-lost` rerun too. The other lower mutants were killed by the earlier, smaller form of the same test (inferred: still killed, the test only gained cases).

## Intent, Effect workarounds and prior art

Intent of Effect `HttpClient.filterStatusOk` / `filterStatus`: a status the caller did not expect is a typed failure, not a body. Effect adds it as a client transform (a pipe step) and its `ResponseError` holds the whole response. Not taken: the transform/pipe shape (here the rule is a parameter of the operation with a stated default, so a sink cannot forget it), and the whole-response error (here a bounded prefix, so a refused body never grows memory). Elixir Req `into:` writes a collectable only for a 200 (hexdocs, read 2026-11-10); Ergo's default is any 2xx, with an explicit set for other needs. Gleam httpc is buffered only and leaves the status to the caller; ReScript uses its host's client; neither has a lowering path for this. Go's control: a `resp.StatusCode` check before `io.Copy`, which the runtime does on the same transport. No speed claim and no benchmark.

## Known gaps

- `Handler.SendInto` has no budget (Handler had none before).
- The download example's `StatusRefused` branch has no case (its saves make outbound requests; its recorded cases cover the refused origin only). The rule is covered by the runtime and source properties.
- When #197 merges: nothing here depends on it.
