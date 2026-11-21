# Production foundation contracts

> **Status:** describes the retired `.ef` language, removed 2026-10-28 by [#205](https://github.com/cevr/ergo/issues/205); kept as design reference. The [retirement record](../receipts/retire-ef.md) lists what was removed.

> **Go is the only target (2026-10-26).** The owner removed the JavaScript target ("we're removing the js interop id say"; [north star](../../NORTH_STAR.md#go-is-the-only-target-owner-2026-10-26)). Clauses below that name JavaScript, TypeScript declarations, Bun, Node or a pinned Effect runtime or ABI record the earlier two-target contract. They are not requirements or acceptance targets; Effect stays prior art for the guarantees.

Status: finite foundation stage implemented and reviewed through `a9da03a`, with combined race/tool receipts at `8debbc2`, 2026-10-06. See [retained evidence and limits](../receipts/foundations-2026-10-06/README.md). Implementation was authorized by the owner on 2026-10-05 through Wayfinder + implement-spec. Integration branch: `integration/production-foundations`. The [implementation graph](https://github.com/cevr/ergo/issues/26) is separate from existing HITL decision tickets; completing code does not close those decisions.

This first complete foundation spec addresses the load-bearing gaps surfaced by generic service, orchestration, lifecycle, lint and testing patterns. Broader host-object, wire-codec, bounded-stream, database and deployment facilities remain subsequent stages in the [map](https://github.com/cevr/ergo/issues/1), rather than fabricated guarantees of this stage.

## Standing constraints and acceptance seams

`.ef` source is authoritative. Public effect contracts remain explicit and locally checked; unsupported representations are diagnosed. Go is the only executable target (2026-10-26). The compiler's semantic model owns CLI/MCP facts. Use the already established public seams: admitted `.ef` source, compiler Result/runtime exported APIs, compiled CLI and real stdio MCP. Build meaningful red/green acceptance cases at these seams, never a second analyzer or implementation-shaped snapshots.

Use isolated lanes (workspaces of `work`) from the integration branch, no package install without lock changes, no push, and conventional compiling/gated commits. Implementer agents use the prescribed implementation model and TDD skill; integration/review use the prescribed review model. Root serializes ticket claims and resolutions on the GitHub [map](https://github.com/cevr/ergo/issues/1). Do not close HITL decision tickets without owner feedback.

## Closed data and exhaustive interpretation

- Nominal records and closed enums have explicit typed payload fields. Local construction checks field names, missing/extra fields and value types; identifiers retain declaration identity.
- Exhaustive matching checks all declared alternatives exactly once; invalid/missing/duplicate arms are diagnostics. Branch payload bindings are typed and scoped. The scrutinee is evaluated once and only the selected branch executes; executed failure/service rows are preserved.
- Structured failure payloads preserve their declared fields. Current simple catch remains honest; richer effectful handlers need an explicit subsequent contract if not expressible in this stage.
- Go emission keeps success/payload values typed; JS emits ordinary tagged data and truthful declarations. Recursive/unsupported layouts receive an explicit diagnostic unless correctly supported.
- CLI/MCP expose canonical type declarations, fields/variants and source spans. A generic state/transition fixture runs on both backends, with negative missing-arm/wrong-payload cases.

## Provider dependencies and lexical capture

- Provider construction dependencies and configuration are explicit and checked; supplying a provider adds its unresolved requirements to the composition contract. No silent row enlargement or blanket ambient dependency lookup.
- Provider methods capture the dependencies supplied at the lexical provision/construction boundary. Later inner overrides must not change the captured dependency. Methods use the current invocation fiber/owner, never a retained construction runtime.
- Graphs represent provider dependency and provision edges and expose incoming dependents. Unsupported fallible acquisition/sharing/cycle behavior remains diagnosed or explicitly unavailable, never implied by a pure dependency graph.
- Acceptance: a configured service consumes a supplied port, two consumers use the intended shared provider value, an inner override cannot retarget its captured dependency, missing requirements fail checking, and Go observations match the contract.

## Causal synchronization and test scheduling

- Supply a portable one-shot signal/latch with idempotent completion and cancellable waiting. Tests establish readiness/completion through signals, not fixed sleeps.
- Introduce an explicit test scheduler/clock seam shared by sleep and timeout/deadline operators. Advancing test time controls owned child sleeps and deadlines. Virtual time cannot stand in for real OS timers.
- The existing real process watchdog stays independent and reports cleanup as unconfirmed after forced termination. Default fixture mode cannot silently grant foreign host access.
- A scheduler adjustment crossing sequential sleeps must observe their intermediate registration deadlines; registration-only timer delivery can be exposed separately but is not that adjustment contract.
- Acceptance: waiter interruption does not complete/cancel an independent signal; scope close releases a waiting child; time advancement wakes the appropriate sleeper, triggers managed timeout and waits for cleanup; advancing a closed test scheduler cannot restart work. No successful result is published before owned cleanup completes.

## Ownership provenance

- Track proven owner provenance through local aliases, conditionals, scopes, data payloads and deferred captures. A value/capture proved owned by a closing inner scope cannot escape it.
- Borrowed outer handles remain usable; do not reject every File/Fiber return. Unknown foreign/function summaries are kept distinct from checked proof. Runtime guards remain in force.
- Bounded analysis retains all ownership alternatives for a selected path or explicitly preserves uncertainty. A complete retained leaf is not proof that an enclosing record/enum subtree is complete. Wrapping, successive projections and helper/capture substitution must not discard potential inner ownership merely because another descendant has evidence. Exhausted proof must diagnose an unsafe escape, never become ordinary unknown foreign provenance; independently complete borrowed siblings remain usable.
- Acceptance: reject direct, aliased and payload-contained inner-owned handle escape; allow borrowing an outer handle through an inner scope; preserve prior lifecycle behavior. This is deliberately bounded provenance checking, not a complete ownership/type system.

## Reasoned lint suppression

- Named next-line suppression applies only to optional semantic advice and requires a non-empty reason. Diagnose unknown rules, malformed directives and unused suppressions with byte spans.
- Compiler correctness diagnostics remain mandatory. A directive in a string is not a comment. Bind directives to source revision and lexical location; nested/local advice remains accurate.
- CLI/MCP diagnostics and severity policy agree. Source/comment text remains authoritative; no unsafe auto-fix deletion is implied.

## Integration exit gate

All implementation children resolve with named receipts and commits on the integration branch. The full gate and Go race suite pass; actual CLI/MCP queries agree with declarations and new metadata. Generic examples execute on both applicable targets, and proposed-versus-implemented docs match the result. Record frontend/import/tool costs without claiming a microbenchmark proves end-to-end Go speed. Perform code review, repair findings at their owner, then clean implementer Rifts. Do not mark this spec complete while any acceptance clause remains unimplemented or unverified.
