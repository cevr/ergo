ready
Protocol: supplied orchestrate worker protocol (implementor).
Spec: /home/exedev/.cache/baton/wf10/mapper-gaps/E3/spec.md; brief-2.md E3 and decisions.md are the design authority.
Head: 9fc1fc61b8904d90ef51ef203f83e0789fb70183 (wf10/e3, code and tests).
Base: 14338b3a. Never rebased or pushed.
Diff stat 14338b3a..9fc1fc61: 21 files changed, 768 insertions(+), 76 deletions(-).

## Changes by file

- internal/tsproj/http.go: project exported constant literal Route tables as
  stock Effect HttpApi values. Bind endpoint variants to one exported operation
  type. Read method, path, typed path parameters, one JSON payload, result and
  explicit success status. Refuse computed or incomplete/ambiguous contracts.
- internal/tsproj/project.go: add the HTTP projection to the shared TS/JS walk.
  Groups are HTTP methods: client.GET.ShowJob disappears on a move to POST,
  even when the operation signature is unchanged. No client adapter is needed.
- internal/tsproj/http_accept_test.go and http_test.go: stock client against
  jobs, call-site method/parameter drift, and projection diagnostics. The peer
  binds a manual clock in a copied jobs entry; handler, provider and codecs
  remain the example's. Host load cannot advance the lifecycle before cancel.
- internal/sketch/std/http/router.ergo: required Route.Status; Success uses it
  in the server reply, with empty 204/205 bodies. Add SuccessUsesRoute and keep
  Match's route identity laws current.
- internal/sketch/lower/std_http_test.go and docs/spec/stdlib.md: migrate every
  old route literal; run and record the new law and the updated Match laws.
- examples/jobs/api/routes.ergo and api/endpoint/endpoint.ergo: literal table,
  served Jobs contract, endpoint identities, exactly-once coverage law, and the
  member law EmptyNameIsBadBody for direct provider calls.
- examples/jobs/api/queue.ergo, handle.ergo, laws.ergo, tests.ergo and body.ergo:
  Queue captures the selected store/clock; Handle calls Jobs and http.Success.
  Bind selects providers; NewQueue builds fresh memory/manual law worlds.
  The old model law still compares responses, headers and state independently.
- examples/jobs/cmd/jobs/main.ergo and README.md: bind and document the served
  contract, not a description-only operation type. All goldens are unchanged.
- internal/tsproj/testdata/mutants.json, internal/sketch/lower/testdata/mutants.json
  and scripts/examples/testdata/mutants.json: E3 guards and their named killers.
- tools/ts/README.md and package.json: document the profile and client calls;
  version 0.2.0 invalidates TypeScript's mapped-output caches.

Implementation commits: a0555dbe (red), 8e1f5128 (feature), 25c52eaa (isolated
guards), 2b3b6a29 (runnable member law/layout/cache version), 9fc1fc61 (test clock).
The first gate found a disallowed example Go file and a lawless contract.
Both are fixed; no exemption or skipped law remains. No test flake was observed.

## Run and evidence (measured)

Share A1: GOMAXPROCS=1, go -p 2, GOMEMLIMIT=6GiB, umask 022 and one private
GOCACHE. Checks ran sequentially in an 8G, no-swap, nice-19 scope. This follows
common.md:44's explicit departure from the no-worker-created-scope rule.
No paid API, deployment or remote service was used. Dependencies were installed
only in test scratch directories, never in the checkout.

- Red: E3/red-1.log, exit 1 at a0555dbe over the base implementation:
  client.ts(4,10), the mapped module has no Routes export.
- Acceptance at head: ERGO_TS_TEST=1 go test -p 2 -count=1 -timeout 10m
  -run '^TestHttpApiClientCallsJobs$' ./internal/tsproj.
  E3/accept-2.log: exit 0. A stock HttpApiClient submits (201), reads with a
  numeric id, lists, cancels and deletes (204, void). A method-only change and
  a path-parameter rename each fail tsc at client.ts(9,...).
- Focused round gate at head: zsh E3/gate-1.sh from the assigned worktree,
  under the scope above. E3/gate-1.log: exit 0. It runs mapper tests including
  the old RPC peer suite, HTTP lowering laws/typecheck, std tests and ledger,
  the failure-catch guard, touched-package vet, formatting, all jobs module
  laws and five program cases, residue, gate cache coverage, and the mapper's
  normal Go-only closure sweep. Batch/full/merge/Mac gates remain the batch's.
- Mutants at head: ERGO=/tmp/opencode/e3-ergo bun E3/judge.mjs from the checkout
  under the same share/scope (build that checkout's CLI first).
  E3/mutants-3.log: exit 0; all 20 E3 mutants assertion-killed. The judge is
  also committed beside this request and works from its current checkout.
  Each mutant has a mutant-e3-*.log receipt. All owned process groups ended.

Named guards, each absent from the old coverage:

- TestHttpTableRefusesAmbiguousContracts: e3-missing, repeat, literal, fields,
  value, binding, bodies, no-content. Old RPC tests have no HTTP table.
- TestHttpApiClientCallsJobs: e3-table, method, path, payload, status,
  empty-status, param. Old RPC peers have neither routes nor 201/204 decoding.
- SuccessUsesRoute/TestStdHTTPLawsRun: e3-success-status and success-empty;
  old laws never build a success from a route or cover an empty 205.
- Jobs.EmptyNameIsBadBody: e3-direct-empty. DraftOf masks this provider bug
  from every HTTP request, so the old model law cannot catch it.
- RoutesCoverEndpoints: e3-route-missing and route-repeat. Repeating an
  endpoint leaves Match's answers unchanged, but the finite coverage law fails.

## Prior art and lowering

Source comparison: Effect 4.1.2 HttpApiEndpoint/HttpApiSchema/HttpApiClient
uses endpoint values, status-directed codecs and undeclared-status errors.
The mapper emits that stock model; Go remains the only application target.
Gleam/Wisp routes are handler pattern matches, not a shared TS schema graph;
ReScript bindings/genType describe exported types rather than deriving an
HTTP contract from a route table; Elixir/Plug lowers route macros to BEAM
dispatch, without a stock statically shared TS client. MoonBit's derive and
whole-program lowering are relevant to data codecs, not this HTTP projection.
These comparisons follow the pinned sources in PRIOR_ART.md; no new compiler
optimization is inferred from them.

Intent: one transport contract for server and client. Ergo uses ordinary
immutable data, operation types, contexts and laws instead of user-written
Effect endpoint/schema builders, service tags or describe-only declarations.
This serves traceability, explicit rows, Go-like data, laws and agent-readable
diagnostics. The Go control for Success is the same explicit response
construction and 204/205 branch. No performance improvement is claimed.

## Known gaps and map update

- E3b remains: a constant failure-status table and projected HTTP error schemas.
  Members with fails are allowed; undeclared failure statuses currently reach
  a stock client as Effect's undeclared-status error, not invented success.
- The finite profile refuses recursive/unsupported data as before, nonliteral
  tables, nonscalar path parameters and multiple JSON payloads. Cross-file
  TypeScript watch registration remains the tool README's existing limit.
- The closure sweep uses its normal Go-only selection; the opt-in TS leg is
  separately run with count=1 and records the source copy it reads.
- Suggested map update: E3 acceptance is met; schedule only the already-decided
  E3b failure projection. No HITL ticket was closed.
