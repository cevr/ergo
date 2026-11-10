ready
Protocol: supplied orchestrate worker protocol (implementor).
Spec: /home/exedev/.cache/baton/wf10/mapper-gaps/E3b/spec.md; assignment and decisions.md Q4 are the design authority.
Head: 719a52ba68f9220e6d86839c1433e89658652e54 (code and tests; the request commit follows).
Base: 47eb057ac9016d93e7ff1a87cd62db611c411ebe (E3). Branch wf10/e3b, never rebased or pushed.
Diff stat 47eb057a..719a52ba: 19 files changed, 416 insertions(+), 48 deletions(-).

## Changes (inferred from source; exercised below)

- internal/tsproj/http.go: read one exported constant literal
  []http.FailureStatus table per package. Check exactly-once failure tags
  against the routed operation rows. Refuse computed tables/fields, missing,
  repeated or unknown tags, ambiguous tables, tag collisions and statuses
  outside 400..599. Emit each endpoint's own tagged error schemas, each with
  its status, as stock Effect HttpApiEndpoint error schemas. HEAD failures
  are a diagnostic: HTTP removes their body, so tagged decoding is not possible.
- internal/tsproj/http_accept_test.go: extend the real jobs peer. A stock
  HttpApiClient catches NotFound with id and BadBody with field/reason.
  Renaming BadBody to InvalidDraft throughout the copied Ergo source leaves
  it checked, but fails tsc at the stale catchTag call site. A catch for a
  different operation's failure also fails tsc. Existing success, method and
  parameter drift checks remain. http_test.go adds projection diagnostics.
- internal/sketch/std/http/failure.ergo: ordinary FailureStatus, StatusOf
  and Failure. Failure reads the tagged JSON body and uses the same table
  for its status. There is no default status or first/last-wins lookup.
  FailureUsesTable covers lookup, status, JSON header and body preservation.
  router.ergo documents the separate failure helper.
- examples/jobs/api/failure.ergo: shared constant table and generated-row
  coverage law, run before the model law. A private row carrier admits the
  domain failures from another package without copying their types.
  routes.ergo keeps the served Jobs contract. handle.ergo no longer chooses
  contract failure statuses; laws.ergo keeps independently expected statuses.
- examples/jobs/cases/{day,refusals}/expected.out and README.md: tagged
  contract failures and their field bodies, plus the table/law layout.
- internal/sketch/lower/std_http_test.go and docs/spec/stdlib.md: execute
  and record the new std law. The three touched mutant ledgers name the guards.
- tools/ts/README.md and package.json: document this profile and bump to
  0.3.0 so TypeScript does not reuse the old mapped contract.

Commits: 40a1aed0 (red), be914b5f (feature), 9a17b931 (formatted law ledger),
1656a881 (precise error rows and HEAD refusal), 719a52ba (law order and checked
rename). The first implementation attempt found the existing foreign-union
restriction; a private anonymous-row carrier respects it. The first focused
gate found spaces missing from the law ledger; these were corrected, not
exempted. No flake or weakened assertion was observed.

## Commands and evidence (measured)

Share A1: GOMAXPROCS=1, go -p 2, GOMEMLIMIT=6GiB, umask 022, one private
GOCACHE /tmp/opencode/e3b-gocache. Checks ran sequentially under MemoryMax=8G,
MemorySwapMax=0 and nice 19. This follows common.md:44's explicit departure
from the no-worker-created-scope rule. Dependencies were installed only in
test scratch directories, offline. No paid API or owner data was used.

- Red: E3b/red-1.log, exit 1 at 40a1aed0 over the E3 implementation. tsc
  refuses catchTag NotFound and BadBody: neither is in the HTTP error channel.
- Acceptance at the code head: ERGO_TS_TEST=1 go test -p 2 -count=1
  -timeout 10m -run '^TestHttpApiClientCallsJobs$' ./internal/tsproj.
  E3b/accept-3.log: exit 0. The stock peer decodes the server's two typed
  errors; stale failure names and unrelated endpoint errors fail at call sites.
