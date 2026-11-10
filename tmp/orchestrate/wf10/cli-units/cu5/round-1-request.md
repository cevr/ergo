ready

Protocol: supplied implementor worker protocol (not protocol.md or safety.md).
Spec: /home/exedev/.cache/baton/wf10/cli-units/brief.md, CU5 round 1; common.md.
Head: 0e6a0eb879165f71cbe0c0e1394ca1fccef20b6c (code and residue receipt).
Base: 8143d8aabf0fe82e67da58139198791e57cf2b76 (ready CU1).
Branch: wf10/cu5. Workspace: /home/exedev/Developer/personal/.worktrees/ergo-v1b.
Measured `git diff --stat 8143d8aa..HEAD | tail -1` at the code head:
27 files changed, 808 insertions(+), 9 deletions(-).

Full request and evidence index:
/home/exedev/.cache/baton/wf10/cli-units/cu5/round-1-request.md.

## Changes

- internal/sketch/std/http/network.ergo: explicit origin and redirect policy unions; Network/Remote/NewNet and Fetch take runtime URLs. URLRequest separates public headers from private origin-bound Credential values. OriginRefused and RedirectLimit are typed failures. Existing fixed-origin APIs remain compatible.
- runtime/v1/http_network.go: policy before every exchange, exact canonical origin identity, credential reselection each hop, hop limits and relative redirects. http_client.go shares the existing stopped-and-joined exchange, without implicit Go redirects.
- runtime/v1/files.go, runtime/embed.go and lower/blocking.go: native failure identities, embedded source and caller-task binding. The existing std authority and host-wait inventories name the new provider/extern. The residue receipt re-anchors the changed embed inventory without relaxing its check.
- runtime/v1/http_network_test.go: seeded two-peer graph/header model, public origin identity round-trip and completed interruption at a redirect target. lower/std_http_network_test.go: built-source property for credential flattening, context delegation and typed native catches.
- The runtime, lower and examples mutant ledgers name twelve regressions. No test was deleted. examples/download is a neutral domain/provider/main program with offline recorded refusals. docs/spec/stdlib.md states contracts, limits and unmeasured costs.

Commits: cdd6c758 implements CU5; 0e6a0eb8 re-anchors the residue receipt.

## Run and measured evidence

Share D: GOMAXPROCS=2, Go -p 2, GOMEMLIMIT=6GiB, umask 022, one private GOCACHE. common.md lines 42–44 explicitly authorize bounded scopes for ad hoc checks. One heavy check ran at a time; process inspections found no owned child left. No full/batch gate ran.

Round gate at the named head:
`systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19 sh /tmp/opencode/cu5-gate.sh`
Log: /home/exedev/.cache/baton/wf10/cli-units/cu5/gate-1.log.
Final exits: ACCEPT_EXIT=0, GATE_EXIT=0, ROUND_GATE_EXIT=0.
The retained first attempt found the stale exact embed-list residue excerpt. After its re-anchor the complete gate passed; this was not a flake.
The gate covers client/network acceptance, runtime body metadata/purity, embedding, std, bindings/task readers, vet, residue, cache coverage, formatting, API smoke and download tests/cases.

Acceptance at head:
`go test -p 2 -timeout 20m -count=1 -run '^TestHTTP(Network|Client)' ./runtime/v1 ./internal/sketch/lower`
Exit: STEP_EXIT=0; ACCEPT_EXIT=0.
API smoke: /tmp/opencode/cu5-red.ergo is red on exact base 8143d8aa (BASE_API_EXIT=1, missing API), green with CU5 (HEAD_API_EXIT=0), and checked again by the head gate. probes.log ends PROBES_EXIT=0.

Mutants: the graph model kills policy-bypassed, credential-leaks, extra-hop, no-redirects-ignored, zone-not-escaped, default-port-not-normalized and zone-case-folded; the interruption guard kills caller-lost. These paths do not exist in fixed-origin tests. The source property kills credential-owner-lost, origin-row-unbound and hop-row-unbound, which the runtime property cannot observe. The download case kills domain-origin-lost, outside the library's seam. All have the cu5- prefix in their ledgers.
An initial credential edit failed to compile and is not counted. Its compiling replacement fails on leaked peer headers in credential-mutant.log, CREDENTIAL_MUTANT_EXIT=0. After moving normalization to the public validation seam, its baseline and three mutations pass/fail as intended in origin-mutants.log, ORIGIN_MUTANTS_EXIT=0. All twelve valid mutants are killed. No production test hook was added.
Replay: ERGO_HTTP_NETWORK_SEED=<seed> with TestHTTPNetworkMatchesModel in ./runtime/v1; ERGO_HTTP_NETWORK_SOURCE_SEED=<seed> with TestHTTPNetworkRuns in ./internal/sketch/lower. The latter needs native execution to observe error identities and uses the existing one-binary helper and bounded execution.

## Purpose, prior art and limits

Effect HttpClient.followRedirects supplies bounded traversal intent. Ergo adds explicit destination authority and credential confinement. Ordinary operation types/context replace service tags/layers; explicit policy unions and ordinary calls replace generator/pipe/middleware shapes; failure rows and exhaustive catches replace string-tagged runtime narrowing. No language rule or syntax changed.
Checked references: Go 1.27 net/http shouldCopyHeaderOnRedirect; https://hexdocs.pm/gleam_httpc/gleam/httpc.html (follow_redirects, off by default); https://hexdocs.pm/req/Req.html (redirect, max_redirects, redirect_trusted). Req's default trust includes scheme, host and port. Go permits same-host/subdomain auth copying; marked Ergo credentials use exact origin including port. Inferred host paths: Gleam delegates to Erlang HTTP, Elixir Req runs BEAM library code, ReScript delegates to JavaScript host HTTP; no redirect-specific compiler optimization was evidenced. Ergo uses direct ordinary Go runtime calls.

Known gaps: no speed/parity claim; matched redirect benchmark harness and quiet-host receipt pending. The named hand-Go control is a net/http redirect loop with identical policy, credential selection, budgets and cancellation/join. Network's external obligations have a documented model-backed law exemption. The download provider law cannot generate Ambient-acquired http.Remote; its exact skip and reason remain explicit in expected-skips, not reported as tested. Public Header values may cross origins; secrets belong only in Credentials. This is not DNS/IP sandboxing. Streaming, retry, decompression and new timeouts are outside scope. CU1's two approved inherited T5 survivor gaps remain unchanged. No paid call, private input, rebase or push.
