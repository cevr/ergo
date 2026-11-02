# Ergo glossary

Ergo is "Effect for Go": a Go-shaped language whose compiler checks Effect's guarantees (typed failures, explicit dependencies, structured concurrency, completed cleanup) and emits Go. Go is the only target (owner, 2026-10-26). The normative rules are in [`docs/spec/`](docs/spec/README.md). Every domain term is defined only here; the spec files state rules and link to these definitions.

The sections are:
- **Language**: the Go-shaped language of `docs/spec/`.
- **Constructs, contracts and laws**: how constructs are admitted, and the vocabulary of laws and evidence.
- **Runtime and providers**: tasks, interruption, exits and lawful providers.
- **Earlier `.ef` language**: terms of the retired language that [#205](https://github.com/cevr/ergo/issues/205) removed with its compiler. They stay as design reference.
- **Tooling and testing**.

A status in parentheses applies to the term's support: *checked* (the 2026 checker `internal/sketch/check`, [#196](https://github.com/cevr/ergo/issues/196), checks it; building may still need [#50](https://github.com/cevr/ergo/issues/50)), *implemented in prototype* (`prototypes/sketch9`), *designed* (decided, not implemented), or, in the earlier-language section, the earlier labels.

## Language

**Package**: One unit of compilation, declared by `package name`, lowered to one Go package. The prototype compiles one file as the whole program.

**Exported name** (designed; owner decision 2026-10-27): A package-level name, field or method whose first character is an upper-case letter. Only exported names are visible to other packages, as in Go. See [grammar](docs/spec/grammar.md#visibility).

**Capability** (checked): An interface with method signatures that names replaceable behaviour, such as `Orders` or `Clock`. Code reaches it only by calling its methods on the capability name, `Orders.Find(id)`, and every such call charges the capability to the caller's capability row. The capability is not a first-class value: it cannot be passed, returned or stored (S104). See [types](docs/spec/types.md#capabilities) and [rows](docs/spec/rows.md#capability-rows).

**Interface value** (checked): A value of an interface type in a type position, such as a parameter `o Orders`. It holds a provider value. Calling its method runs that provider's method with the method's declared rows and charges no capability. A type is assignable to the interface by method-row compatibility ([types](docs/spec/types.md#interface-assignability)).

**Provider** (checked for provider values): A value whose type implements a capability's methods, bound to that capability by a `wire`. A provider may fail less than its capability, never more.

**Constructor** (designed): An ordinary function named as a `wire` entry, such as `Orders = OpenPostgres`. Its requirements are satisfied from the same wire, and its failures become startup failures of the `with` that builds it.

**Captured provider** (designed): A provider that holds the capabilities its constructor used. Its methods use them without a `needs` clause of their own.

**Prelude**: The names in scope in every package without an import: `Option`, `Task`, `Decision`, `Clock`, `Scheduler`, `Random` and `Console`.

**Host package** (designed): A reviewed standard-library package, such as `os`, `fs`, `net` or `sql`, that calls Go. Only host packages and `import go` reach the host, so every effect appears in some footprint.

**Error type** (checked): A named struct type that an error row names: in a package, one named by an error row of that package or of the package that declares it. Returning a value of an error type is a failure, never an ordinary result.

**Error row** (checked): The set of error types a function may fail with, written `fails A | B` after the result type. Order and repetition carry no meaning.

**Capability row** (checked): The set of capabilities a caller must supply, written `needs A, B` after a signature. Also called the `needs` row or requirement row. Before 2026-10-27 it was spelled `uses` ([#217](https://github.com/cevr/ergo/issues/217)); `uses` is now an ordinary identifier.

**Row**: An error row or a capability row. Rows are checked and then erased from the emitted Go.

**Inferred row** (checked): A row the checker computes from a body because the row is not written: the rows of an unexported function with a body and of a function literal. A written row is declared and is an upper bound. Inference runs per strongly connected component of a package's reference graph.

**Export guard** (checked): The rule that a declaration other code depends on spells its rows: exported functions, operations, `main`, laws, bodiless declarations and the step of an exported machine type. An unwritten row there is empty, never inferred.

**Requirement binding** (checked): The bare name of a capability a written `needs` row lists, entered in the body's frame beside the parameters. A local of that name in the same frame is an error.

**Implicit type parameter** (checked): A name of one upper-case letter, optionally followed by digits, that names no declaration in a signature. Its kind follows its position: a type, an error row after `fails`, or a capability row after `needs`.

**Fallible expression** (checked): An expression that can fail with a non-empty error row and whose failure is not yet handled. It must be tried, caught or returned.

**Escape** (checked): The passage of a failure from an expression to its failure target, through `try`, a fallible `return`, a returned error value or the rest of a partial catch.

**Failure target** (checked): Where an escaping failure goes: the innermost enclosing function or function literal. A trailing block is a function literal, so the block of `timeout.After` is one. A `with` block is not a failure target.

**Try** (checked): The keyword form `try e`, which yields `e`'s success value and escapes `e`'s failures.

**Catch** (checked): The keyword form `e catch { case T: … }`, which handles named error types of a fallible expression. It covers typed failures only; interruption and defects pass through it.

**Total catch** (checked): A `catch` without `try`. It must handle every error type of the caught row.

**Partial catch** (checked): `try e catch { … }`. It handles the named types, and `try` escapes the rest.

**Discharge** (checked): Removing a capability from the row a block charges to its enclosing function, because an enclosing `with` binds it.

**Wire** (checked for provider values; constructors designed): A `wire { Cap: provider, … }` value that binds capabilities to providers or constructors. It is Ergo's form of an Effect layer.

**Wire exposure** (parsed; S109 naming [#196](https://github.com/cevr/ergo/issues/196) stage b, which checks it): The `provides A, B` clause of a named wire header: the capabilities a `with` of that wire discharges. Entries the wire builds and does not list stay private to its build. A header may also state the wire's requirements, `wire App provides Account, Invoice needs Config { … }`, which join the `with` site's capability row.

**Inline wire** (checked): A `wire{ … }` literal given directly to `with`. Its provider expressions are evaluated in, and charged to, the enclosing function.

**Named wire** (checked): A top-level `let Name = wire { … }`. It is built by its own function at each `with` that names it.

**Closed wire** (checked; open named wires are [#37](https://github.com/cevr/ergo/issues/37)'s): A named wire whose providers need no capability from the caller and let no failure escape. The checker admits only closed named wires.

**Build** (designed): One execution of a wire at one `with`: constructors run once per shared node, and the build owns their releases until the `with` block ends. Each `with` is a new build.

**With** (checked for one wire): The compiler-known form `with(W) { block }`, spelled as a predeclared identifier. It discharges the capabilities `W` binds for the block and yields the block's last expression. It also takes context values (designed).

**Context value** (designed): An immutable value, such as a span or a log annotation, that `with` scopes to a block and that every task started inside inherits.

**Footprint** (designed): Every capability a function's body exercises, including the ones its own `with` provides, plus `go`. The compiler infers it; it is never written.

**Pure function** (designed): A function whose footprint is empty. An empty capability row does not make a function pure, and a call through an unknown function value counts as impure.

**Union** (checked): A closed set of struct types, written `interface{ A | B }`. It has no zero value.

**Variant** (checked): One struct type listed in a union. A variant value is assignable to every union that lists it.

**Defaultable type**: A type with a valid zero value: a basic type, or a struct whose fields are all defaultable. Unions, capabilities, function types, tasks, resources and borrows are not defaultable. Outside its package, a struct with an unexported field is not defaultable either, so its only values there come from the package's exported functions.

**Definite initialisation** (checked for `var x T`; the rest designed, [#197](https://github.com/cevr/ergo/issues/197)): The rule that a variable of a non-defaultable type has a value before it is used.

**Type switch** (checked, with one or several subjects): `match u { case A as v: … }` over a union value, narrowing `v` to the case's variant. With several subjects, `match (a, b)`, a case is a tuple of variants, and `_` matches any variant.

**Totality**: The property of a type switch, a `catch` or a machine step that covers every alternative. A total form needs no default for its declared alternatives. The checker decides it for a type switch by a usefulness check over its case matrix, never by enumerating the product of the subjects.

**Irrefutable pattern** (checked): A pattern that matches every value of its type, such as a name, a tuple of names or a record pattern of the value's own type. `let` and `for` take only irrefutable patterns; a partial one belongs in a `match`.

**Row value** (checked): The value `case err:` binds in a `catch` arm: the failures the earlier arms leave, typed as the anonymous union of their error types. It can be returned (a failure of those types), matched, caught or assigned to a declared union that lists each of them; it has no other operation.

**Block**: A braced statement list. As the body of `with`, `timeout.After` or a trailing block, it yields its last expression, and `return`, `break` and `continue` are errors inside it.

**Trailing block** (checked; the build lowers it only for `with` and `timeout.After` until #50): A block written after a call's parentheses. It is notation for a function-literal final argument whose body contains no `return`, `break` or `continue`; an early return needs an explicit `func` literal. It cannot escape the call unless the parameter type says so. The block of `with` is part of that construct instead.

**Non-escaping parameter** (designed): A function-typed parameter that the callee may call, or hand to a task the call consumes, but may not store, return or send.

**Concurrent parameter** (designed): A function parameter that the callee hands to `go`, directly or through another call. The compiler infers the fact and records it in the interface summary. An argument to it may not assign captured locals.

**Replayable parameter** (designed): A function parameter that may run more than once, such as a transaction body. It may not assign captured locals, use channels or consume a resource, and its footprint holds only `sync.Tx`.

**Borrow** (designed): A `*T` parameter or receiver. It is never nil, stored, returned, sent or captured by `go`, and two mutable borrows of one value must not overlap.

**Resource** (designed): A value whose type has a `Close()` method, a struct with a resource field, or a slice, array or map with a resource element type. A task and an actor handle are resources. Its owner scope releases it. See [resources](docs/spec/resources.md).

**Owner scope** (designed): The block that owns a resource: the block that acquired it, or the scope it moved to. A loop body is its own owner scope for each iteration.

**Release** (designed): Closing a resource at the end of its owner scope, in reverse acquisition order, on every exit. A release is infallible and runs masked.

**Move** (designed): A transfer of a resource's ownership: by `return`, into a provider field, into a `go` task outside a structured lend, by building a struct or container that holds it, or by assignment. A channel send of a resource is an error in v0. The source cannot be used after a move.

**Lend** (designed): Passing a resource as an argument, capturing it in a non-escaping closure, or indexing it out of a container. The owner keeps ownership; the borrower may not store, return, send or capture it, except by a structured lend to a task it releases before it returns, so a lend needs no lifetime annotation.

**Structured lend** (designed): A lend of a resource to a task, or to an escaping closure an owner keeps, that is allowed because the task or owner is certain to be released before the borrow of the resource ends, on every exit. The borrow ends at the release point of the resource's root owner: a value reached by indexing or through a field keeps the root owner of its container or aggregate. The task qualifies when it is bound after the root owner in the same scope, owned by a scope nested inside the root owner's scope, stored in a later field of the same struct, or stored in a container whose owner qualifies. Only for an incoming parameter, whose owner outlives the call, does the borrow end at the function's return, so any task owned by a scope local to that function qualifies; moving or returning that task out of the function is an error. See [resources](docs/spec/resources.md#structured-lend).

**Consume** (designed): Ending a resource early by calling `Close()`, or a task's `Wait()` or `Stop()`. Nothing runs for it at scope end, and a later use is an error.

**Generated Close** (designed): The `Close` the compiler gives a struct with resource fields. It closes those fields in reverse declaration order.

**Task** (designed in source; the task tree is implemented in prototype): A unit of concurrent work started by `go`, of type `Task[T, E]`. It is a resource; `Wait` returns its result and `Stop` interrupts it and waits for it.

**Task tree** (implemented in prototype): The parent and child relation of tasks. Stopping a task stops every task below it.

**Set** (designed): `tasks.Set(policy)`, a resource that owns an unbounded number of tasks and applies a fail-fast, log or restart policy (G4).

**Interruption** (implemented in prototype): A request that a task stop. It is cooperative and is not a typed error: no error row names it.

**Interruption point** (implemented in prototype before capability calls): A place where a task checks for interruption: a capability call, a channel operation, `Wait`, `Sleep` or a loop back-edge in a function with a footprint (I1).

**Mask** (designed): A region that interruption cannot enter: releases, `Close`, `scope.OnExit` bodies, wire cleanup and the step between acquisition and its registration (I2).

**Exit cause**: How a task ended when it did not succeed: one or more typed failures, defects and interruptions, all kept.

**Defect**: A failure outside every row, such as a Go panic in Ergo code or a crash in `Close`. It re-raises in the parent at the join.

**Span** (designed): A named trace region, scoped by `with(trace.Span(…))`, keyed to a checked definition identity.

**Machine** (designed): A declaration `type X machine[S, E] { initial v; step f }` of a state machine over the unions `S` and `E`. Its meaning is a plan any lawful provider can run. In the earlier language (specified), a machine is a checked definition of state and event types, transition policies and state-owned behavior.

**Decision** (designed): The value a machine step returns: `Enter{next}`, `Stay{next}`, `Ignore{}`, `Reject{}` or `Done{output}`. Its type is the prelude `Decision[S, O]`.

**Actor** (designed): A machine run by `actor.Start` as one task with a bounded mailbox, behind a resource handle with `Send`, `Ask`, `State` and `Stop`. In the earlier language (specified), an actor is an owned, addressable instance of behavior with typed messages and replies and an explicit admission and concurrency policy; an effect without a message interface remains a fiber.

**Duration literal** (implemented in prototype): An integer with a unit suffix (`ns`, `us`, `ms`, `s`, `m`, `h`), of type `duration`.

**Codec** (designed): The pair a type's wire form is found by: `ParseX` decodes and `x.Wire()` encodes. A derived codec's rows are the union of its fields' rows. In the earlier language (implemented for structural JSON), a codec is a checked witness relating a wire type and a domain type through separately contracted decoding and encoding operations.

**Go import** (designed): `import go "path"`, which imports a Go package. Every call charges the predeclared `go.Foreign` capability.

## Constructs, contracts and laws

**Construct admission** (owner rule, 2026-10-07; the 2026-10-12 tests and checks are direction, a recorded synthesis and elaboration of owner direction, not owner words): The rule that a language construct, meaning syntax or a checker rule, exists only to make bad code unrepresentable where a library cannot. Since 2026-10-12 a construct must also pass the guardrail test, the portability test and the composability checks.

**Guardrail test** (direction; recorded synthesis of owner direction, 2026-10-12, not owner words): The first construct admission test. A construct passes when it makes an illegal state unrepresentable that neither a library function nor a general type feature can. Behavior that fails it is a primitive function plus library.

**Portability test** (direction; recorded synthesis of owner direction, 2026-10-12, not owner words): The second construct admission test. A construct passes when its meaning is a protocol, a canonical plan or operations plus laws, that any lawful provider can implement, including a userland provider that is not an Effect runtime.

**Composability checks** (direction; recorded elaboration of owner direction, 2026-10-12, not owner words): The five checks every construct and primitive decision passes with evidence. Lossless wrapping: an ordinary function abstracts the construct without losing ownership, rows or observations. Nesting: it nests inside every other construct and library combinator with defined meaning. First-class recipes: effects stay values that library combinators can take. Orthogonality: the primitive set stays small, and derived behavior is library. Composition laws: the construct's algebra is stated as laws.

**Lawful provider** (direction): A userland implementation of a construct's protocol that is admitted because it passes that protocol's laws. It need not be an Effect runtime or share Effect's API. An effect runtime provider and a machine provider are two kinds.

**Notation exception**: A syntax form admitted without an unrepresentability argument because it desugars one-to-one, at parse time, into ordinary calls and adds no checking, typing, evaluation order or runtime behavior.

**Compiler-known construct**: A form whose meaning the compiler must know for checking, inference, plan facts, inspection or special lowering. By owner direction (2026-10-09) it is spelled as syntax or a predeclared language identifier. In the Go-shaped language: `try`, `catch`, `needs`, `provides`, `wire`, `machine`, `go` and `with`. A dot form is never one: `timeout.After` is an ordinary library function over the kernel primitives `go`, `Task.Stop` and `Scheduler`, and the prototype's name-keyed handling of it is a recorded exception ([spec README, Gap 31](docs/spec/README.md#gaps)).

**Predeclared language identifier**: A name the language specification defines in every package without an import or library declaration, following Go's predeclared `len` and `error`, such as `with`, `duration` and the prelude types. Capability services are not predeclared language identifiers, even when the compiler declares them.

**Special-cased library item** (rejected pattern): A bundled, prelude or library declaration that the compiler recognises by identity or name to produce a semantic fact about it (a diagnostic, checked type, row or ownership fact), also called a lang item. An item stops being one only by losing every such branch or by becoming syntax. Backend retention or emission keyed on the item does not by itself make it one.

**Library intrinsic**: A target implementation the compiler substitutes for an ordinary declared library signature. The signature, together with any declared contract written only in the inferred contract vocabulary, is the only source of diagnostics, checked types, rows and ownership, provided that the declared contract is consumed by the same path as an inferred one and no checker, emitter or reachability branch is keyed on the item's identity beyond backend selection (amended for [#130](https://github.com/cevr/ergo/issues/130)). The intrinsic may add backend facts such as retained runtime modules. The implementation of a syntax construct is part of that construct, not a library intrinsic.

**Declared contract** ([#130](https://github.com/cevr/ergo/issues/130)): A library primitive's statement of what its body does with its parameters, written only in the vocabulary that inference produces, for a primitive that has no reference body to infer from. It is consumed exactly as an inferred contract is, so it grants no meaning that inference could not.

**Recipe-parameter contract** ([#130](https://github.com/cevr/ergo/issues/130)): The inferred summary of what a function's body does with each function-typed parameter: calls it, hands it to a task, stores it or replays it. A caller substitutes its actual argument. In the Go-shaped language it carries the non-escaping, concurrent and replayable facts and travels in the interface summary.

**Language protocol**: A nominal row label whose charging or discharge a construct's own checking rule defines, such as `go.Foreign` for calls through `import go`, or `sync.Tx` inside `sync.Atomically`. It is predeclared. An ordinary capability, whose label enters a row only through declared operations or requirements, is not a language protocol.

**Constraint** ([#141](https://github.com/cevr/ergo/issues/141); was "Where clause"): A requirement on a generic type parameter that its type provides a method or belongs to a named interface such as `comparable` or `cmp.Ordered`. The spelling is provisional until [#135](https://github.com/cevr/ergo/issues/135).

**Owner package** ([#141](https://github.com/cevr/ergo/issues/141); was "Owner module"): The package that declares a type. Methods are declared only there, as in Go, so a constraint on a type has one answer: the owner's method. A function type, a row or a resource handle without such a method satisfies no method constraint.

**Method argument** ([#141](https://github.com/cevr/ergo/issues/141)): The method value the compiler passes for a satisfied constraint in the lowered code. It is the same function value a caller could pass by hand, and Go generics carry it in their dictionaries.

**Law** (designed): A named behavioral obligation that types cannot state, written as `law X(…) { … }` in an operation type, with a body that ends in a `bool` expression, and checked like a fuzz target. A refuted law refuses the build. Laws are one general construct with many clients: codecs, providers, schedules, machines and composition algebra ([#125](https://github.com/cevr/ergo/issues/125)).

**Provider law** (designed): A law with a `needs` clause. It is checked against every provider wired for that capability.

**Contract** ([#170](https://github.com/cevr/ergo/issues/170)): An interface used as a constraint together with the laws stated over its methods. A type satisfies the contract when it has the methods and its laws hold.

**Provider protocol** ([#170](https://github.com/cevr/ergo/issues/170)): A capability together with its provider laws. A provider is admitted for the capability by passing them, whatever its implementation.

**Satisfaction** ([#170](https://github.com/cevr/ergo/issues/170)): The relation between one type or provider and one contract or provider protocol, with each law's evidence status: tested, refuted, trusted or unresolved.

**Admission** ([#170](https://github.com/cevr/ergo/issues/170)): The build's decision to accept a satisfaction. A refuted law refuses the build; a trusted law needs a written reason; an unresolved law is allowed with a visible note unless evidence rests on it.

**Evidence kind** ([#170](https://github.com/cevr/ergo/issues/170)): A nominal wrapper type, such as `SortedBy[S, compare]`, whose values carry the proof that a law-backed check has run on the carrier. Only the checking function mints it.

**Law receipt** ([#170](https://github.com/cevr/ergo/issues/170)): The recorded outcome of running a law suite for one revision, seed and target: the status of each law, its case count and any counterexample. The build reads only the canonical receipt.

**Proof tier** (designed in outline; owner direction 2026-11-02, [#162](https://github.com/cevr/ergo/issues/162)): The check-time stage that sends only decidable facts, such as index ranges, length relations, integer bounds and unreachable arms, to an SMT solver within a fixed time budget, reports "proved" only when the solver discharges the obligation and "unknown" on a timeout, and lowers a proved fact as a removed check, while everything outside it stays tested, never proved.

**Zero-cost abstraction obligation** (destination, not current support): A candidate abstraction must make the illegal states and invalid operations covered by its checked contract unrepresentable in checked source, while foreign and trusted behavior remains explicitly qualified, and then erase or directly lower without an abstraction-induced executable size or runtime penalty against a semantic equivalence baseline. Retained allocation, dispatch, module, byte and residual-check costs must be visible in matched receipts. Native output targets parity with or better performance than the optimized idiomatic Go member, and Go lowering pursues every material measured lowering or specialization opportunity while preserving the userland runtime contract. This obligation never removes a required guardrail and does not promise universally zero overhead or a fixed speedup multiplier.

**Semantic equivalence baseline** (acceptance term): A matched Ergo and explicit Go program pair that performs the same validation and observable work while preserving the same rows, ownership, cancellation and completed-cleanup behavior. The Go member is an optimized idiomatic Go control with the same contract. The baseline makes an abstraction's generated and runtime costs attributable rather than inferred from unequal examples. Until 2026-10-26 the baseline also had a TypeScript/Effect member; it went with the JavaScript target.

## Runtime and providers

**Effect runtime provider** (direction): An implementation of the lawful runtime contract that executes Ergo effects on a target. `runtime/ergo` is the native Go provider of the earlier language, and the prototype's `rt` package is the first provider of the Go-shaped language. Pinned Effect as the JavaScript provider went with the JavaScript target on 2026-10-26.

**Lawful runtime contract** (specified research): The host operations and laws an effect runtime provider must satisfy, with each obligation's evidence classified as nominally checked, trusted decision, tested law, refuted or unresolved/unavailable.

**Host-driven program** (direction; owner direction 2026-11-02, [#202](https://github.com/cevr/ergo/issues/202) and [#240](https://github.com/cevr/ergo/issues/240)): A program whose control loop a host owns, such as a game engine, a GUI window or a TUI screen that calls it each frame or event on the host's thread through a declared entry signature, while its capabilities stay separate services provided by a context rather than one platform owning them all.

**Uninterruptible host wait** (designed): A blocking Go call with no cancel hook. It is recorded in the footprint and listed by `ergo inspect` (G8).

**Exit accumulator** (designed): The runtime record that combines a body's outcome with release defects and interruption into one exit cause, so a release never hides a failure.

**Definition ID** (designed): The stable name of one checked definition, derived by the checker from the declaration: `<package>.<Name>`, `<package>.<Type>.<Method>`, or `<enclosing ID>/<construct>#<n>` for a block such as a timeout. It is meaningful only with the source revision it was computed from. Runtime traces, defect chains and `ergo inspect` share this ID space ([traceability](docs/research/traceability.md)).

**Source revision** (designed): The SHA-256 digest of a source file, the revision that qualifies its definition IDs. A trace header carries it once, and a trace query refuses a trace whose revision differs from the file.

**Trace event** (designed): One record of a run. It has an occurrence ID, a kind from the runtime-law vocabulary (#124) or the three additions `call`, `call-exit` and `interrupt`, a task and its parent, a definition ID and a source site, and it may carry an occurrence link. Its serialized form names failures and defects by type and never carries argument or result values.

**Occurrence ID** (designed): The number the runtime gives a trace event when it raises it, unique within one traced task tree and taken from one counter, so an event that happens before another has the smaller ID. A capability call's occurrence ID joins its `call` and `call-exit` events.

**Occurrence link** (designed): The occurrence ID that one trace event records for another, set by the runtime and never inferred by a reader: a `handler` links the failure, deadline or child exit it caught; a `call-exit` its call; an `observe` the child's `body-exit`; a `panic` the capability call it left. A `body-exit` has no single link: each reason of its exit carries its own origin, an occurrence link for that reason alone (a typed failure its failed call, deadline or child exit; a defect its `panic`; an interruption its `cancel-request`). An origin names the occurrence the reason came from, never an occurrence that merely carried an equal value; when the runtime cannot tell which of several equal failures a reason came from, it records none. A trace query states a cause only along these links, says "origin not recorded" for a reason without one, and labels anything else a candidate.

**Point event** (designed): A trace event that emitted code raises at its own source site: a capability `call` and its `call-exit` with the outcome (ok, a failure, a defect or interrupted), an `interrupt` at an interruption point, a wire `construct` or a catch `handler`. The runtime raises the other kinds for a task, keyed by the task's definition ID.

**Trace evidence** (designed): The answer of a trace query (`sketchc trace`, later `ergo trace` and MCP `code.trace`): the `trace-evidence-1` envelope with the fields of the review evidence schema (#156), then the tasks, the capability calls with their spans, the resolved events and the cause trace. The query validates the trace before it answers and refuses a malformed one with the reason.

## Earlier `.ef` language

These terms describe the retired `.ef` language, with its `effect fn`, `run`, `raises` and `service` forms. [#205](https://github.com/cevr/ergo/issues/205) removed its compiler ([retirement record](docs/receipts/retire-ef.md)); its runtime, `runtime/ergo`, stays in the tree until lowering replaces it. The terms stay as design reference. The Go-shaped language replaces it; where a term has a counterpart above, the entry says so.

**Record**: A nominal data type with declared fields. Construction checks every required field and its type. Counterpart: struct.

**Closed enum**: A nominal set of alternatives, each with its own declared payload. Counterpart: union.

**Absence**: An explicitly selected closed alternative describing a missing value, distinct from an uninitialized field or a bare nil/null value.

**Void**: The no-value type, spelled `void`, describing successful completion without an information-bearing result. Its explicit expression is `void`. It is distinct from absence and from a computation that does not complete successfully.

**Option** (specified): A generic closed enum with `None` and `Some(T)` alternatives. `Some` contains an admitted value of `T`, never an unchecked nil host reference.

**Exhaustive match**: An interpretation that covers each declared alternative exactly once and executes only its selected arm. Counterpart: totality.

**Signed-64 wrap** (specified): The portable `i64` arithmetic rule: each admitted `+`, `-`, `*`, `/` and unary `-` result is reduced to its two's-complement signed 64-bit residue, so `MIN / -1` is `MIN`. Division and remainder truncate toward zero.

**Divisor proof** (specified): Checked evidence that an `i64` divisor is not zero: a nonzero integer literal, or a local binding inside an `if` branch whose comparison of that binding with an integer literal excludes zero. `/` and `%` are admitted only with one, so division by zero is not representable in checked source.

**Failure payload**: The declared data carried by a named failure, separate from an ordinary success value.

**Entry failure report**: The deterministic stderr text and exit status a program entry produces when `main` fails. It lists each cause reason in order, as a named failure with its payload, a defect message or an interruption. Payloads are rendered from the checked types of `main`'s failure row, so the report is deterministic for every failure in that row, including built-in, codec and HTTP listener failures. The one exception is an unclassified HTTP listener failure, which keeps the host's text.

**Effect contract**: The success value, named failures, and required services of a deferred Ergo program.

**Failure row**: The unordered set of nominal failures admitted by an effect contract. Counterpart: error row.

**Requirement row**: The unordered set of nominal services needed to execute an effect. Counterpart: capability row.

**Callable value**: A named function carried as a value with explicit parameter, result, failure and requirement contracts. Pure functions and functions constructing deferred effects have distinct callable kinds.

**Row parameter**: A declaration-qualified variable representing a finite failure or requirement row. Ordinary function application obtains its least bound from direct callback argument rows.

**Callback-result relation**: Retained evidence relating a callback invocation's returned handles to its resolved named callee and input ownership. It is separate from ownership of the callable value; an unresolved relation remains potential ownership.

**Postfix construct**: A compiler-known construct written after a receiver with `.`, such as `.catch<E>(h)`, `.provide<S>(p)` or `.timeout(ms)`. The parser claims its word after any receiver. The 2026-10-12 construct bar no longer admits this spelling for constructs; the Go-shaped language replaces these forms with `catch`, `with` and `timeout.After`.

**Pipe** (implemented): The notation `x |> f(args)`, which is the ordinary call `f(x, args)`.

**Receiver method** (admitted, not implemented): A function declared with an explicit receiver in its type's owner module, such as `fn (u: User) display()`. The call `u.display()` is exactly `User.display(u)`.

**Where clause** (earlier spelling): A requirement that each type argument's owner module declares a method with a given name and signature. Counterpart: constraint.

**Owner module**: The module that declares a type and its receiver methods. Counterpart: owner package.

**Constant parameter default** (admitted, not implemented): A literal or named constant declared for a parameter and inserted at the call site when the argument is omitted.

**Visibility** (admitted, not implemented): Whether another module may name a declaration. Declarations are private by default and exported with `pub`; the case of a name carries no meaning. Superseded 2026-10-27 by exported names (owner: "Yeah, let's go with your [recommendations]").

**Construction authority** (admitted, not implemented): The right to construct values of a nominal type. A `pub readonly` type lets other modules read and match its values while only its owner module constructs them. In the Go-shaped language, an unexported field gives the same authority (designed): outside the owner package, the struct has no literal and no implicit zero value (`Quantity{}`, `var q Quantity`, `make([]Quantity, n)`, a zero-filled field, a missed map lookup), so only the owner package can build the value. See [types](docs/spec/types.md#zero-values-and-definite-initialisation).

**JSX pragma** (admitted, not implemented): A user-defined selection of the factory, fragment and result types to which JSX notation desugars.

**Target provider**: An implementation of a service on a particular execution target.

**Provider construction contract**: The configuration and required services used to create a provider value, distinct from the contract of invoking its service operations.

**Provider recipe**: A deferred constructor with explicit configuration and construction requirements. Each execution materializes a provider value.

**Materialized provider value**: An already constructed service implementation that can be explicitly reused without reexecuting its recipe.

**Layer** (specified): A lazy checked acquisition plan selecting implementations and composing their construction dependencies. Its contract records exposed services, construction failures and unsatisfied inputs. Counterpart: wire.

**Layer binding** (specified): A nominal service selection at a declaration site, distinct from its implementation's type and from an acquired provider instance.

**Shared acquisition node** (specified): One identified construction selection reused within a graph build under compatible input identities; concurrent consumers await the same acquisition.

**Graph build** (specified): One scope-owned execution of a layer plan, with its own acquisition table and completed startup or rollback outcome. Separate builds are independent. Counterpart: build.

**Layer replacement** (specified): A checked pre-acquisition substitution of one binding throughout a composed graph, followed by reconstruction of its effective construction edges and contract.

**Fresh layer build** (specified): An explicitly separate provision/build boundary with independent acquisition. A flat graph does not admit a second binding of one nominal service by copying its subtree.

**Ownership provenance**: The relationship between a retained value and the lifetime that owns it. Borrowed, newly owned and unknown relationships carry different evidence.

**Semantic revision**: The identity of the checked snapshot described by inspection or diagnostics, including imported declaration data and behavior contracts when present.

**Managed fiber**: An execution of an Ergo effect with an owner and a completion result. Its cancellation request and completed shutdown are distinct states. Counterpart: task.

**Owning scope**: The lifetime that owns managed fibers and resource releases. Its closure establishes their completed shutdown and cleanup. Counterpart: owner scope.

**Host declaration**: A Go declaration supplying the native shape and identity of an imported value or callable.

**Initialization root**: A declared foreign Go import's package initialization, retained in a native application independently of reachable calls and lowered as a named or blank Go import. Module availability, export-data discovery and metadata lookup are not initialization roots.

**Native Go protocol** (specified): An imported Go interface and its method set, interpreted by Go's assignment rules. Its behavioral obligations remain distinct from type compatibility.

**Host borrow** (specified): Use of a native value under an existing owner's lifetime without acquiring release authority; retention and aliasing constraints remain explicit.

**Host adoption** (specified): Establishing managed release authority for an admitted native resource and its tracked aliases under an owning scope.

**Native descriptor** (specified): An OS-specific handle whose validity follows a native resource lifetime; it is distinct from the resource object and its interfaces.

**Binding contract**: Supplemental behavioral facts attached to a host declaration, including its cancellation, failure, resource, and trust policy.

**Structural derivation** (implemented for the first JSON profile): Generating a codec's structural rules from canonical checked data declarations and an explicit representation policy.

**Codec plan** (implemented): The bounded, shared structural DAG one derivation checks for a domain type under a profile. An application retains it only through an executed direction.

**Codec transformation** (implemented): A checked conversion in a codec's decoding or encoding direction, carrying its own expected failures and required services. The two directions need not be mathematical inverses. A witness states one with `decode:` and `encode:` functions over a structurally derived representation.

**Failure witness** (implemented): A structural codec witness of a failure declared in the module, used only as an endpoint's failure body. It encodes the failure tagged with its name and has no direction functions.

**Endpoint** (implemented): A declaration `derive name = Http.endpoint(...)` binding a method, an exact static path, optional input and output witnesses, a success status, a handler function and the response of every named failure its input decoding, handler and output encoding can raise. It synthesizes an ordinary handler function from `HttpRequest` to `HttpReply`.

**Route table** (implemented): A declaration `derive name = Http.routes(...)` composing endpoints into one handler that dispatches on exact method and path; no two entries serve the same pair.

**Transformed witness** (implemented): A codec witness that states a codec transformation. A parent witness composes transformed witnesses only by listing them in its `with` clause; each applies wherever its domain type occurs in the parent's representation.

**Machine plan** (specified): The finite, source-revisioned provider-independent facts lowered from a checked machine declaration, including nominal state/event/outcome/output identities, callable rows, transition edges, entry/work identity rules and inspection data.

**Machine provider** (specified): An ordinary typed library, function or service implementation that consumes a machine plan and owns its execution, admission, scopes, cleanup and runtime observation without changing the plan's identities or public rows.

**Provider conformance** (specified): Evidence that independently authored machine providers execute the same checked plans on the same target with unchanged declarations/compiler selection while preserving the declared observable contracts and recording their separate runtime obligations.

**Machine-backed actor** (specified): An actor whose behavior follows a checked machine plan, adding transitions and state-entry lifetimes to ordinary actor ownership and messaging.

**Supervisor** (proposed): An owner that observes completed child exits and applies an explicit restart, stop or escalation policy using fresh child factories and an explicit restart budget: a finite restart intensity, or `unbounded` flagged by the `no-unbounded-restart` justifier rule. Counterpart: set.

**Actor address** (proposed): A typed identity used to route to an actor independently of its current running instance; an address alone promises neither persistence nor delivery.

**Actor incarnation** (proposed): One running instance associated with an address, distinguished from its predecessors and from its individual state entries.

**Durable entity** (proposed): An addressable actor whose provider contract preserves declared domain data and acknowledged progress across process failure, with explicit storage, codec and ownership policies.

**Workflow replay** (proposed): Reconstructing execution from versioned recorded steps and outcomes under an explicit replay contract; ordinary effect recipes do not imply replay safety.

**Machine step** (specified): An ordinary pure or effectful function that consumes a state and event and returns an explicit transition decision. Effectful evaluation has a machine-backed actor-owned scope and completes before state commit.

**State entry** (specified): One owned lifetime of a machine-backed actor's current state, identified separately from that state's tag and payload.

**Re-entry** (specified): Closing a state entry and starting a fresh one, including when the state tag stays the same.

**Entry epoch** (specified): An identity distinguishing a state entry from prior entries so obsolete work cannot update a later entry.

**Transition decision** (implemented syntax): The value a machine step produces for one admitted event: `enter`, `stay`, `ignore`, `reject` or `done`. Its type is the predeclared `decision<S, O>`. Counterpart: decision.

**Region** (proposed): A payload field of one state variant, bound by the machine declaration to a separately declared child machine whose state it holds. Several regions on a variant are orthogonal; one region is a nested (hierarchical) child.

**Region entry** (proposed): The state entry of one active region, nested in its parent's entry with its own epoch, so re-entering one region leaves its siblings' work untouched.

**Machine history** (proposed pattern): A remembered child state kept as ordinary data in an inactive variant and restored by a later `enter`; deep history restores the value, shallow history keeps only its top-level variant. Restoring always starts fresh entries.

**Request scope**: A fresh owning scope for one HTTP request, linked to connection and server cancellation. Completed shutdown includes its handler cleanup.

## Tooling and testing

**Lint advice**: Optional guidance about an admitted program, distinct from diagnostics that determine whether its contracts are valid.

**Justifier rule**: A default-preset lint rule that flags a legal but reviewable choice, such as an `unbounded` budget, until a suppression records the reason.

**Suppression status**: The outcome of one reasoned lint suppression in one analysis. It is applied when it removed a finding of its rule, unused when that rule ran and found nothing to remove, or not evaluated, with a reason, when the rule did not run.

**Lint cost receipt**: Raw per-run wall-clock measurements of one lint analysis. Frontend checking, fact extraction, each rule pack's fact serialization and process phases, and the merge are kept separate. A receipt carries no aggregate and supports no performance claim on its own.

**Expression anchor**: A source location identifying the expression described by a diagnostic or local type query.

**Diagnostic code**: The stable identifier of one checker rule, such as `S102` in the prototype or an `EF` code in the main compiler's registry. [docs/spec/diagnostics.md](docs/spec/diagnostics.md) maps each prototype code to its rule and a fixture.

**Inlay hint** (designed): Text the language server shows in the editor without changing the source, such as `closes: out, in` at a block end that releases resources.

**Dependency graph**: A revision-scoped view of program contracts and the relationships between functions, services and providers. Incoming relationships identify dependents.

**Layer construction path** (specified): A source-grounded sequence of acquisition dependencies explaining a selected node, remaining input or startup failure, separate from service-operation call edges.

**Owning test case**: A checked effect whose normal completion includes shutdown of its children and release of its resources.

**Live test mode**: Explicit permission for a test program to use host capabilities or real program time, distinct from fixture substitution.

**Test watchdog**: A harness deadline independent of program time. Forced termination leaves managed cleanup unconfirmed.

**Application receipt** (implemented): A raw measurement of one built application: the runtime modules and declarations its plan retained, its generated files, imports, dependencies, effective build settings, executable or module bytes and symbols, and the external runtime it does not contain. It reports the compiler distribution separately and makes no performance claim.

**Unmatched floor** (size conformance): A control program that does less than the fixture it accompanies, such as printing a result without the entry's cancellation and scope. It is recorded only as a lower bound and is never compared as a member of a semantic equivalence baseline.

**Program time**: The time authority under which managed sleeps and deadlines execute. Test program time can advance independently of the watchdog's wall clock.

**Virtual clock** (designed): `testing.VirtualClock`, a provider of `Clock` and `Scheduler` whose time jumps to the next timer when every task in the test is blocked.

**Quiescence**: The scheduler state in which no managed fiber is running or runnable. A wake in flight is not idle: a fiber that has been signalled, fired or cancelled but has not run again is runnable. At quiescence, every pending timer that managed work can register without outside input is registered. `Scheduler.awaitRegistration()` returns at quiescence, and `Scheduler.advance` selects each deadline only at quiescence. When several fibers wait for quiescence at once, the order in which they are released is unspecified. Interrupting a waiter is an ordinary interruption. Unmanaged goroutines, host promises and foreign blocking calls are outside it.

**Latch handoff**: What `Sync.signal` does to the fibers parked on a latch. It completes the latch, makes each waiter runnable and returns. Wherever execution is serialized into turns (Go under the managed test scheduler), the signaller keeps its turn and each waiter resumes later on a turn of its own, never inside the signaller's turn. Interrupting the signaller after `signal` returns cannot undo the signal or affect a waiter it does not own. A waiter interrupted after it is signalled but before it runs, directly or by its owner's close, ends interrupted, and the latch stays signalled. Live Go has no turns: a waiter runs on its own goroutine, possibly in parallel with the signaller, so an interrupt after `signal` races with it.
