ready
Protocol: supplied orchestrate worker protocol, implementor; wf10/common.md.
Spec: /home/exedev/.cache/baton/wf10/binsize/plan.md, BS6 and design (e)/(f); no separate spec.md.
Head: 97b42176fe21d83100a9d891b600283342060d0d (code and budget head).

# BS6 round 1: tables and paths

## Commits

- Branch `wf10/bs6`, worktree `/home/exedev/Developer/personal/.worktrees/ergo-bs`.
- Base `a37eed37`; code head `97b42176`.
- `git diff --stat a37eed37..97b42176 | tail -1`: `26 files changed, 456 insertions(+), 122 deletions(-)`.
- Preserved `708532f3` unchanged. Follow-ups: `4e3b4e80` compact tables/docs/guards; `a197eb82` direct compact reads; `97b42176` tighter size ceilings. No squash, amend, rebase or history rewrite.
- A later request-only commit carries this file at `tmp/orchestrate/wf10/binsize/bs6/round-1-request.md` on the unit branch. It changes no checked source; the code/acceptance head above is the verifier's replay head.

## What changed, by file

- `internal/sketch/lower/{lower.go,sources.go,canon.go,module.go}`: `Unit.Root` and relative emitted names, with admitted absolute identities retained for validation, ordering and refusals. Direct callers omitting Root keep their admitted paths.
- `internal/sketchfront/{load.go,sketchfront.go}`: supply the module root, or source directory when no module exists. The cache key/manifest still identifies admitted sources; this does not promise a shared cache entry across checkouts.
- `internal/sketch/lower/{calls.go,naming.go}`: intern function/callee/file strings once; each site is five uint32 words, **20 B/site**. `callLowering=wide` keeps the old emitted representation selectable. Successful application calls, polls, rows and scopes do not change.
- `runtime/v1/exit.go`: read compact fields directly in the existing defect-report loop; remove Go's `ergo.local/program/` prefix from trimmed source frames. Only wide-reference registration links its normalization map; default programs do no registration conversion.
- `paths_test.go`, `runtime/v1/{defect_test.go,calltable_test.go}`, both mutant ledgers: checkout-independent module bytes, trimmed frames, seeded report equality and the site-byte bound. Existing `calls_test.go` now spans two files and retains column assertions.
- `internal/sketchfront/sketchfront_test.go`, `cmd/ergo/{build_test.go,test_command_test.go}`: fix path assumptions at their cause; symlink tests still compare source file identity, now relative to its root.
- `lower/{dispatch_test.go,runtime199_blocking_test.go}`: recognize compact registration in existing harnesses. `std_laws_test.go` uses wide metadata for its unchanged pre-law golden, preserving its law-erasure oracle. No test was deleted.
- `internal/sizes/cmd/sizes/main.go`, `testdata/unused/{main.ergo,more.ergo}`: no checkout path in any fixture binary; hello equals hello-plus-unreachable-additions byte for byte, stripped and debug; the defect report retains its real source line (`pick at main.ergo:2`).
- `internal/sizes/budgets.json`: lower relay, kvstore and deploys ceilings to the measured Ergo-only bytes plus 2%; no ceiling increased.
- `docs/spec/runtime.md`: relative report/directive paths, preserved admission guards and compact representation.

## How to run

Use `umask 022`, `GOCACHE=/tmp/opencode/gocache-bs GOMEMLIMIT=6GiB GOMAXPROCS=1`, Go `-p 2`, and explicit watchdog timeouts. Ad hoc runs use the 8G bounded scope of common.md lines 41–45, its explicit departure from the supplied launch rule. Scratch/cache deletion uses the Written `bs6/cleanup.ts` script only (DELETION rule).

- Focused gate: `zsh /home/exedev/.cache/baton/wf10/binsize/bs6/gate.sh`.
- Acceptance: `go run -p 2 ./internal/sizes/cmd/sizes`.
- Replay the generated table property: `go test -p 2 -timeout=20m -run 'TestCompactCallsAgree/seed_N$' ./runtime/v1`.
- Wide reference CLI: `go build -p 2 -ldflags=-X=ergo.local/prototype/internal/sketch/lower.callLowering=wide -o /tmp/opencode/bs6/ergo-wide ./cmd/ergo`.
- Examples and fresh base repro: bounded `timeout --kill-after=10s 30m zsh bs6/examples-and-red.sh` (from this run directory). The example runner owns and stops its process groups.

