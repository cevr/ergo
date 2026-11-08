# Upstream behavioral conformance

Ergo's selected cases cite Effect 4.0.1 at commit `460272d30457f4697d8b8c52cad41caccbcace08`. The repository keeps no checkout or copy of upstream: each case records its upstream file, line and test label as provenance text (see [conformance/README.md](../conformance/README.md)). Ergo never executes the upstream TypeScript suite or its shell fixtures. Future native fixtures belong to their own Ergo test owner.

[effect-cases.json](../conformance/effect-cases.json) is the maintained authority for selected behavioral comparisons. Each stable ID records an upstream test file, declaration line and label at the pinned commit, states its behavior and limits, and names the existing Ergo Go test that exercises it when applicable. Line anchors distinguish identical labels in different suites. IDs stay stable when a future upstream pin changes; review the behavior and update the anchor deliberately.

| Status | Meaning |
| --- | --- |
| `covered` | Named behavior has an existing Go acceptance test. Read its limits; it is not a claim that the entire upstream case/API was ported. |
| `difference` | Existing acceptance exercises an explicit Ergo policy that differs from the cited upstream case. The difference must stay described. |
| `pending` | A relevant behavior is planned but lacks admitted source-level acceptance. Internal runtime helpers do not count. |
| `unsupported` | The current admitted profile excludes the behavior. No passing pointer is permitted. |

Unlisted cases remain **reference-only, unassessed**. A source pointer records where evidence lives, not a stored result that stays passing forever. The validator checks the pin, the shape of each upstream record (a test file path, a positive line, a nonempty label), duplicate IDs/cases, status/evidence structure and named Go test declarations. It has no upstream copy, so it cannot check that a recorded line still holds its label; review the anchors when the pin moves. It cannot prove semantic equivalence or infer from a Go function name that its assertions cover a claim; reviewing those assertions and executing them remain required. Arbitrary extra “passed” metadata is refused.

```sh
# Mapping admission, offline
go run ./scripts/conformance check

# Execute the evidence tests: one go test per package that holds one
go run ./scripts/conformance check --run

# Full gate also runs all Go tests, including any evidence tests
./scripts/gate.sh
```

Evidence may be any Go test in a `*_test.go` file that Git tracks in the repository; an untracked test file is refused. The test proves its case in a subtest named after the case id: its body calls `t.Run("<case id>", ...)` with the id as a string literal, and `check` refuses a test whose body has no such call. `check --run` runs the evidence tests with `go test -json` and fails unless each claimed subtest (`<Test>/<case id>`) ran and passed, so a subtest that is skipped or sits in a branch not taken does not count. The gate's mapping step runs `check` without `--run`; the evidence tests run with the other Go tests.

**Since [#205](https://github.com/cevr/ergo/issues/205), the earlier cases have no evidence.** The evidence tests of the 17 `covered` and `difference` cases were in the retired compiler, so each of those cases is now `pending` with no evidence; its limits keep the earlier text after "Earlier limits:", and the [retirement record](receipts/retire-ef.md#conformance-cases-without-evidence) lists them. A case returns to `covered` or `difference` when a v1 test asserts it. The std cases `effect.cli.options-interleaved-with-operands`, `effect.string.lines-with-separators`, `effect.string.replace-all` and `effect.path.join-posix` are `covered`, and `effect.number.parse` is a `difference`, each by a subtest in `internal/sketch/lower`. The next three paragraphs record what the removed evidence asserted.

To move the pin:

1. Update `pinnedCommit` in `scripts/conformance/shared.go` and `sourceCommit` in `conformance/effect-cases.json`.
2. Re-read every mapped case at the new commit and update its file, line and label deliberately.
3. Update the prose that states the pin: the first paragraph of this page, `conformance/README.md`, and the Effect and Effect Cluster rows of `PRIOR_ART.md`. Other citations of the old commit, such as the source comparison below and those in `docs/research` and `docs/specs`, record what was read at that commit; change one only when its source is re-read at the new pin.

The first selection covered child interruption and completed child-before-parent cleanup, preservation of an expected failure plus cleanup defect, and virtual-time admission of an unstarted fork. Timeout has an explicit difference row: Ergo's nominal `Timeout` and required `Scheduler` differ from upstream `Cause.TimeoutError`; a supplemental timer-defect test ensures defects are not rewritten as timeouts. Existing lifecycle acceptance also checks unobserved child failure, but that assertion has no selected upstream case mapping yet and is not counted as another port.

Layer acquisition mapped five `Layer.test.ts` cases to traces over the emitted Go runtime. Four were covered: parallel acquisition under build cancellation, a failure preserved while a shared acquisition is abandoned, release of every acquired resource after interruption, and dependent-first finalization. Cross-build memoization is a difference row: separate Ergo provisions are separate builds with independent acquisition tables, so a failed build is retried only by a new explicit build. The first-requester interruption row is also a difference, because producers belong to the build rather than to their first waiter.

Generic acquisition, Deferred, Ref and fixed-capacity semaphore cases remain pending. Parallel scope finalization and detached child lifetime remain unsupported. Scheduler duration/precision and the rest of the corpus are not implied by the selected virtual-time fixture. Each later library, codec, server or machine family can add its own selected cases to the same mapping instead of creating another parity table.

## Acquisition and scope source comparison

Pinned `packages/effect/src/internal/effect.ts` was read from the same immutable Git object, not the newer cache working tree. [scopeClose:3929–3994](https://github.com/Effect-TS/effect/blob/460272d30457f4697d8b8c52cad41caccbcace08/packages/effect/src/internal/effect.ts#L3929) marks a scope closed, removes it from its parent and executes finalizers under protected close; sequential finalizers run in reverse registration order, while parallel finalization is an explicit separate policy. [acquireRelease:4134–4150](https://github.com/Effect-TS/effect/blob/460272d30457f4697d8b8c52cad41caccbcace08/packages/effect/src/internal/effect.ts#L4134) protects acquisition/registration by default, with an explicit interruptible option and captured release context. [acquireUseRelease:4396–4408](https://github.com/Effect-TS/effect/blob/460272d30457f4697d8b8c52cad41caccbcace08/packages/effect/src/internal/effect.ts#L4396) masks acquisition, restores interruptibility for use and registers release at the exit boundary.

The mapped interrupted-acquisition case asserts acquired=true, use=false, release=true. Ergo's existing lifecycle fixture proves release ordering and composite causes through the native Go provider; it does **not** exercise that interrupted-acquisition window through a generic Ergo callback API. A future port must add causal acquisition/register/use interruption controls and ownership/error/service admission rather than mark the row covered because both runtimes have acquire/release functions.

Deferred first completion is distinct from recipe-storage `completeWith` or producer single-flight. Ref callback purity and resource-free payload placement need source admission tests beyond upstream mutable cell behavior. Ordinary Semaphore queued-cancellation cases must not be confused with PartitionedSemaphore or mutable-capacity cases in the same upstream file. These differences remain visible as later acceptance is added.

No upstream dependency tree was installed or run. Imported reference provenance, selected Ergo correctness and runtime performance are separate evidence; this mapping makes no performance claim.
