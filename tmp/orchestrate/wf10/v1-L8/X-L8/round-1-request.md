ready

Protocol: supplied orchestrate worker/implementor protocol (implementor prompt).
Spec: `/home/exedev/.cache/baton/wf10/v1-L8/plan.md`, X-L8, line 292; `v1-L8/decisions.md` and common.md.
Head: `9b2d6920bb59b7efc3bab497e17422370f3732f2`, branch `wf10/x-l8`, isolated worktree `ergo-l8b`.
Base: `c398aa31` (P-L8 ready head). `git diff --stat c398aa31..HEAD | tail -1`: `9 files changed, 422 insertions(+)`.

## Change, by file (measured)

- `examples/fetchpool/domain/fetch.ergo`: domain operation types Fetcher, Conn and Limiter, with laws; typed Offline and ReadFailed; Queued/Fetching/Done/Failed download machine, exhaustive state/signal matches and report rendering.
- `memory/fetch.ergo`: recording connections, a Script law provider and a synchronized finite limiter. No host I/O or time. Each connection's Close and the limiter's Close append to a recording Ref.
- `pool/pool.ergo`: dialer task moves owned connections into queue.Queue[Conn]; worker Set borrows the queue and incoming limiter, owns each popped connection, and finishes before the queue drains its unread tail. No manual application Close or callback registry.
- `cmd/fetchpool/main.ergo`: the only provider choice, demo and one generated composed-program property. It varies download count, worker count and admission budget; checks successful, offline and read-failure reports, exactly one release per opened connection, and limiter release after every connection.
- `cases/demo/expected.out`: deterministic single-worker case, three admitted downloads and two queued leftovers; release order is 0, 1, 2, 4, 3, limiter.
- `testdata/mutants.json`: eight application mutations uniquely exercised by the example and its laws.
- `README.md`, `ergo.mod`, `tags`: Go package layout, run commands, explicit bounded-batch policy, honest compiler gap, default example registration.

## How to run

```
go build -p 2 -o <ergo> ./cmd/ergo
go run ./scripts/examples -ergo <ergo> fetchpool
```

For B1, build the compiler with `-ldflags '-X ergo.local/prototype/internal/sketch/lower.releaseLowering=scope'` and give its path to the same examples command. The runner formats, checks, builds, tests every example package and compares the recorded CLI case. Use common.md's bounded scope, umask 022, GOMAXPROCS=2, GOMEMLIMIT=6GiB and a private GOCACHE.

## Evidence (measured)

- Gate: `/home/exedev/.cache/baton/wf10/v1-L8/X-L8/gate-1.log`, **GATE_EXIT=0**, at 9b2d6920. It builds both compilers, checks source formatting, runs fetchpool's whole example acceptance under both, then residue and TestTestCommandsHaveCacheCoverage. Every step is STEP_EXIT=0. There are no changed Go packages to test or vet.
- Acceptance: `go run ./scripts/examples -ergo /tmp/opencode/ig9/x-ergo fetchpool`, **ACCEPT_EXIT=0** in `accept-1.log`; the same command and the scope compiler's command both **STEP_EXIT=0** at head in gate-1.log.
- Laws actually run: StartKeepsNumber (domain); Conn.ReadIsRepeatable for connection; Fetcher.KeepsNumber for Recording and Script; Limiter.AdmissionSpendsOne for Limit. No expected skip file and no skipped law claim. The binary's generated property runs 100 cases with replay seeds in a failing report.
- Red at base: runner pointed at an archive of c398aa31 reports `unknown program "fetchpool"`, tool exit 2, **BASE_EXAMPLE_EXIT=1** through go run (`red-1.log`). This is a new example, absent on its base, not a new compiler feature assertion.
- Mutants: `mutants-1.log`, **MUTANTS_EXIT=0**, eight judged and eight killed by examples:fetchpool: repeated read changes; admission spends nothing; dialer skips input; worker returns nonterminal state; limiter grants extra work; typed read failure becomes success; machine start changes identity; provider returns wrong identity. Ran at abc9e22f; the only later code-head change is a comment distinguishing typed source failure from interruption. The runner used an explicit example-test anchor and a focused scheduling manifest whose baseline duration was measured, not a claim of full line coverage.
- No tests deleted from the base. All example checks run in bounded scopes, share C, one heavy command at a time. Process listings after each run show no own worker/compiler/mutant descendants. No isolated HOME, no module-cache cleanup. Private cache removed after checks: PRIVATE_CACHE_REMOVE_EXIT=0, absence-check exit 0 (state.md).

## Gap found / recommendation

`gaps.md`, `conversion-lend.ergo`, `conversion-lend.log`: a concrete resource implicitly converted into a lending operation-type argument leaks under **both** selectors (minimal program prints 0; expected 1). The example first exposed the missing limiter Close. Its composition root now declares the owned provider local as domain.Limiter, the normal contract-facing binding, and documents that unsupported conversion path. Inferred owner seam: A4/B2 conversion-temporary ownership, not static release. Recommend a separate neutral compiler unit with this reproducer; do not special-case this example. No out-of-scope compiler edit was made.

## Design and limits

- Effect intent: acquire/release and bounded concurrent processing. Not copied: runtime Scope values, service tags, callback lifetime registries or hand epoch checks. Ergo uses owned values, operation types with laws, checked rows, contexts, task/Set lifetimes and structured lends. The control shape is a Go buffered channel plus worker group. No speed claim.
- Simpler reversible policy: an input-sized queue filled before workers start. It still transfers resources through a dialer task, queue and worker Set, but is a bounded batch example, not a streaming network client. This makes tail release order deterministic without timers.
- Neutral original example only; no private application source, names or endpoints. No main merge or push.
- P-L8 remains READY at c398aa31; its benchmark remains pending a quiet host. Its request attributes the pre-existing timeout-shape failure to B4 commit 71dd4282 by adjacent-commit bisection.
- Map update: X-L8's executable example clauses are satisfied; the conversion-temporary gap needs a follow-up unit.

## Prior art (inferred from pinned project comparisons)

Go channels and worker groups are the execution shape, but Go leaves Close discipline to callers. Effect's acquire/release and scoped fibers supply the guarantee, not the API. Gleam and ReScript lower explicit host cleanup calls without affine ownership; Elixir/OTP ties ports and process cleanup to VM lifetimes, not this queue ownership plan. MoonBit's task groups and defer are closer lifecycle comparisons. Ergo makes each resource hand-off and each structured lend a checked fact and keeps typed failures distinct from interruption.