## Gate and acceptance evidence

- `bs6/gate-1.log`: **`exit 0`**. Vet and tests of lower, runtime/..., sketchfront, cmd/ergo and sizes/...; residue; `TestTestCommandsHaveCacheCoverage`; sizes acceptance. Full compiler/runtime checks ran at `a197eb82`. The only later code change was the budget file; its focused sizes vet/tests and acceptance ran at `97b42176` and are appended to the same log. Unchanged compiler suites were not repeated.
- Acceptance command at code head `97b42176`: `go run -p 2 ./internal/sizes/cmd/sizes`; `bs6/accept.log`: **`sizes: 6 fixtures within budget`**, **`exit 0`**. Receipts: `bs6/receipts.jsonl`. This also checks the unused-addition equality, absence of checkout paths and relative defect report.
- `bs6/examples-and-red.log`: **`exit 0`**. A freshly archived/built `a37eed37` CLI produces different binaries for identical hello files in two directories and retains the checkout path (RED). Head produces byte-identical binaries and no checkout path (GREEN).
- `bs6/examples-compact.log` and `bs6/examples-wide.log`: each **`examples: passed; cases: 214`**, **`exit 0`**, at `97b42176`.
- `bs6/mutants.log`: seven killed, **`exit 0`**. Focused ledger replay (`bs6/mutants.ts`), not the all-package coverage discovery pass. Each edit was restored; no owned child process remained after the checks.
- First gate's package checks were green but its size check was red. Fixed the reporter's retained wide-reader overhead instead of raising ceilings. Evidence retained in `gate-first.log`, `accept-first.log` and `accept-reader.log`. Not a flake. The example wrapper's first invocation stopped before any example ran because noninteractive zsh could not enable monitor mode; corrected the wrapper, not an assertion.

## Tests and mutants

- Two-directory lowering: `bs6-abs-path` (new module-byte property; no host build).
- Trimmed Go frame: `bs6-trim-off` (new diagnostic-position row; no host build).
- Compact/wide registration with seeded chains, two files and ambiguous same-line calls: `bs6-site-wide`, `bs6-decode-file`, `bs6-wide-file` (new in-process property with explicit expected frames; prints its seed, stack, both tables/reports and replay command).
- Two-file source defect row: `bs6-file-index`; retained column assertions: `bs6-site-column`. This existing full-tier test needs a binary because Go supplies the actual panic frames and exit status.
- No new test reads external files; no closure allowance was added.

## Measured bytes (linux/amd64, Go 1.27.0)

| Fixture | Debug bytes | Stripped bytes | Change from BS5 (debug / stripped) |
| --- | ---: | ---: | ---: |
| hello / unused | 2304529 | 1482912 | +181 / 0 |
| pure | 2304897 | 1482912 | +165 / 0 |
| relay | 2336278 | 1507488 | -115 / 0 |
| kvstore | 2900915 | 1884320 | -3699 / -4096 |
| deploys | 2629336 | 1712288 | -7499 / -8192 |

Kvstore's no-inline link has 100 call sites in 2000 B and 106 shared string headers in 1696 B (`kvstore-nm.txt`); the source declaration table is 1920 B. Names' backing string bytes are additional and shared by Go. No latency or universal size claim is made; indexing occurs only on the defect-report path.

## Prior art and limits

Source comparison, not a fresh upstream experiment: the plan and PRIOR_ART.md record Gleam's host modules/source maps, ReScript's `js_source_map.ml`, Elixir/BEAM's host line metadata and whole-module releases, and MoonBit's compiler-side shrinking. Ergo keeps Go's own position tables instead of a second source-map runtime; relative filenames make those tables reproducible without dropping line/column information. File/name interning changes representation, not checked behavior. Go stripping stays separate from evidence of code removal.

Full conformance, full/merge gates and the Mac check remain the batch's. The compact table is proved by the in-process/property and native defect evidence above, not a speed benchmark. Follow-ups are in `../followups.md`: compile-time machine variant names plus a runtime API without init-time registration; and kvstore's corrected declaration/link attribution (**187 parser declarations − 58 dropped + 12 wrapper/shape symbols + 20 closures = 161 linked text symbols**). The historical 172 was a `^func` line count, and 140 was a different head's linked-symbol count, not a valid emission ceiling.