- Round gate at the code head: zsh E3b/gate-1.sh, from the worktree, in the
  bounded scope above. E3b/gate-1.log: exit 0. It includes mapper tests and
  both stock peers, HTTP lowering laws/typecheck, std admission/ledger, the
  total-catch regression, touched-package vet, formatting, all jobs module
  laws and five program cases, residue, gate cache coverage, and closure
  sweeps for the mapper and example readers. Full/merge/Mac gates are the batch's.
- Mutants at the code head: build this checkout's CLI, then
  ERGO=/tmp/opencode/e3b-ergo bun E3b/judge.mjs from the checkout, under A1
  and the same bound. E3b/mutants-2.log: exit 0; all 21 mutants killed.
  Each mutant-e3b-*.log is a receipt. The two example receipts explicitly
  refute FailuresCoverContract before the later model encounters a defect.
  The judge restores every edit and rejects build errors or survivors.
- After the checks, source was restored and the branch was clean. ps found
  no owned server, test runner or example process left.

Guards absent from the old coverage:

- Stock HTTP client: e3b-errors, status, tag and row-leak. The old RPC peer
  does not use HTTP error statuses; E3's HTTP peer decoded only successes.
- Projection diagnostics: e3b-head, missing, repeat, literal, fields, range,
  unknown and two-tables. Old route diagnostics have no failure table.
- FailureUsesTable: e3b-lookup-tag, response-status, response-body,
  response-header, lookup-repeat, lookup-low and lookup-high. The success law
  never reads failure tags or failure tables.
- FailuresCoverContract: e3b-law-missing and law-repeat. The endpoint
  coverage law knows endpoint identities, not operation failure statuses.

## Intent, prior art and lowering (inferred)

Effect HttpApiEndpoint/HttpApiSchema/HttpApiClient 4.1.2 solves the user's
problem of status-directed decoding with a checked error channel. The pinned
HttpApiEndpoint.make accepts error schemas with individual status annotations;
the mapper emits that stock form. No adapter or second contract is required.

Ergo does not copy user-written Effect endpoint/schema builders, service tags,
Layer wiring or TaggedError declarations into application source. Operation
rows, contexts, ordinary immutable tables, derived codecs and exhaustive
catch supply them. The _tag exists only at the JSON boundary and in the peer.

PRIOR_ART.md comparisons: Gleam/Wisp handles Result variants and builds HTTP
responses/codecs in handlers; ReScript/genType projects exported host types,
not a shared HTTP status graph; Elixir/Plug lowers routing macros to BEAM
dispatch and handles statuses at runtime, without a statically shared TS
error channel. MoonBit's checked errors and derived codecs are relevant to
the data boundary, not a transport-status table. Their lowering paths do not
require a new Ergo optimization here. Go remains the only application target.

The explicit Go control is a tagged-JSON read, a table scan and response
construction. The new helpers are ordinary Ergo functions lowered to Go;
rows remain checked and erased. No performance improvement or parity
measurement is claimed. This serves explicit failures, traceable transport
facts, laws and an inspectable contract without changing language rules.

## Known gaps and map update

- This finite projection requires one constant failure table per package.
  Tags are checked against nominal contract failures by the mapper; native
  tests check table coverage. A bad runtime table is a defect, not a default.
- HEAD failures are explicitly unsupported by the tagged-body profile.
  Transport refusals outside the operation row keep their existing Problem
  bodies and reach the stock client's transport/schema error channel.
- A declared union cannot include another package's records. The private
  carrier names an anonymous failure row for this example's function/law
  parameters; it does not introduce copied failure types or a compiler escape.
- Existing cross-file watch registration limits remain unchanged.
- Suggested map update: E3b acceptance is met. No HITL ticket was closed.
