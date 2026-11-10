ready
Protocol: supplied orchestrate worker protocol, implementor.
Spec: /home/exedev/.cache/baton/wf10/batch-15/fix-r4/brief.md (this round has no separate spec.md).
Head (source): e4f8eff46e019a58d958e7ddcae1e1838b453197.

# Batch 15 fix round 4: A2 and C2

Measured unless labelled inferred. Workspace: /home/exedev/Developer/personal/.worktrees/ergo-b15-r2; branch wf10/batch-15-r2. Base: 216c5829b8e480b57f4d30b50329807893399f11. Source commits: 8ebaf052 and e4f8eff4. At the source head, `git diff --stat 216c5829..HEAD | tail -1` reports `7 files changed, 813 insertions(+), 105 deletions(-)`.

The committed request copy is `tmp/orchestrate/wf10/batch-15/fix-r4/round-4-request.md`. Its receipt-only commit follows the source head; it changes no compiler, test or spec bytes. The external request and round-4-request are identical copies. State: fix-r4/state.md. Design, written before coding: fix-r4/design.md.

## Fix report

- **A2 M1, repeat of r2 M1: fixed 8ebaf052, completed by e4f8eff4.** One `yieldValue` destination handles if, both match forms, catch and with. An arm-local named result moves before its scope closes; a longer-lived enclosing name stays lent. Nested result blocks keep the consumer's boundary. Result-origin loans follow paths and expire with their call, so a later operand cannot consume a yielded lender, but a take and a lend in opposite arms do not invent a conflict. A projected part of a short-lived root is refused rather than closed early. Reserving a result place no longer puts its release after operand temporaries: the result follows them in acquisition order. Guard: TestOwnershipFlowModel now interprets tokens and ordered use/release events from Info.Releases, checking live uses, exactly one release per acquisition and reverse acquisition order at each owner end, across origins, nested blocks, match arms, lending/taking/binding consumers and operands. No B1 runtime release execution is claimed.
- **C2 M1, repeat of r2 M1: fixed 8ebaf052.** Borrow-holder legality is independent of let-mut event collection. Alias uses record the mutable root's own escaping frame, including uses in an escaping holder frame. Guard: TestCaptureRuleModel generates immutable, mutable and rebound holders, plain and nested-tuple values, six binding/value forms, and direct, immediate, non-escaping-argument, local and escaping frames. It checks Borrows 1 and the root-write rule independently. TestCaptureLoweringAgrees runs every admitted holder control under value and reference capture and compares each with the generated expected output. The existing seeded alias interpreter remains, including single-subject pattern bindings.
- **SwitchStmt borrow-pattern follow-up: rejected as an equivalent mutant; dead branch removed in 8ebaf052.** The attempted mutant survived (mut-c2-switch-clause-unbound.log, exit 0). `syntax.parseMatch`/`literalCases` produce SwitchStmt only for literal/wildcard cases; a name pattern, even with one non-tuple subject, is TypeSwitchStmt. Thus no source borrow binding can reach that branch of bindingValues. It was removed instead of testing an impossible AST. The real single-subject binding form remains in the property; c2-clause-unbound is killed. Full evidence: fix-r4/switch-pattern.md.

- repeat M1: TestOwnershipFlowModel and TestCaptureRuleModel red at 216c5829; final-property replay logs are mut-a2-red-at-216c5829.log and mut-c2-red-at-216c5829.log.
- repeat A2 M1: TestOwnershipFlowModel red at 216c5829; red-a2.log also records the original named-result use-after-release (seed 2).
- repeat C2 M1: TestCaptureRuleModel red at 216c5829; red-c2.log records the mutable captured holder admitted (holder seed 65).
- Additional ordered-event red: TestOwnershipFlowModel seed 8 at 8ebaf052, red-result-order.log; e4f8eff4 repairs it. The result-origin sibling-operand guard was red first too (red-result-origin.log).

## Files

