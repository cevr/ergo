# Ergo: a language design sketch

Research and proposal prepared 2026-10-05. This document describes the wider proposed language. The small runnable subset and its measurements are recorded in [prototype.md](prototype.md); syntax outside that subset remains illustrative.

> **Specification, 2026-10-27.** The normative language is now the Go-shaped language in [`docs/spec/`](spec/README.md) (v0, [#193](https://github.com/cevr/ergo/issues/193)). Sections below that it replaces carry a "Superseded by `docs/spec/`" note and stay as the record of the earlier design and of the `.ef` language, whose compiler ([`internal/compiler`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/internal/compiler)) [#205](https://github.com/cevr/ergo/issues/205) removed ([retirement record](receipts/retire-ef.md)). Owner quotes and owner rules are kept unchanged.

The [application showcases](showcases.md) apply this direction to ADTs, state transitions, decoding, task replacement, streams, durable commands and infrastructure outputs. Their [source review](research/effect-native-showcases.md) separates proposed simplifications from application policies that must remain explicit.

## Recommendation

Build a language for servers and applications with algebraic data types and Go interoperability, native lazy Effects, compiler-checked error and service rows, and explicit execution profiles. Go is the only target (owner, 2026-10-26): it uses managed goroutines and Go GC. Effect is prior art for the guarantees, not a runtime or an ABI. Keep the language's semantic and introspection model independent of the backend's runtime.

The proposition is: **an explicit, inspectable language for servers and applications where the compiler knows what a program returns, how it can fail, what services it needs, and which targets can execute it.** Operability, agent introspection, clear guardrails, server/OS access, and compile speed are foundational requirements.

Servers are the intended systems scope; kernels, bare-metal execution, and hard real-time guarantees are outside the requirements. Managed memory is appropriate for the Go target.

## Design constraints

Deferred effects need explicit execution and checked failure/service rows. Host declarations should supply routine signatures automatically, while behavior and resource ownership remain explicit contracts. Scoped concurrency must be established by the runtime, rather than assumed from the target scheduler. Application data needs closed sums and exhaustive interpretation; external data still needs runtime decoding.

## Language design principles

> **Partly superseded by `docs/spec/` (2026-10-27).** The owner rules in this section still govern. The notation members (JSX, pipe, `fn (u: User)` receiver methods, constant defaults) and the `law`/`contract` spelling describe the earlier `.ef` language. For the Go-shaped language, laws are in [docs/spec/types.md](spec/types.md#laws), and visibility follows Go's capitalisation ([grammar](spec/grammar.md#visibility)): on 2026-10-27 the owner superseded the `pub` and `pub readonly` decision below ("Yeah, let's go with your [recommendations]").

These owner rules govern every language and library proposal. They were set on 2026-10-07, and the notation members and visibility/law decisions were added on 2026-10-08.

**Construct admission (owner rule, 2026-10-07).** A language construct, meaning syntax or a checker rule, exists only to make bad code unrepresentable where a library cannot. Anything userland expresses just as well is a library API, a compiler diagnostic or a lint rule. Prefer a minimal general checker rule over ordinary language forms to new syntax. A checker rule keyed on one library identity is not that minimal alternative: under the 2026-10-09 direction below, it must become syntax or lose its compiler knowledge. Since 2026-10-12 a construct must also pass the guardrail test, the portability test and the composability checks, which the [glossary](../GLOSSARY.md) defines. The two tests are the recorded synthesis, and the checks the recorded elaboration, of the owner's 2026-10-12 directions in the [NORTH_STAR construct bar](../NORTH_STAR.md), not owner words. Under that synthesis, a construct that passes uses a keyword or block form distinct from dot and pipe, and behavior that fails the guardrail test is a primitive function plus library ([#134](https://github.com/cevr/ergo/issues/134)).

How to apply: every construct proposal, brief and review answers one question first: "Could a library express this just as well? If yes, it is not a construct." A proposal names the bad program it makes unrepresentable and shows that a bundled library version would accept that program. Prefer the narrowest construct that closes the gap. Graph or inspection needs alone do not admit syntax: a library value at a stable declaration can carry the same identity. The [construct status](specs/language-abstractions.md#construct-status) summarizes the current verdicts. Finite-profile limits, such as the user-generic and recursive-layout refusals (EF127, EF119), are current support limits, not admitted prohibitions.

**Compiler-special behavior is a visible construct (owner refinement, 2026-10-07).** Compiler-special behavior is spelled as a construct; a library API never carries hidden compiler checks or fulfilment. When the compiler must know something about a form, whether a check such as the machine `stay` rule or a structural fulfilment such as codec derivation, that form is syntax. A general checker rule over ordinary language forms, such as ownership checking, needs no new syntax; a check or fulfilment tied to one library API does. The construct still has to justify itself under construct admission by making bad code unrepresentable. The owner generalized this on 2026-10-09 to "all of our langauge constructs": a bundled or prelude library item that the compiler recognises by identity or name is never an acceptable way to grant language meaning. An item is library only when no compiler branch produces a semantic fact about it. A library intrinsic may swap an implementation and add backend retention facts, but it may not change checking. The [compiler-known constructs audit](research/compiler-known-constructs-audit.md) lists the current exceptions being removed.

**Notation exception (1:1 sugar, no semantics; owner decision, 2026-10-07).** A syntax form may be admitted without an unrepresentability argument only when it desugars one-to-one, at parse time, into ordinary calls. It must add no checking, typing, evaluation order or runtime behavior beyond those calls, and the formatter and tooling must show the desugared form on request. Each member is recorded by name, with its implementation status:

1. **JSX view notation** (decided 2026-10-07; refined 2026-10-08). Admitted, not implemented. A user-defined pragma supplies the factory, fragment and result types, and each accepted form desugars one-to-one to that pragma's ordinary calls. The owner asked for JSX 2.0 rather than a copy of the frozen JSX specification, and for user-defined pragmas rather than framework adapters, matching how an effect runtime is meant to be user-provided. The compiler hard-codes no React, Solid or view-node vocabulary. See the [JSX pragma record](research/jsx-pragmas-and-runtime-selection.md); the pragma spelling is still open.
2. **Pipe operator** `x |> f(args)` (decided 2026-10-08). Shipped. It desugars to the ordinary call `f(x, args)` the author names ([specified here](specs/language-abstractions.md#pipe-operator)). The admission is conditional: the tooling clause stays unmet until the P2 desugared view shows the call at a `|>`.
3. **Go-style receiver methods** (decided 2026-10-08, from the owner's preference for dot chaining as an equivalent of piping). Admitted, not implemented. `fn (u: User) display()` is declared in the type's owner module only, and `u.display()` is exactly `u |> User.display()` with byte-identical output. There are no methods on primitives or type parameters. `.catch`, `.provide` and `.timeout` stay postfix constructs, with reservation of their member words proposed, because the checker must know them (owner direction 2026-10-09, [audit decision D1](research/compiler-known-constructs-audit.md#decisions)). That part is superseded, as the [NORTH_STAR](../NORTH_STAR.md) 2026-10-09 construct paragraph records. The [effect rows note](research/effect-rows-as-types.md#verdict) finds `.catch<E>` and `.provide<S>` GO as ordinary library, each gated on its unit. The 2026-10-12 construct bar no longer admits a postfix construct as a construct spelling, and [#134](https://github.com/cevr/ergo/issues/134) is re-deciding provide and timeout. Until those units land, every one of these forms remains a dedicated parser form. `.orFail` may become an ordinary method only after `GoResult` is ordinary data and no checker branch remains. No receiver method carries compiler meaning. Import-scoped extension methods in the C# style are excluded: what a call resolves to would then depend on the importing file, and the call would no longer be exactly one named owner function.
4. **Constant-only parameter defaults** (decided 2026-10-08). Admitted, not implemented. A literal or named constant such as `maxActive: i64 = 1024` is inserted at the call site, so an omitted argument is the same ordinary call as the explicit constant. Calls, effects and parameter-dependent defaults are refused. Budgets and codec bounds stay required under [justify, don't ban](#language-design-principles). A changed default is a public contract change.

A notation that gains any semantics leaves this class and must pass the ordinary admission rule.

**Visibility and laws (owner decisions, 2026-10-08).** These are constructs rather than notation, and none of them is implemented in the current compiler:

- **`pub`.** Declarations are private by default, and `pub` exports them. Go's capitalized export is not adopted, so the case of a name is only a naming convention.
- **`pub readonly`.** This is the construction-authority tier. Other modules may read and match the data, but only the owner module constructs it. The spelling replaces the earlier `opaque` word for this tier, because the fields stay visible.
- **`law` / `contract`.** This is a visible construct for service and runtime laws, written `law name() uses {...} { ... }` inside a service or contract. A law suite checks the laws, and a refuted law refuses the build. The construct is visible because that checking is compiler-special.

The [capability synthesis task](https://github.com/cevr/ergo/issues/15) and the [constant-parameter-defaults task](https://github.com/cevr/ergo/issues/74) track implementation.

Together these cover every syntax proposal: a construct for semantics the compiler must know, and the notation exception for pure sugar. Anything else is a library API, a diagnostic or a lint rule.

**Justify, don't ban (owner rule, 2026-10-07).** Budgets and bounds are explicit, required choices with no silent default. `unbounded` is a legal value. A justifier lint rule in the default preset flags it until a suppression with a reason records why; a suppression without a reason is `EFL004 invalid-suppression`.

How to apply: a budget-taking API makes the budget a required argument, so omitting it is a type error and every call site shows its choice. A finite value keeps its refusal behavior. Choosing `unbounded` is reviewed at the call site through the family's justifier rule and a next-line `ergo-lint-disable-next-line <rule> -- <reason>` suppression ([lint](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/docs/tooling.md#lint)). Failure rows follow from the operation's type, not the chosen value: `unbounded` never removes a protocol failure from a row. Each budget family names its justifier rule in the [library budget choices](specs/standard-library-capabilities.md#budget-choices). The rule covers budgets only. Compiler analysis budgets (match coverage, layer graph and template work), the catch-all arm refusal (EF118) and presence contracts such as the refusal of unchecked `Option` extraction are closed-interpretation or soundness contracts, and stay fixed.

## Surface language

> **Superseded by [docs/spec/grammar.md](spec/grammar.md) and [docs/spec/rows.md](spec/rows.md) (2026-10-27).** The Go-shaped language spells a service as a capability interface, `raises` as `error[...]`, `run` as `try`, and provision as `with` and `context`. The text below describes the earlier `.ef` surface.

```rust
error NotFound { id: UserId }
error DbError { message: string }

service Users {
    effect fn find(id: UserId) -> User
        raises {NotFound, DbError}
}

service Logger {
    effect fn info(message: string) -> void
}

effect fn greeting(id: UserId) -> string
    raises {NotFound, DbError}
    uses {Users, Logger}
{
    let user = run Users.find(id)
    run Logger.info("loaded user")
    "Hello, " + user.name
}

let pending = greeting(UserId(42))
// Effect<string, {NotFound, DbError}, {Users, Logger}>
```

Calling an `effect fn` constructs a deferred program. Its body runs only when the program is executed. Within an effect body, `run` sequences another Effect, produces its success value, and propagates its typed failures and requirements. A nested Effect returned as data stays nested. Argument evaluation follows ordinary evaluation rules; pure arguments may be computed when the recipe is constructed.

Ordinary `fn` functions cannot secretly execute I/O. They may construct Effects as data. Host entry points are the execution boundary and require all service requirements to be supplied; unhandled typed errors become reported process failures. Unsafe foreign code is an explicit escape hatch.

Use familiar syntax for data: structs, enums, exhaustive `match`, generics, Option, Result, and local mutation. `run` handles Effect sequencing. A `?` operator for Result propagation is an unadmitted proposal: it is ergonomics rather than an invariant, so it must pass [construct admission](#language-design-principles) before it ships (Gleam has no such operator). Retry, timeout, tracing, provision, and parallel combinators transform Effect values rather than introducing special control-flow syntax for every feature.

## The type system is the main feature

> **Superseded by [docs/spec/rows.md](spec/rows.md) (2026-10-27).** Its [composition laws](spec/rows.md#composition-laws) restate the table below for `try`, `catch`, `with` and `timeout.After`; [footprints](spec/rows.md#footprints) settle the purity boundary of the last paragraph.

Track two normalized rows alongside the success type: failures and required service keys. A row is an unordered, duplicate-free set that may have an open tail parameter.

This is a proposed Ergo design. Failure and requirement rows have distinct roles; the current checker admits closed named rows.

| Composition | Success | Failure row | Requirement row |
| --- | --- | --- | --- |
| Sequence P then Q | Q's result | `Ep union Eq` | `Rp union Rq` |
| Recover one error type | Original/recovery result | Remove handled type; add handler failures | Original requirements plus handler requirements |
| Provide a service value | Unchanged | Unchanged | Remove supplied key |
| Provide a Layer | Unchanged | Program failures plus construction failures | Remove supplied keys; add the Layer's inputs |
| Parallel tuple | Tuple of results | Union of child failures | Union of child requirements |
| Timeout | Original result | Add Timeout | Add Clock if not already required |

Higher-order functions must preserve open rows. In mathematical notation, `retry: Effect<A,E,R> -> Policy -> Effect<A,E,R union {Clock}>` for a pure policy, and `flatMap` unions both programs' rows. A compiler that only tracks a fixed list of services in straight-line functions cannot express the intended library.

Infer private function contracts. Require exported function contracts as upper bounds, so changing an implementation does not silently enlarge every downstream API. Owner-directed [layer contracts](specs/layers.md) are a deliberate exception: infer their contracts by default and check optional bounds. Errors have nominal identities and ordinary payloads. Internally represent an error union as a compiler-generated tagged sum, rather than collapsing it to Go's `error` interface.

Service identity is nominal. A role qualifier such as `Database at Primary` versus `Database at Analytics` is an unadmitted proposal: distinct nominal services may express the same distinction, so roles must pass [construct admission](#language-design-principles) first. A service requirement identifies replaceable behavior; it is not a global variable or merely a structural interface shape.

The `R` row tracks capabilities, not every side effect or all mutation. A stronger purity claim needs additional rules for mutation, captured state, trusted intrinsics, and foreign code. Define that boundary explicitly.

## Operability and agent introspection

Treat the compiler's semantic model as a supported inspection API, shared by the CLI, language server, and agent tools. Exported function contracts remain explicit in source; layer inference and checked annotations follow their declared exception. Local inference must still produce a concise canonical type with stable nominal identities and source locations; no tool should require reverse-engineering generated Go or a large expanded generic expression.

Illustrative CLI operations, not implemented commands:

```text
ergo inspect type --at src/users.ef:42 --json
ergo inspect symbol app.greeting --json
ergo explain requirement app.greeting Users --json
ergo check --target go --json
ergo inspect build --json
ergo inspect runtime --pid 1234 --json
```

Type/symbol inspection returns success type, named failure alternatives, service/role requirements, generic/row constraints, supported target constraints, referenced declarations, and safety/trust boundaries. An explanation identifies the relevant call or binding that introduced an error or requirement. Providing a service records what was discharged and what remains. Diagnostics carry stable codes, expected/actual contracts, source spans, target, and a short causal path rather than only prose or a generic expansion.

Inspection output is schema-versioned and identifies the source/build revision. Package summaries provide cached public-contract inspection; inspecting a function body may lazily load or recheck its package. Detailed explanations can be computed on demand. Inspection should not force a whole-project check, default full tracing, or a giant metadata dump during every build.

Runtime inspection exposes the task/scope tree, ownership of managed resources, task states, instrumented wait reasons, cancellation requests and acknowledgements, active finalizers, queue/backpressure statistics, and Layer initialization failures. Correlate events with source symbols and build identity. Distinguish observed state from the last recorded safe point: arbitrary CPU-bound or foreign work cannot always provide a current high-level program location.

Use bounded event buffers and targeted/sampled tracing. Inspection describes a point-in-time snapshot with its consistency limits; it does not promise that all concurrent tasks freeze atomically. Default introspection returns metadata, not arbitrary service state or payloads. Managed-resource labels and counters can be explicit opt-ins.

Make each guardrail's enforcement level inspectable:

| Guardrail | Enforcement |
| --- | --- |
| Unhandled failure or missing service | Compile-time contract checking |
| Unsupported target capability | Compile-time target checking |
| Parent completion before managed child shutdown | Runtime scope/fiber protocol |
| Resource use after scope closure | Initially runtime handle checks; stronger region typing is separate work |
| Cross-task mutable aliasing | Transferability/alias rules and synchronized APIs; foreign declarations remain trusted boundaries |
| Raw pointers, ABI assumptions, unchecked foreign calls | Explicit unsafe/trusted declarations with visible scope and target |

The aim is a tool-answerable question for each failure: what is happening, who owns it, what contract applies, what boundary is trusted, and where the relevant source is.

## Compiler-backed MCP and semantic editing

Ship an optional `ergo mcp` entry point alongside the CLI and language server. MCP is a protocol adapter over the same semantic workspace service, not a second analyzer or a collection of shell commands that scrape compiler output. That workspace service owns incremental parsing/checking, package summaries, stable symbol handles, revisions, and explanations. It serves CLI, LSP, MCP, and build clients without changing the standalone compiler's availability.

Keep `.ef` text as the authoritative, editor/Git-friendly representation. Build a rebuildable semantic index with focused queries, revision expectations, checked edit plans and compiler-version-matched guidance. Human edits invalidate affected index entries; no separately authoritative graph file is required.

Proposed initial MCP tools (not implemented):

| Tool | Contract |
| --- | --- |
| `project.describe` | Compiler/protocol version, workspace revision, targets, available operations and binding/provider facts |
| `code.inspect` | Canonical symbol/type/contract and focused source at a symbol handle or location |
| `code.references` | Bounded reference/call/dependency results with source locations |
| `code.explain` | Why a diagnostic, requirement, failure alternative, target restriction, or safety boundary exists |
| `code.plan_edit` | Validate a structured semantic operation or source-body replacement in an overlay; return patch/diff and contract impact |
| `code.apply_edit` | Apply a validated plan against its expected source revisions and return a new revision plus verification status |
| `project.check` | Incremental checking for a selected package/target/revision, with structured diagnostics and timings |
| `project.test` | Run a selected test scope and return bounded results tied to a source/build revision |
| `runtime.inspect` | Query the managed runtime endpoint for task/scope/resource/wait snapshots tied to a running build |

Tool arguments and outputs have versioned schemas; MCP supports structured tool results and output schemas. Use read-only annotations for inspection tools, but enforce behavior in the server rather than relying on advisory metadata. Source: [MCP tools contract](https://modelcontextprotocol.io/specification/2025-11-25/server/tools).

Return handles scoped to a workspace revision, source locations, declaration identities, checked target, and the freshness/completeness of results. Preserve symbol identity across unambiguous edits where possible; never claim every expression ID survives an arbitrary text rewrite. Unresolved foreign calls and unavailable facts remain explicit. Paginate references and graph traversal, bound depth, and return named contracts by default with expansion on request.

Semantic edits support deliberate operations such as rename, add a declared provider, replace a body, handle a selected error, or wrap a particular Effect expression in timeout/retry. The compiler computes a minimal text patch preserving unrelated code/comments, checks the candidate overlay, and reports changed public contracts and affected dependents. Typed acceptance establishes contract validity, not application correctness; separate test results establish the behaviors exercised.

`code.plan_edit` records input file hashes, imported interface hashes, compiler/binding versions, target, and the generated patch. `code.apply_edit` rechecks all relevant preconditions at commit; stale or ambiguous plans fail without silently selecting a different node. Compiler-mediated edits are serialized, and multi-file changes use a recoverable write journal. Define crash recovery and file-watcher/editor coordination explicitly; unrelated filesystem readers cannot be promised an atomic multi-file view. Publish a committed workspace revision after the writes and index update succeed.

Do not store the whole program graph in the agent context. A typical loop is inspect the relevant symbol, explain the missing contract, plan a checked edit, apply it against the expected revision, then run focused checks/tests. Every step returns a compact receipt. MCP read queries reuse the existing incremental index and do not implicitly invoke the backend or rebuild the project. Heavy checks/builds are explicit operations with separate timings and result handles.

Expose compiler-version-matched language rules, diagnostics, examples, supported semantic edits, and target/foreign-binding contracts as searchable MCP resources and CLI documentation. Derive these from compiler-owned metadata where possible. This prevents an agent from inventing syntax accepted by a different compiler version.

Runtime inspection initially stays observational. It connects to explicit managed runtime endpoints; it cannot infer a complete task graph from arbitrary Go goroutines. The Go backend instruments its own managed tasks. Both project their observable facts into a common inspection schema with backend-specific fields where necessary. Mutating runtime controls, if added, are distinct operations with explicit scope and policy.

## Dependency construction and testing

> **Superseded by [docs/spec/rows.md](spec/rows.md#context) (2026-10-27)** for the surface: a `context` binds capabilities to providers and constructors, and `with` discharges them. Test providers are ordinary context entries ([docs/spec/stdlib.md](spec/stdlib.md#testing)). The construction principles below still hold.

Keep the distinction between a service contract, its implementation, and a Layer that constructs an implementation. Conceptually, `Layer<Provided, BuildErrors, Required>` is a recipe for acquiring services, possibly failing, with a scope that owns cleanup.

An application graph might be:

```text
Config -> DatabaseLive -> UsersLive -> request program
LoggerLive -------------------------> request program
```

The compiler checks missing services, ambiguous providers, roles, and statically visible construction cycles. The runtime memoizes shared Layer nodes within a graph build, rolls back partial construction, and releases services in dependency order. Memoization uses Layer node identity with one checked input context per static node, not just a service name. Explicit separate provision/build boundaries produce fresh instances; flat graphs do not gain duplicate bindings through subtree copies.

Implementations capture their acquired dependencies. A caller requiring Users should not also have to name Database solely because UsersLive uses one internally. Caller-visible failures remain those declared by the Users operations; Layer construction failures remain in the startup path.

In tests, supply UsersMemory, a recording Logger, and a controllable Clock through the same provision mechanism. The compiler checks the resulting requirements. Fake time controls Ergo timers; it does not make Go goroutine scheduling or arbitrary foreign I/O deterministic.

## Concurrency and resources

> **Superseded by [docs/spec/resources.md](spec/resources.md) and [docs/spec/runtime.md](spec/runtime.md) (2026-10-27).** Resources release automatically at the end of their owner scope, ownership moves and lends by checked rules, tasks are resources, and I1/I2 state the interruption points and masks. The runtime-guarded handles and "stronger region typing is separate work" below are replaced by those rules.

Use **scopes as the lifetime boundary** for child tasks and acquired resources. Keep three concepts distinct: Scope owns lifetimes, cancellation requests that execution stop, and Fiber represents execution plus its completion result. Cancelling one child must not close its parent's scope or release resources its siblings still use.

Scopes form a tree. Application scope owns long-lived services; request scope owns request work; operation scopes own narrower parallel groups and resources. Context may carry the cancellation signal into Go calls, but Scope closure is the operation that establishes completed shutdown.

An Ergo Fiber is a managed task whose execution uses a Go goroutine. It carries parent ownership, cancellation, completion, service context, and tracing state. Ordinary sequencing stays in the current goroutine; constructing an Effect does not spawn anything.

Define different contracts for different operations:

- `all` owns its children, collects successful results, and on failure requests sibling cancellation and waits for cleanup before returning.
- `race` selects a winner, cancels the loser, and waits for loser cleanup before publishing completion.
- Low-level `forkChild` attaches lifetime to the parent. Joining propagates the child's failure; a supervisor policy governs failures that are never joined.
- Detached work is deferred from the initial language; later it would require an explicit longer-lived owner.

Cancellation is cooperative. Managed waits observe cancellation; compiler-generated effect boundaries and effectful loop backedges are safe points. Pure infinite loops and uncooperative foreign calls can delay termination. A timeout requests cancellation; returning only after cleanup can exceed the nominal deadline. An API that returns earlier must explicitly transfer ownership of still-running work.

Go context carries cancellation across compatible library boundaries, but notification alone neither kills a goroutine nor proves that it finished. See [Go context](https://go.dev/blog/context) and [errgroup](https://pkg.go.dev/golang.org/x/sync/errgroup).

A scope owns both child work and resource finalizers. Closing it stops new work/acquisitions, requests child cancellation, invokes registered cancellation hooks needed to unblock pending operations, waits for children to release resources, then runs remaining finalizers in reverse acquisition order. An adapter may need to abort/close an I/O operation during cancellation rather than wait for ordinary release; such hooks need documented safety and idempotence. Protect the acquire/register-finalizer transition from interruption. Run cleanup in a protected cancellation region and preserve cleanup defects alongside the original failure.

Make scope lifecycle explicit: Open -> Closing -> Closed. Registration and the transition to Closing must be synchronized so shutdown accounts for every task and resource. Track in-flight acquisitions before opening resources; late successful acquisition must be released before Closed is published. Close is idempotent; concurrent close callers await the same terminal outcome. Lexical resource cleanup can lower to direct generated cleanup where ownership is statically evident; a shared runtime registry is needed for dynamic/concurrent registration, not for every ordinary variable.

Start with runtime-guarded resource handles. Scopes alone do not statically prevent a resource reference escaping and being used after closure. Later, generative region identities or affine handles could enforce stronger rules. GC manages memory reclamation; sockets, files, transactions, and subscriptions still require explicit lifetime management.

Goroutines introduce parallel access to captured values. Adopt immutable shared data and synchronized Ref/Queue/Semaphore abstractions; reject direct capture of mutable local places across forks. That rule needs alias analysis or a transferability rule to be sound for reference-containing values. Foreign pointers and mutable Go objects need trusted thread-safety contracts or an unsafe boundary. Do not advertise race freedom before these rules are implemented.

## Provider-independent machine plans

> **Surface superseded by [docs/spec/runtime.md](spec/runtime.md#machines-and-actors) (2026-10-27):** `type X machine[S, E] { initial …; step … }`. The provider-independent plan principle below still holds.

A checked machine declaration should lower to a finite, provider-independent plan containing its nominal state/event/output identities, callable rows, transition facts and entry lifetime rules. The compiler checks and lowers that plan; an ordinary typed library, function or service provider owns execution, admission, scopes, cleanup and runtime observation. Provider selection stays outside parser/compiler dispatch, as with other ordinary service ownership. See [Runtime-independent machine plans and providers](specs/machine-provider-contract.md) and [checked state machines](specs/state-machines.md) for the finite boundary and evidence gates. This design does not claim an implemented provider or cross-provider execution.

## Failure is more than a Result

> **Superseded by [docs/spec/runtime.md](spec/runtime.md#exit-causes) (2026-10-27)** for the Go-shaped language. The entry failure report below remains the current compiler's behaviour.

The runtime result should distinguish `Success(A)` from failure causes containing typed failures E, defects, or interruption. Ordinary recovery handles E. An explicit supervision/sandbox operation may inspect the full cause. Preserve multiple concurrent failures and cleanup failures instead of silently keeping only the first. The one exception is an explicit cancellation: a cancellation-aborted owner discards the ordinary typed failures of children it had not observed, because the party that cancelled already holds a different declared outcome (see [runtime](runtime.md)); defects and cleanup failures are always preserved.

Recoverable Go panics can be captured at managed task boundaries as defects and trigger cleanup. Fatal runtime failures, process termination, and arbitrary unmanaged goroutine panics cannot receive the same guarantee.

### Entry failure report

When `main`'s exit is a failure, the program entry writes an [entry failure report](../GLOSSARY.md) to stderr and exits; stdout receives nothing more. The Go executable (`runtime/ergo/report.go`) renders every failure in `main`'s checked failure row, including built-in, codec and HTTP listener failures, from one plan. There is one exception. An HTTP listener failure outside the classified set (see [HTTP server](runtime.md#http-server)) keeps the host's text, because the host describes it in its own words. [`cmd/ergo/entry_failure_process_test.go`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/cmd/ergo/entry_failure_process_test.go) checks stderr and status through `ergo run`. [`internal/compiler/entry_report_test.go`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/internal/compiler/entry_report_test.go) runs each generated entry with only its computation replaced by one that exits with a fixed composite cause; mutants of the entry's rendering and exit status make it fail. `ergo run` forwards SIGINT and SIGTERM to the program and passes its report and status through unchanged.

The report has one line per cause reason, in cause order. A scope's cause lists its body's reasons, then unobserved children in fork order, then cleanup.

```text
failure: Bad
failure: Invalid { code: 42, message: "bad \"input\"" }
defect: "live scheduler cannot advance"
interrupt
```

- A named failure prints its tag, then its payload fields in braces when it has any. A built-in failure that carries diagnostic text, such as `AssertionFailed` or `JsonDecodeFailure`, reports it as its `message` field. `Timeout` has no payload.
- A defect prints its message as a quoted string. Interruption prints `interrupt` with no host detail.
- Payload values use one grammar. Strings are double-quoted: `"` and `\` are escaped, newline, carriage return and tab use `\n`, `\r` and `\t`, other C0 controls and DEL use `\u00XX`, every other character (U+2028, characters outside the BMP) is literal, and invalid UTF-8 is replaced by U+FFFD. `i64` is decimal, `bool` is `true` or `false`, and `void` is `void`. A record prints as `{ field: value, ... }`, or `{}` with no fields. An enum alternative prints its variant name, followed by its fields when it has any, such as `Some { value: "h" }`. Bytes print as `<bytes len=N>`, a callable as `<fn>`, and any other value, such as a handle or host value, as `<opaque>`.
- Fields are listed in byte order of their source names, an order that depends only on the plan, not on declaration or construction order.
- The exit status is 130 when every reason is an interruption, and 1 otherwise.
- Text that built-in diagnostic text embeds, such as the two sides of `Assert.equalText`, uses the same quoting rule (`QuoteText`), and each test-provider defect has one message.

**Rendering is driven by checked types.** The compiler builds an entry report plan from `main`'s failure row ([`internal/compiler/entry_report.go`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/internal/compiler/entry_report.go)). For each failure label the plan records whether it is a declared failure with fields, a built-in failure with diagnostic text, or a label with no payload. For each field type it records one node: a primitive kind, a record with its sorted fields, an enum with its variants, a callable, or opaque. Nodes are keyed by checked type identity, so a recursive type refers back to its own node. The Go target lowers the plan to a table its runtime interprets. Field reads are typed accessors on the generated structs, and a variant is matched by a type assertion on its generated type. A record field named `_tag` is therefore ordinary data: `{ _tag: "a\nb" }` prints as a record, and the newline is escaped. The runtime never classifies a value by its shape or decodes generated identifiers. The plan follows the codec plan's precedent ([`internal/compiler/codec_plan.go`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/internal/compiler/codec_plan.go)): the compiler builds one target-neutral, type-directed plan, and the runtime consumes it.

Prior art: Effect's `runMain` uses `defaultTeardown` (`Runtime.ts`), which exits 130 for interruption-only causes and 1 otherwise. Go's convention for a failed `main` is to print the error to stderr and exit 1, and 130 is the shell's status for SIGINT (128 + 2). Rejected alternatives:

- **Effect `Cause.pretty`** (`Cause.ts`, `causePrettyErrors` in `internal/effect.ts`) converts each reason to an `Error` and joins their stack traces, with span annotations and interrupting fiber ids. Stack frames and fiber ids are host-specific, so the Go executable cannot reproduce them. Node's `console.error` object dump has the same problem.
- **Effect Schema-driven formatting** (`SchemaIssue` formatters and `Formatter.format`) walks a runtime schema value or the JavaScript object graph. Ergo types are checked at compile time and there is no runtime schema on Go. The entry report plan is the compile-time form of the same idea, built once from checked types.
- **Elixir `inspect` and the `Inspect` protocol** (`lib/elixir/lib/inspect.ex` at 91ee75bb) dispatch on the runtime value's type to a per-type implementation. `@derive {Inspect, only: ...}` can hide fields, and the default lists struct fields alphabetically. Ergo borrows the alphabetical field order. It rejects user-overridable implementations here: a custom renderer would bypass the checked plan, and Ergo has no protocol dispatch on Go values.
- **MoonBit `derive(Show)`** (`builtin/traits.mbt` and `String::escape` in `builtin/show.mbt` at e96ede8; deprecated in favor of `derive(Debug)`) generates a per-type `output` method at compile time. Generating a renderer function for every type would also be typed. It was rejected because it adds code for each failure type, while one interpreted plan keeps the emitted hunks small. MoonBit's escape rules (`\n`, `\r`, `\t`, `\u{hex}` for other controls) informed the string grammar; Ergo uses `\u00XX` so the escapes read the same as JSON.
- **Runtime shape inspection** (sniffing `_tag` on JavaScript objects, reflecting over Go struct and generated field names) was the first implementation. It misreported records with a `_tag` field and tied the report to generated naming.
- Go's `Cause.Error()` text was rejected because it keeps only the tags and drops the payload.
- Printing nothing for interruption, as Effect does, was rejected because a reported interruption tells an operator or agent why the process stopped.

## Compiler and Go runtime

> **Partly superseded by [docs/spec/runtime.md](spec/runtime.md#host-and-lowering-notes) (2026-10-27).** The Go-shaped language lowers by erasure: one Go function per function, direct calls, a hidden environment and task parameter, and no closure per step. The lazy-closure representation proposed below is the earlier emitter's, whose measured build cost motivated the change.

Effect typing and an Effect runtime are different design layers. Error/service rows and nominal capability identities can be checked and erased; they do not intrinsically require GC, fibers, boxed instructions, or a scheduler. First-class lazy computations do require a concrete representation for captures and execution, whose allocation and dispatch costs depend on lowering. For server workloads, managed memory and the Go runtime are appropriate, but they remain visible operational constraints rather than hidden implications of a type signature.

Effect's guarantees, not its execution model, are the reference (the JS/Effect backend that adopted Effect's execution model was removed on 2026-10-26). The Go backend may lower straight-line effect bodies to ordinary Go control flow with explicit context, result propagation, cancellation safe points, and cleanup. Optimizations may fuse away intermediate descriptions only when laziness, error/cancellation ordering, scope lifetime, and observability contracts remain equivalent. Do not promise zero allocation for arbitrary higher-order or escaping Effects.

```text
.ef source
  -> parse and resolve
  -> infer values + failure rows + capability rows
  -> typed effect IR
  -> Go code + Go package wrappers + source locations
  -> go build
  -> application binary
```

The compiler owns sum types, row inference, service identities, native Effect sequencing, target/profile constraints, and diagnostic/source-location metadata. Backend runtimes implement the contracts admitted by their profile. A Go runtime package owns managed Fibers, scope closure, Cause/Exit, Layer memoization, cancellation, timers, and tracing.

A first representation could be a lazy closure: `Effect[A,E]` containing a function from FiberContext to Exit[A,E]. Requirements can be checked by the source compiler and erased in generated Go, where service access uses nominal keys. Keep generated Go internal to this contract; external callers enter through wrappers that supply providers and translate the result.

This representation preserves laziness without requiring a port of Effect's JavaScript interpreter or another scheduler above Go. It still needs measured work on closure allocation and stack-safe deeply composed programs. Growing goroutine stacks are not an unlimited-stack guarantee. Use iterative/trampolined combinator execution or explicit suspension where necessary; optimize only after conformance tests exist.

Generated code should be readable and carry source mappings, so Go tooling remains useful. Go libraries remain Go libraries. Export wrappers translate an Ergo operation to explicit Go provider/context arguments and `(T,error)` or a richer Exit API when callers need cancellation and defects separately.

## Compile speed is a language constraint

User requirement: compilation must feel exceptionally fast, comparable to Go. Treat this as a constraint on the language and build architecture from the first prototype, rather than a later optimization. Emitting Go alone does not meet it: the pipeline adds parsing, semantic analysis, and generation before Go compilation and linking.

Go's compiler operates per package and imports compiled export data, including type information and selected generic/inlining bodies, rather than reparsing imported source at every compilation. Its build command caches compilation results. These are relevant foundations, not measured proof about Ergo. Sources: [Go package compilation](https://go.dev/cmd/compile/), [Go compiler export data](https://go.dev/src/cmd/compile/README), [Go build caching](https://pkg.go.dev/cmd/go#hdr-Build_and_test_caching).

Recommended constraints:

- Compile and check packages independently. Require explicit exported type/failure/service contracts, including generic row constraints, so clients need interface summaries rather than dependency bodies.
- Store versioned interface summaries and content-addressed artifacts. Cache keys include source, imported interface hashes, compiler/runtime versions, target, options, and Go binding metadata. An implementation-only edit can preserve the Ergo interface hash and avoid rechecking clients; Go independently decides which backend compilation/link artifacts remain valid.
- Cache imported Go type information and binding contracts against package/toolchain/build inputs. Do not walk and reparse the entire Go dependency graph for each edit.
- Give failure/service rows a dedicated compiler representation with interned nominal IDs, normalized sets, and explicit open-row constraints. Avoid implementing the model through recursive conditional types, implicit instance search, or unrestricted type-level computation. Benchmark higher-order row union/subtraction and pathological cases; ordinary set operations alone do not prove cheap inference.
- Lower Ergo generics to Go generics and let the Go toolchain instantiate them; do not add a frontend monomorphization pass or copy generic bodies per instantiation. Go compiles one body per GC shape: the type argument's underlying type, keeping struct field names, with every eligible pointer type collapsing to one shape. Identical layouts with different field names are therefore different shapes. Ergo's emitted representation decides code sharing: emit representations whose shapes coincide wherever semantics allow (no type parameters in variant markers, so every instantiation of an enum shares the interface's method set), and let the application plan report reachable instantiations so their cost is inspectable. Specialize only named operations at the call site from checked types (scalar equality, codec adapters); a per-type copy of any other generic needs a named consumer and a size receipt. Keep runtime code in reusable Go packages.
- Frontend monomorphization is refused on retained historical measurements, not on a sharing claim. The following receipts belong to the SH1 donor sequence `c65e51ad` through `0f1de7d7` and its named workloads/toolchain; they are not measurements of the current integration. Two measurements, on different workloads, bound it. (1) In a pure-Go probe of noinline generic functions over distinct structs, hand monomorphization saved about 3.9% of binary size against Go generics. (2) In the Ergo Option fixture of `TestVariantMarkersShareShapes` (64 payload records returned as `Data.Option<Rk>` through effect functions and matched, unstripped native build on go1.27.0), type-argument-free variant markers cut stenciled runtime copies from 264 to 12 and the binary from 3,414,000 to 3,034,486 bytes (11.1%). These numbers are not a matched monomorphization-versus-markers comparison. Monomorphization would also require interface summaries to carry generic bodies and would forbid exporting generic declarations to Go.
- Enum variant markers (`efVariantTemplate_*`) take no type arguments, so the Go compiler accepts `Some[B]` where `Option[A]` is expected: the method sets are identical, so `Option[B]` is assignable to `Option[A]`. Inside an Ergo program the Ergo checker is the only type authority for generated code. Constructor functions do not restore static typing for Go callers: with a type-argument-free marker, `func NewSome[T any](v T) Option[T]` still lets `wantString(NewSome(3))` compile and run with a dynamic `Some[int]` (reproduced in Go), and private variant structs do not change that. Any future exported Go surface (VIS1/API1) must therefore either expose exported generic enums through a type-mentioning public marker or a wrapper type that carries its own type argument, with its own size receipt, or accept that Go callers are checked only by Ergo's tooling. Also note the inference hazard: a bare variant struct passed to `func f[T any](o Option[T])` fails with "cannot infer T", so generated code converts to the interface type at construction.
- Emit deterministic, compact Go with stable names and file layout; write only changed bytes. Avoid timestamps and traversal-order-dependent names that invalidate otherwise reusable build artifacts.
- Parallelize independent package work. Support an optional persistent compiler for editor/watch workloads, while retaining a fast standalone compiler and reusable disk caches.

I would initially implement the compiler in Go for a native executable and close integration with the Go toolchain. That is an engineering choice, not a performance guarantee. Prototype counters and timings should separate import loading, parsing, value checking, row solving, lowering, emission, Go compilation, and linking.

Provisional performance budgets, to be validated on a fixed reference machine and matched application fixtures:

| Scenario | Proposed acceptance target |
| --- | --- |
| Warm no-op build | Ergo frontend/cache overhead below 20 ms |
| Implementation edit in a roughly 10k-line package | Ergo recheck/emission below 100 ms; unrelated packages not rechecked |
| Complete build or edit-to-binary | p95 wall time within 1.25x the equivalent Go application in the same cache regime |

These are desired budgets, not existing results. Compare cold builds, warm builds, private implementation edits, public contract edits, and dependency updates separately; include generation, compilation, and linking, and report memory use. Benchmark compiler scaling on packages and dependency graphs, plus adverse row-polymorphism fixtures. No-op cache speed cannot substitute for cold compilation performance. Keep a compiler performance regression gate alongside runtime conformance tests.

## Go interoperability is a semantic boundary

> **Surface superseded by [docs/spec/grammar.md](spec/grammar.md#declarations) (2026-10-27):** `import go "path"` charges `go.Foreign`, and decision #9 maps Go imports automatically ([Gap 21](spec/README.md#gaps)). The boundary principles below still hold.

A `(T,error)` or `(T,bool)` signature alone does not establish I/O behavior, nullability, callback retention, shared mutation or cancellation. Unsupported signatures need focused diagnostics; behavioral assertions remain separate from native declaration import.

Generate package declarations and direct Go wrappers from Go's type information, then attach explicit binding metadata for:

- effect classification and service requirements;
- nil/absence and partial-success behavior;
- error conversion into domain errors or an honest GoError fallback;
- context support, cancellation behavior, and blocking behavior;
- ownership, mutability, thread safety, and callback lifetime.

Do not automatically replace `(T,bool)` with Option: false can accompany a meaningful T. Do not discard partial results from `(T,error)`. Use per-operation contracts. Safe wrappers execute foreign work only inside the deferred Effect body. Unknown behavior gets a conservative foreign capability and runtime limitations, with explicit unsafe access available.

Start with curated fmt/logging, os/filesystem, net/http, context, and database/sql bindings. Complete Go interoperability is a major project, not an automatic consequence of emitting Go.

## The Go target

Go is Ergo's only target (owner, 2026-10-26: "we're removing the js interop id say"). Design one typed semantic core and target-neutral IR with a Go lowering path; a request for any other target, including `js`, is the EF110 diagnostic. Native Go executables are the deliverable, for ecosystem interoperability and parallel managed services. Effect stays prior art for the guarantees: Ergo's standard library covers what Effect does, restated in Ergo's idioms (owner, 2026-10-26). The JavaScript/Effect lowering that this section described until 2026-10-26, with TypeScript declarations and the pinned Effect runtime, is removed.

An operation must have an implementation for the target. Foreign signature compatibility does not prove runtime behavior, and target restrictions must remain explicit.

Keep three layers explicit:

1. Portable values and Effects: structs, ADTs, fixed-width numerical contracts, typed failures, services, and ownership/lifetime rules for managed work.
2. Host providers: HTTP, clocks, filesystem, processes, sockets, tracing, and other operations the Go host implements.
3. Host-specific systems modules: Go package types, unsafe pointers, OS-specific syscalls and platform FFI.

Service requirements are runtime dependencies. Host requirements are compile-time availability constraints; satisfying one does not automatically satisfy the other. Library package summaries include host constraints, so unavailable code is diagnosed without reparsing dependency implementations. Go OS/architecture combinations are different host profiles.

Portable concurrency promises structured child ownership and cancellation, not a fixed schedule or parallel execution. Go runs managed goroutines. A future user-selected lawful effect runtime is a scoped provider decision, not an implicit second runtime: alternate representations must declare the checked ABI, effect/service/failure/ownership rows, cancellation and cleanup behavior, and machine interoperation before admission. A provider must pass the common semantic conformance corpus and is never executed implicitly. Explicit parallel/thread/atomic facilities require a host implementation.

Define numeric and data semantics rather than silently inheriting host defaults. Specify overflow, float widths, UTF-8 text/byte conversion, equality, mutation, and codec behavior. A source type does not imply an in-memory layout; define a serialization contract for exchange with other programs.

The Go target should expose efficient server facilities: byte buffers, fixed-width integers, networking, processes, synchronization, profiling, and curated OS bindings. Go-specific unsafe or ABI-dependent operations remain explicit and visible to inspection. Do not label an operation portable when the host cannot meet its contract. A freestanding backend and no-GC execution are outside the current design scope.

### Shared post-check lowering

The target-neutral IR above is grown only as its consumers need it. The first piece is a small pass that runs after checking, reads the checked tree, and returns annotations the Go emitter consumes ([`internal/compiler/lower_tail.go`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/internal/compiler/lower_tail.go)); it builds no intermediate tree. Its first consumer turns self tail calls into loops.

This subsection describes the earlier `.ef` compiler. The Go-shaped language has Go's `for` loops ([docs/spec/grammar.md](spec/grammar.md#statements)), so the sentence below holds only for that compiler.

Ergo has no loop construct, so recursion is the only iteration. Goroutine stacks grow, but growth is not an unlimited-stack guarantee (see above), so a deep pure self recursion must run in constant stack. The loop is therefore part of the language contract rather than an optimization:

- A call is rewritten when it is a direct application of the pure function that contains it (the checker's resolved function, never a callable value or a shadowing name) and is in tail position: the last statement of the body, and recursively of every `if` branch and `match` arm that is itself in tail position.
- Go emits `for` with `continue`. Parameters become loop state; each iteration declares its own bindings from them, so anything that captures a parameter sees its own iteration's value, and every argument is evaluated, left to right, before any parameter changes.
- The pass reads the call's checked parameter-order arguments, so a labelled call updates the slots its labels bind, and a call that omits a [defaulted parameter](specs/language-abstractions.md#constant-parameter-defaults) assigns that parameter's constant on every iteration instead of keeping the previous value. MoonBit fills omitted optional arguments in shared Core IR ([`core_of_tast.ml`](https://github.com/moonbitlang/moonbit-compiler/blob/d4ada10d212b5376f7f8bf49cd2fbaa275a395df/src/core_of_tast.ml#L1602-L1614)) before contification sees the call, so a defaulted self tail call is an ordinary full-arity self call there too. Elixir instead compiles `\\` defaults into extra arities that call the full arity ([`elixir_def.erl`](https://github.com/elixir-lang/elixir/blob/91ee75bbe58ad8edeee4c43c25bffe03d91f0dad/lib/elixir/src/elixir_def.erl#L297-L307)), so a defaulted self call passes through a second function and relies on the BEAM's general tail calls; Ergo keeps one function and one loop.
- Mutual recursion, non-tail self calls (`"x" + f(n)`) and effect functions keep their recursive form. Only the functions with a self tail call change in emitted output.
- The `if` and `match` expressions on the tail path are emitted as statements inside the loop. The pass records them (`onSpine`) so that statement-position `if`/`match` lowering, planned next, can reuse the same walk instead of finding tails again.

Prior art: MoonBit contifies self tail calls into a loop in its shared IR, so mutual recursion is also left alone; Gleam's `tail_call_loop` does the same in its JavaScript backend, a compile-to-host peer; the BEAM does it for every tail call.

#### Statement and tail control flow

Checked `if` and `match` nodes on the tail path are emitted as Go statements inside the self-tail loop; Ergo's Go emitter uses ordinary Go `if` and `switch` statements for branch control. Conditions and match subjects keep source order and single evaluation, while branches stay lazy, and the emitter consumes the checker-owned match plan rather than inferring exhaustiveness or bindings again. The JavaScript statement lowering that this section described until 2026-10-26 was removed with the JavaScript target.

Pinned compiler comparisons clarify the mechanism boundary:

- MoonBit's [`pass_contification.ml`](https://github.com/moonbitlang/moonbit-compiler/blob/d4ada10d212b5376f7f8bf49cd2fbaa275a395df/src/pass_contification.ml#L126-L216) rewrites self tail calls in shared Core IR.
- Gleam's JavaScript [`tail_call_loop`](https://github.com/gleam-lang/gleam/blob/52e735c82d42811dd08d29d5508f564da081fd7d/compiler-core/src/javascript/expression.rs#L350-L390) emits a loop for tail recursion. That is a target-specific tail-call lowering, not a general statement/match pass.
- ReScript's [`js_pass_tailcall_inline.ml`](https://github.com/rescript-lang/rescript-compiler/blob/30ce3698ef683137039c7eb7ded2915b278d26ee/compiler/core/js_pass_tailcall_inline.ml#L150-L192) performs returned-call inlining; it is not self-tail loop conversion.
- Elixir's `if` is a macro over `case` ([`kernel.ex`](https://github.com/elixir-lang/elixir/blob/91ee75bbe58ad8edeee4c43c25bffe03d91f0dad/lib/elixir/lib/kernel.ex#L4045-L4063)), and `case` translates directly to an Erlang `case` in the enclosing function ([`elixir_erl_pass.erl`](https://github.com/elixir-lang/elixir/blob/91ee75bbe58ad8edeee4c43c25bffe03d91f0dad/lib/elixir/src/elixir_erl_pass.erl#L144-L145)). No closure wraps a branch, and the BEAM performs every tail call, so Elixir needs no separate self-tail pass. Go has no general tail calls, so Ergo keeps the explicit self-tail loop.

No allocation reduction or speedup is claimed; matched performance measurement remains separate work.

## TypeScript content-mapper integration

Reopened 2026-11-10 as a wire projection, not a target ([decision](decisions.md#owner-direction-2026-11-10)): Go stays the only target, and TypeScript sees an Ergo contract only as the projection of its checked operation type that Effect RPC's wire carries ([rpc.md](spec/rpc.md)). A TypeScript 7.1 content mapper written in Go serves that projection to `tsc` (unit R3'); no Ergo function lowers to JavaScript, and no Ergo build starts a TypeScript tool.

From 2026-10-26 to 2026-11-10 this section read: superseded, Go is the only target, so no TypeScript projection of Ergo source is planned. It proposed one through [typescript-go PR #4712, Content mappers](https://github.com/microsoft/typescript-go/pull/4712) (merged August 19, 2026), with the [TypeScript follow-up #63936](https://github.com/microsoft/TypeScript/pull/63936) and [LSP middleware #64583](https://github.com/microsoft/TypeScript/pull/64583) for editor features. The links stay as a record. Ergo's own LSP, CLI and MCP serve its semantic model.

## Implementation path and decision gates

The steps below were written for two targets on 2026-10-05. Since 2026-10-26 Go is the only target: the JS/Effect parts of each step, the content-mapper adapter and typed JS exports are superseded, and Go carries every step.

1. **Compiler and JS/Effect slice.** Compile a tiny program with ADTs, `effect fn`, `run`, failure/service rows, provision, and canonical JSON inspection into runnable Effect code. Expose `project.describe`, `code.inspect`, `code.explain`, and `project.check` through the same CLI/MCP semantic service. Verify higher-order row inference and selective error subtraction. Establish cold/warm/private-edit/public-edit benchmarks and interface-summary caching before expanding language features.
2. **Go runtime semantics slice.** Implement managed Fibers, cancellation, scopes, and provider construction; test acquisition races, concurrent failures, timeout cleanup, and foreign blocking. Run the supported semantic corpus on both JS/Effect and Go. Benchmark frontend and backend time independently.
3. **One real server.** Run an HTTP/database application with Layer-managed startup/shutdown, concurrency, bounded queues, selective retry, and replacement services in tests. Verify JS and Go artifacts, the Go race detector, and agent-visible type/ownership/wait explanations.
4. **Interop and operability.** Add the content-mapper adapter, language-aware LSP/MCP edits, stale-plan/crash-recovery checks, runtime task/scope inspection, typed JS exports, and Go-facing exports. Grow curated foreign bindings and streams after lifecycle semantics are stable.

An operation must have an implementation for the selected backend. Foreign signature compatibility does not prove runtime behavior, and target restrictions must remain explicit.

Defer freestanding/no-GC execution, a complete Rust-like ownership checker, general resumable algebraic-effect handlers, a replacement Go scheduler, durable workflows, and the full Effect package ecosystem. Managed task/resource ownership and safe cross-task mutation boundaries remain required. Checked service/failure rows are not the same as arbitrary continuation-capturing effect handlers.

The first proof should be concrete: a compiler rejects a missing Users provider, selective recovery removes NotFound while preserving DbError, retry repeats the deferred operation, and cancelled concurrent work releases its resources before the owner reports completion. Once those hold in a real Go binary, there is a credible foundation for Ergo.
