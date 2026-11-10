ready

Protocol: orchestrate implementor worker protocol carried in the prompt.
Spec: `/home/exedev/.cache/baton/wf10/batch-15/fix-r7/brief.md` (no separate spec.md supplied).
Head: `99488aa137df5cb6251c60a029aab76c86f4c8ee` (code head; the next commit records only this receipt).
Branch: `wf10/batch-15-r2`. Full report: `/home/exedev/.cache/baton/wf10/batch-15/fix-r7/request.md`.

## Base and head
- Base `26d3add9372c3171262d3ba4656a124391834875`; code head `99488aa137df5cb6251c60a029aab76c86f4c8ee`.
- `git diff --stat 26d3add9..99488aa1 | tail -1`: `11 files changed, 269 insertions(+), 43 deletions(-)` (measured).

## Fix report and changed files
- Runner OOM: fixed `1af7d0756610ba7f6be19389f4a2a56ff4707202`. `runtime/explore/explore.go` reconstructs one frontier state from parent/signal indexes instead of retaining whole payloads across BFS layers. The old implementation remains selectable as a reference. `explore_test.go` extends the seeded property with both-path equality and actual retained-payload counts. Its ledger updates the Path loop anchor and adds retention/replay mutants.
- Scheduling: the same commit updates `scripts/gate/{main,steps,slots}.go` with a full-gate memory reservation for model searches. The existing class check requires that declaration; `memory_slots_test.go` counts queued/immediate reservations; the gate ledger records their mutants. Hard memory caps and proof budgets are unchanged.
- Darwin acceptance: fixed `99488aa137df5cb6251c60a029aab76c86f4c8ee`. `cmd/ergo/gocontext_test.go` uses task.Go, a Go wait-start event, Stop/join and a completed release event instead of a 20 ms scheduling delay and runtime.NumGoroutine. Its new ledger retains the release-mask and post-call interruption guards. No runtime release path changed.
- New test's unique mutants: `b15-r7-memory-slot-undercharged` and `b15-r7-memory-immediate-undercharged`; no prior test exercised multi-slot admission. Existing properties/acceptance cover all other added mutants. Measured: 19 ledger mutants killed, 0 survived, no build failures (`mutants.log`, EXIT 0).

## How to run
Use umask 022, nice 19, GOMAXPROCS=1, GOFLAGS=-p=1, GOMEMLIMIT=6GiB and a private GOCACHE.
`focused.sh` runs vet and all touched package tests plus residue/cache coverage; every log has EXIT 0. The whole scripts/gate package ran once with -v and -parallel=1; its slowest test was TestMergeTierSandboxRegression (187.11 s, measured). No new test reads repository files.

## Gate and acceptance
- Fast gate command: `bash /home/exedev/.cache/baton/wf10/batch-15/fix-r7/gate.sh`.
- Gate log: `/home/exedev/.cache/baton/wf10/batch-15/fix-r7/gate-7.log`; `gate: passed 47 steps in 495.0s`; `gate exit 0`, at the code head.
- Head acceptance: `go test -p 1 -count=1 -timeout=5m -run '^(TestSearchAgreesWithReference|TestSlotPoolReservesMemoryHeavySteps|TestFastTierHoldsNoHeavyStep|TestGoContextInterrupts)$' ./runtime/explore ./scripts/gate ./cmd/ergo`.
- Acceptance log: `/home/exedev/.cache/baton/wf10/batch-15/fix-r7/acceptance-head.log`; all three packages pass, `EXIT 0`, at the code head after the fast gate.
- Original model check: `go run ./cmd/ergo test models/runner --states 1200000` passes all 31 laws on the same code bytes (`runner-green.log`, EXIT 0). Design2 proves 1,096,512 states / 42,763,968 edges / depth 24. Design3 stops at 1,200,000 states, checked depth 13, not proved. GNU time maximum RSS: 159,468 kB (single-process maximum, not aggregate scope peak).

## Evidence, limits and decisions
- Measured: the old frontier fails the new count guard at seed 1 (`frontier-red.log`, EXIT 1). The original runner at base passed once at the reduced one-core share; the recorded gate OOM was not reproduced here. Its 2 GiB failure is the generated program's RLIMIT_DATA, not the gate's 8 GiB step cap. The gate explicitly passes --states 1200000, so Design2 does not bypass the default budget. Why that particular gate run crossed the cap remains unproved.
- Inferred: Handle.finish publishes completed releases via done before its goroutine physically exits. A global goroutine delta can therefore report a live goroutine after valid owned completion. The Mac race was not reproduced on Linux; the batch must rerun Darwin. Timeout behavior remains covered by the existing timeout suites; this acceptance now isolates Go context interruption.
- Prior art: fingerprint/parent BFS (TLC) motivates indexed replay. The project's Gleam/Elixir BEAM, ReScript JavaScript and MoonBit task-group comparisons use owned completion messages/events, not host-wide goroutine counts; their lowering paths do not change here. No new host target, speed claim or collision-free proof is claimed.
- Decided by fix-root-causes, test-through-public-interfaces and correctness-first/completed-owned-cleanup: bound retained payloads, preserve budgets/releases, and use owned events. Replay adds work proportional to witness depth; no timing assertion is added.
- Common.md 42–44 conflicts with the supplied protocol's ban on worker-created scopes. This lane followed the supplied protocol; the gate owns its scopes and generated programs keep kernel bounds. Full/merge gates and Darwin remain the batch's. No push was made.
- The one private GOCACHE is removed (`cleanup.log`, EXIT 0); process inspection found no surviving process from these checks.