- internal/sketch/check/ownership.go: shared result destination, origin/root handling, path-local call loans, acquisition-order correction.
- internal/sketch/check/escaping.go: independent borrow checks and alias-root events; remove unreachable SwitchStmt binding branch.
- internal/sketch/check/ownership_model_test.go: extend the existing property with token/event execution, projected roots and operand-origin controls; repair its lattice reference's early-release assumption.
- internal/sketch/lower/captures_property_test.go: extend the existing rule/interpreter and executable comparison; add holder/frame matrix and single-subject bindings.
- internal/sketch/check/testdata/mutants.json: 15 new result/holder mutants; re-anchor the older lend, R17 and tail-mode mutants.
- docs/spec/resources.md and docs/spec/types.md: state the common result-owner and independent borrow-holder rules.

No test was deleted or weakened. No production test-only export or lowering switch was added. The old coverage missed result lifetime because its reference repeated the early-release rule, and missed mutable borrowed captures because it never captured its holders.

## Run and receipts

Final runs: umask 022, GOMAXPROCS=1, Go -p 1, nice 19, GOMEMLIMIT=6GiB, private GOCACHE /tmp/opencode/gocache-b15r4 (removed before READY). Ad hoc children use RLIMIT_DATA=8GiB; generated programs also use the repository's memory-bound process helpers. Common.md lines 42–44 request a private systemd scope without a `Departs from` line; the supplied worker protocol forbids that launcher, so none was added. The gate owns its memory bound, ERGO_GATE_MEMORY=8G, and uses /tmp/opencode/heavy-gate.lock.

Acceptance at the source head (accept-head.log starts with e4f8eff46e019a58d958e7ddcae1e1838b453197):

```
go test -p 1 -count=1 -timeout 2300s -run '^(TestOwnership.*|TestMintFormNamesEveryKind)$' ./internal/sketch/check
ownership acceptance exit 0
go test -p 1 -count=1 -timeout 2300s -run '^(TestCaptureRuleModel|TestCaptureLoweringAgrees)$' ./internal/sketch/lower
capture acceptance exit 0
```

Replay: ERGO_SEED=N for ownership; `-args -capture.seed=N` for captures. Exact bounded commands are in accept-head.sh and focused.sh in this run dir.

Focused gate: focused.log ends `focused exit 0`. Whole checker, lower short suite, full capture acceptance, residue, gate cache-coverage test, checker closure sweep and check/lower vet pass; changed-Go gofmt output is empty.

Mutants: mutants-final.log ends `mutants exit 0`; mutants-order.log ends `mutants exit 0`. All 15 new ledger mutants die by test failure, not Go build failure. The earlier affected flow/capture mutants also die. The first broad harness used the wrong property for four unchanged borrows.go mutants; mutants-corrected.log records their actual TestBorrowOverlapModel killers, all exit 1 as wanted. The equivalent SwitchStmt mutant is not counted as killed. Per-child process snapshots show no child group left.

Round gate: `bash fix-r4/gate.sh` runs `./scripts/gate.sh` with the environment above. Log: /home/exedev/.cache/baton/wf10/batch-15/fix-r4/gate-4.log. Exit lines: `gate: passed 47 steps in 565.7s` and `gate exit 0`. No whole-gate cache record: bin/ergo changed during the run; residue and testinputs/guard also withheld cache records. All steps passed. One earlier invocation rejected the memory-size spelling before starting any step (gate-config-error.log); 8G is the corrected spelling, not a flaky test retry.

Source and commit-message private-name checks are empty (exit 1). No push or shared batch-worktree write was made.

## Limits and map update

These are finite property tests, not proofs. Resource evidence is the checker release plan; automatic-close runtime lowering belongs to B1. Projected results with a short-lived root are a diagnostic support limit, not an unchecked transfer. No speed claim or benchmark is made. Full/merge gates and macOS checks remain the batch's.

Inferred prior-art relation: Rust transfers MIR results before dropping arm locals and tracks path-dependent drops. Go escape analysis protects memory, not Close lifetimes. Gleam's Erlang/JS and ReScript's JS lowerings use immutable captures; Elixir versions immutable bindings before BEAM closure lowering. MoonBit's contification, DCE and monomorphization are optimization prior art, not borrow/lifetime proofs. These pinned repository comparisons support origin-based checking, not reproducing a host's unchecked capture behavior.

Map update: record the A2/C2 repair receipts against #197; this request does not close that rollup or any HITL ticket.
