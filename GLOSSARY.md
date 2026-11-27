# Ergo glossary

Ergo is "Effect for Go": a Go-shaped language whose compiler checks Effect's guarantees (typed failures, explicit dependencies, structured concurrency, completed cleanup) and emits Go. Go is the only target (owner, 2026-10-26). The normative rules are in [`docs/spec/`](docs/spec/README.md). Every domain term is defined only here; the spec files state rules and link to these definitions.

The sections are:
- **Language**: the Go-shaped language of `docs/spec/`.
- **Constructs, contracts and laws**: how constructs are admitted, and the vocabulary of laws and evidence.
- **Runtime and providers**: tasks, interruption, exits and lawful providers.
- **Domain modeling**: aggregates, deciders, views, policies, sagas, stories and scenes, and the terms of infrastructure as code.
- **Earlier `.ef` language**: terms of the retired language that the .ef migration removed with its compiler. They stay as design reference.
- **Tooling and testing**.

An entry defines a term and carries no status; a construct's status is in [docs/spec/status.json](docs/spec/status.json).

## Language

**Package**: One unit of checking: a folder, every `.ergo` file directly in it, in name order. The folder names the package (the last element of its import path); a file has no `package` clause. A program's packages are lowered into one Go package (196b Q5: one lowering path for std and user packages; a Go package per Ergo package waits for an edit-rebuild receipt).

**Module**: The packages under one module root, named by the `module NAME` line of its `ergo.mod`. A local package's import path is the module name and its folder's path from the root, as in Go. A program imports std, its own module, and the Ergo modules its `go.mod` requires (MD1; [grammar](docs/spec/grammar.md#source-files), Required modules).

**Trust line**: A `trust go MODULE` line of the root module's `ergo.mod`: the program admits the Go code (`import go`, `extern go`) of that required module, which Ergo does not check. Without it such a use is S108. Only the root's trust lines count (MD3; [grammar](docs/spec/grammar.md#source-files), Trusted Go).

**Module root**: The folder that holds `ergo.mod`. The loader finds the nearest one at or above a program's folder; every diagnostic names its file relative to it.

**Exported name** (owner decision 2026-10-27): A package-level name, field or operation whose first character is an upper-case letter. Only exported names are visible to other packages, as in Go: another package's unexported field or operation is S104, and an operation type with an unexported operation is implemented only by its package's types. See [grammar](docs/spec/grammar.md#visibility).

**Operation type**: A type declared by its operations alone, `type Store { Get(k string) string fails NotFound, Put(k string, v string) }`. A value of any type that has every operation, with compatible rows, converts to it ([types](docs/spec/types.md#interface-assignability)). An operation type with an unexported operation is sealed: only its own package implements it ([grammar](docs/spec/grammar.md#visibility)). An operation type can carry laws. In a `needs` row it is a *capability* (below). Go's nearest form is an interface; the specification says operation type.

**Capability**: An interface with method signatures that names replaceable behaviour, such as `Orders` or `Clock`. Code reaches it only by calling its methods on the capability name, `Orders.Find(id)`, and every such call charges the capability to the caller's capability row. The capability is not a first-class value: it cannot be passed, returned or stored (S104). See [types](docs/spec/types.md#capabilities) and [rows](docs/spec/rows.md#capability-rows).

**Interface value**: A value of an interface type in a type position, such as a parameter `o Orders`. It holds a provider value. Calling its method runs that provider's method with the method's declared rows and charges no capability. A type is assignable to the interface by method-row compatibility ([types](docs/spec/types.md#interface-assignability)).

**Provider**: A value bound to a capability by a `context`, whose type implements the capability's operation type: its `implements` clause names it, directly or through an include (EXC1). A provider may fail less than its capability, never more.

**Value provider**: A named record whose own value is its state, with operation functions on `P` or `*P`. A law run owns a fresh record and lends it to pointer operations; a conversion of `P` to an operation-type value moves it into a provider cell. A rewrite proof compares the entire keyed state after both sides, not a separately written model or only the provider's public reads. See [types, Laws 14](docs/spec/types.md#laws).

**Provider cell**: The place a value provider's value moves to when it converts to its operation type: the operations on `*P` write it, and nothing else sees it. Only an operation type sealed in the provider's package (an unexported operation) takes a cell, and only in that package. A cell is confined to the task that made it: an escaping frame never captures it, and no type argument, package `let` or other operation type holds it. See [types, Interface assignability 6](docs/spec/types.md#interface-assignability).

**Constructor**: An ordinary function named as a `context` entry, such as `Orders = OpenPostgres`. Its requirements are satisfied from the same context, and its failures become startup failures of the `with` that builds it.

**Captured provider**: A provider that holds the capabilities its constructor used. Its methods use them without a `needs` clause of their own. Today only `go.Foreign` is captured: a provider whose operations call Go converts to its operation type where it is built, and that site is charged `go.Foreign` (README Gap 17; [rows](docs/spec/rows.md) Context 2).

**Prelude**: The names in scope in every package without an import: `Option`, `Task`, `Decision`, `Clock`, `Scheduler`, `Random`, `Console`, `Ambient`, `TestAmbient` and the functions `Unreachable` and `TestFolder`.

**Ambient**: The prelude capability that is the authority to choose the host. Only the entry gives it to a `main` that lists it. Its one operation is unexported, so no program type implements it and no context binds it. A host package's `NewHost` has it in its row, and so has code that makes a host provider. Pony's `AmbientAuth` and Austral's `RootCapability` are the prior art. See [stdlib](docs/spec/stdlib.md#host-packages).

**Test's ambient**: The prelude capability `TestAmbient`, the authority a test has to reach the host, confined to a fresh folder per case. Its one provider is the test runner, for a named context whose header says `needs TestAmbient`, built by a `with` in a test or by a law case; the runner removes the folder when the case ends. Its providers, `fs.NewTestHost`, `exec.NewTestHost` and `os.NewTestHost`, refuse a path out of the folder with their `Denied`. It is never the host's `Ambient`, which a test never holds. See [rows](docs/spec/rows.md#tests-ambient).

**Test loopback**: The prelude capability `TestLoopback`, the test runner's separate grant to a named context to open TCP listeners and connections on the literals `127.0.0.1` and `::1` only. It does not grant access to the case's folder or to unrestricted host I/O. See [rows](docs/spec/rows.md#test-loopback).

**Host package**: A reviewed standard-library package, such as `os`, `fs`, `net` or `sql`, that calls Go. Only host packages and `import go` reach the host, so every effect appears in some footprint. Its host provider `Host` has construction authority, and its one constructor `NewHost` needs `Ambient`.

**Error type**: A named struct type that an error row names: in a package, one named by an error row of that package or of the package that declares it. Returning a value of an error type is a failure, never an ordinary result.

**Error row**: The set of error types a function may fail with, written `fails A | B` after the result type. Order and repetition carry no meaning.

**Capability row**: The set of capabilities a caller must supply, written `needs A | B` after a signature, the same union spelling as the error row (196n). Also called the `needs` row or requirement row. Before 2026-10-27 it was spelled `uses`; `uses` is now an ordinary identifier.

**Row**: An error row or a capability row. Rows are checked and then erased from the emitted Go.

**Inferred row** (retired by decision, 2026-11-12; no longer checked since EXPLICIT EX2): A row the checker computed from a body because the row was not written, for an unexported function with a body, per strongly connected component of a package's reference graph. `ergo fix`'s clause writer still computes these least rows to write them. A function literal with no expected type has the rows its body charges (q139); it declares nothing. Under the tiebreak **Explicit local signatures** beat **inference** ([NORTH_STAR](NORTH_STAR.md#tiebreaks); [decision](docs/decisions.md#owner-direction-2026-11-12)), no row is inferred: an unwritten row is empty, as under the export guard.

**Export guard**: The rule that a declaration spells its rows: exported functions, operations, `main`, laws, bodiless declarations and the step of an exported machine type. An unwritten row there is empty, never inferred. By the 2026-11-12 decision it is the signature rule for every named function, operation and method, exported or not, and for every clause: `fails`, `needs`, `takes`, `given`, `gives` and `keeps`. A body that exceeds its signature is a diagnostic that names the clause to add.

**Clause**: A part of a signature, written after the result type, that states a contract the checker checks the body against: an error row (`fails`), a capability row (`needs`), an ownership clause (`takes`), or a fact clause (`given`, `gives`, `keeps`). A clause is always written; an unwritten clause is the empty clause, and the checker never computes one from a body or a call graph. Code the compiler generates gets its clauses from its generator, and `ergo inspect` shows them.

**Requirement binding**: The bare name of a capability a written `needs` row lists, entered in the body's frame beside the parameters. A local of that name in the same frame is an error.

**Implicit type parameter**: A name of one upper-case letter, optionally followed by digits, that names no declaration in a signature. Its kind follows its position: a type, an error row after `fails`, or a capability row after `needs`.

**Fallible expression**: An expression that can fail with a non-empty error row and whose failure is not yet handled. It must be tried, caught or returned.

**Escape**: The passage of a failure from an expression to its failure target, through `try`, a fallible `return`, a returned error value or the rest of a partial catch.

**Failure target**: Where an escaping failure goes: the innermost enclosing function or function literal. A trailing block is a function literal, so the block of `timeout.After` is one. A `with` block is not a failure target.

**Try**: The keyword form `try e`, which yields `e`'s success value and escapes `e`'s failures.

**Call site**: The prelude record `CallSite { File, Line, Column, Pkg, Id }`, a call's position in Ergo source and its identity as a `(Pkg, Id)` pair. A call may leave out its callee's final `CallSite` parameter, and the compiler passes the call's own position; a call that writes it forwards the value. It gives positional identity to `ui` hooks and feeds traces. See [types](docs/spec/types.md#call-sites).

**Catch**: The keyword form `e catch { case T: … }`, which handles named error types of a fallible expression. It covers typed failures only; interruption and defects pass through it.

**Total catch**: A `catch` without `try`. It must handle every error type of the caught row.

**Partial catch**: `try e catch { … }`. It handles the named types, and `try` escapes the rest.

**Discharge**: Removing a capability from the row a block charges to its enclosing function, because an enclosing `with` binds it.

**Context**: What `with` installs for a block: capability bindings and context values. A `context { Cap: provider, … }` literal, inline or named, binds capabilities to providers or constructors; it is Ergo's form of an Effect layer. The construct was spelled `wire` until 196w ([2026-11-02 preview](docs/previews/2026-11-02-wire-to-context.md)). *Prior art:* Effect's `Context` (the service map a Layer builds), Go's `context.Context`, React Context, Scala 3 and Kotlin context parameters.

**Context exposure**: The `provides A | B` clause of a named context header: the capabilities a `with` of that context discharges. Entries the context builds and does not list stay private to its build. A header may also state the context's requirements, `let App = context provides Account | Invoice needs Config { … }`, which join the `with` site's capability row.

**Inline context**: A `context { … }` literal given directly to `with`. Its provider expressions are evaluated in, and charged to, the enclosing function.

**Named context**: A top-level `let Name = context { … }`. It is built by its own function at each `with` that names it.

**Closed context**: A named context whose providers need no capability from the caller and let no failure escape. The checker admits only closed named contexts.

**Build**: One execution of a context at one `with`: constructors run once per shared node, and the build owns their releases until the `with` block ends. Each `with` is a new build.

**With**: The compiler-known form `with W { block }`. It installs a context for the block: it discharges the capabilities `W` binds and yields the block's last expression. It also takes context values.

**Built context**: A value of type `Context[R]`: the built providers of the capability row `R`. A context converts to one where a `Context[R]` is expected, and `with v { … }` installs it, discharging `R` and building nothing. A built context binds capabilities; a *context value* (below) is a value held in the context. *Prior art:* Effect's `Context<R>`, ZIO's `ZEnvironment[R]`.

**Script**: `testing.Script[S]`, a test provider of the capability `S` that the checker derives from `S`'s operations: one field per operation, a `testing.Op` whose `Answer(f)` sets how the operation answers and whose `Calls()` are the arguments of each call so far. Not a *fake* written by hand, which states the operations again. *Prior art:* Elixir's Mox, Go's `mockgen`. See [stdlib](docs/spec/stdlib.md#testing).

**Context value**: A value in the context: an immutable value, such as a span or a log annotation, that `with` scopes to a block and that every task started inside inherits.

**Footprint**: Every capability a function's body exercises, including the ones its own `with` provides, plus `go`. The compiler infers it; it is never written.

**Pure function**: A function whose footprint is empty. An empty capability row does not make a function pure, and a call through an unknown function value counts as impure.

**Union**: A closed set of record types, written `type T = A | B`. It has no zero value.

**Never**: The prelude type with no value, a union of no variants (owner, 2026-11-13). A machine with no output is a `Decision[S, Never]`; a match leaves out the variants that hold a `Never` and needs no arm on one. It is no bottom type: `n.Absurd[T]()` converts it where a `T` is wanted. Not `Unreachable`, which is a defect raised on a path that should not run. *Prior art:* Elm's `Never`, Rust's `Infallible`. See [types](docs/spec/types.md#never).

**Variant**: One struct type listed in a union. A variant value is assignable to every union that lists it.

**Included record**: A record named by the member `..R` of another record declaration or variant, `type Visit { ..Owned, S Session }`. Its fields become the including record's own fields at that position, and each keeps its declaration, so a spread copies it between two records that include `R`. One name has one declaration: a duplicate, also through a diamond, is S108. See [types](docs/spec/types.md#structs) Structs 7.

**Spread**: A record literal `T{..s, F: v}` whose base `s` has another record type. Each field of `T` it does not name is copied from the field of `s` with the same declaration (one record type's instances, or one included record) and the same type; any other unnamed field is S108. With a base of exactly `T`, the same form is a record update. See [types](docs/spec/types.md#structs) Structs 3.

**Defaultable type**: A type with a valid zero value: a basic type, a slice, a Map, a record with no fields, or a tuple of these. A record with fields is not defaultable: it is built only by a literal that names every field or by a record update (owner q44, 2026-11-09; [types](docs/spec/types.md#zero-values-and-definite-initialisation) Zero values 2). Unions, capabilities, function types, tasks, resources and borrows are not defaultable.

**Definite initialisation**: The rule that a variable of a non-defaultable type has a value before it is used.

**Inserted operation**: An operation the compiler adds where the source names none, such as a release at a scope end, the adapter of a conversion to an operation type, the `Wire` call of `encode`, or the instance of a generic body. It carries every rule of its written form. See [inserted](docs/spec/inserted.md).

**Written form**: The Ergo text that stands for an inserted operation, such as `x.Close()` for a release or `encode(Wire(v), w)` for `encode(v, w)`. The checker must give a program and the same program with its inserted operations written out the same verdict and rows ([inserted](docs/spec/inserted.md) 1 and 2).

**Type switch**: `match u { case A as v: … }` over a union value, narrowing `v` to the case's variant. With several subjects, `match (a, b)`, a case is a tuple of variants, and `_` matches any variant.

**Totality**: The property of a type switch, a `catch` or a machine step that covers every alternative. A total form needs no default for its declared alternatives. The checker decides it for a type switch by a usefulness check over its case matrix, never by enumerating the product of the subjects.

**Irrefutable pattern**: A pattern that matches every value of its type, such as a name, a tuple of names or a record pattern of the value's own type. `let` and `for` take only irrefutable patterns; a partial one belongs in a `match`.

**Row value**: The value `case err:` binds in a `catch` arm: the failures the earlier arms leave, typed as the anonymous union of their error types. It is also the type an error row parameter names in a type position, and a row written as a type argument (q47). It can be returned (a failure of those types), matched, caught, compared when every member is equatable, held in a field, or assigned to a declared union that lists each of them; it has no other operation ([rows.md](docs/spec/rows.md#error-rows) Error rows 8).

**Block**: A braced statement list. As the body of `with`, `timeout.After` or a trailing block, it yields its last expression, and `return`, `break` and `continue` are errors inside it.

**Trailing block**: A block written after a call's parentheses. It is notation for a function-literal final argument whose body contains no `return`, `break` or `continue`; an early return needs an explicit `func` literal. It cannot escape the call unless the parameter type says so. The block of `with` is part of that construct instead.

**Non-escaping parameter**: A function-typed parameter written without `escaping`. The callee runs it only on the calling task, before the call returns, one run at a time, and no one keeps it ([types.md](docs/spec/types.md#function-values) Function values 2). Any other use needs `escaping`.

**Concurrent parameter**: A function parameter that the callee runs on another task while another run or the caller may run. It is written `escaping` (ESC1, 2026-11-11); no separate clause marks it, and an argument to it may not assign captured locals. The fact is always the written `escaping`; the compiler does not infer it (decision, 2026-11-12).

**Replayable parameter**: A function parameter that may run more than once, such as a transaction body. It may not assign captured locals, use channels or consume a resource, and its footprint holds only `sync.Tx`.

**Borrow**: A `mut T` (also spelled `*T`) parameter or receiver: the write-in-place mode, beside the read default and `own` ([grammar](docs/spec/grammar.md#declarations) Declarations 18; a Go binding keeps `*T`, Go's pointer). It is never nil, stored, returned, sent or captured by `go`, and two mutable borrows of one value must not overlap. A value converted from it, to an operation type or a Go interface, keeps its lifetime ([types](docs/spec/types.md#borrows) Borrows 6), and it instantiates no type parameter: a generic function that writes through its argument declares `x mut T`, and a constraint's operation that writes is declared `mut` (Borrows 7; [types](docs/spec/types.md#generics) Generics 2). A **mapped text**, `fs.Text`, is a borrow of its `fs.Mapped` with no `*` written: it is never stored, returned or converted, and every use of it is a use of its Mapped, so none follows `Close` (Borrows 8).

**Resource**: A value whose type implements the prelude's `Closer` (its clause names it, or an operation type that includes it), an operation type that includes `Closer` or has an operation that takes a resource, or a record, union, tuple, slice, `Map` or `Option` that holds one (resources R1, R34). A task is owned by its frame; an actor handle is a shared handle, not a resource. Its owner scope releases it. See [resources](docs/spec/resources.md).

**Own type parameter**: A type parameter its declaration marks `own`, `[T own]`: it may bind a resource, and the body is held to a resource's rules for its values. An unmarked type parameter binds plain data only. Replaces the copy site of resource ownership L8 A3 (MODES-5). See [types](docs/spec/types.md) Generics 9.

**Crossing type**: A type that holds no resource, so the ownership rules mean nothing for its values: they copy freely. Plain data and shared handles cross; a generic type crosses with its arguments (`Stack[int]` crosses, `Stack[File]` does not). The term is OxCaml's mode crossing. See [resources](docs/spec/resources.md) R1.

**Declared root** (EXH1): A value whose type implements `Closer`, or a `*T` borrow of one, seen as the owner of the handles it holds (values of an operation type that is not a resource, such as `sync.Ref` or `actor.Actor`, or values holding one). A handle read from it is lent: it leaves only with the whole root, and a write back to its own place grows the root in place. See [resources](docs/spec/resources.md) R17.

**Takes clause**: `takes p` after a signature's rows: the call hands the parameter `p` off, and the caller cannot use it after the call. Written on every function, exported or not (q137; `ergo fix` writes a missing one), and part of a function type. A bare `takes` on an operation names the value the operation is called on. See [grammar](docs/spec/grammar.md) Declarations 13.

**Owner scope**: The block that owns a resource: the block that acquired it, or the scope it moved to. A loop body is its own owner scope for each iteration.

**Release**: Closing a resource at the end of its owner scope, in reverse acquisition order, on every exit. A release is infallible and runs masked.

**Move**: A transfer of a resource's ownership: by `return`, into a provider field, into a `go` task outside a structured lend, by building a struct or container that holds it, or by assignment. A channel send of a resource is an error in v0. The source cannot be used after a move.

**Lend**: Passing a resource as an argument, capturing it in a non-escaping closure, or indexing it out of a container. The owner keeps ownership; the borrower may not store, return, send or capture it, except by a structured lend to a task it releases before it returns, so a lend needs no lifetime annotation.

**Structured lend**: A lend of a resource to a task, or to an escaping closure an owner keeps, that is allowed because the task or owner is certain to be released before the borrow of the resource ends, on every exit. The borrow ends at the release point of the resource's root owner: a value reached by indexing or through a field keeps the root owner of its container or aggregate. The task qualifies when it is bound after the root owner in the same scope, owned by a scope nested inside the root owner's scope, stored in a later field of the same struct, or stored in a container whose owner qualifies. Only for an incoming parameter, whose owner outlives the call, does the borrow end at the function's return, so any task owned by a scope local to that function qualifies; moving or returning that task out of the function is an error. See [resources](docs/spec/resources.md#structured-lend).

**Consume**: Ending a resource early by calling `Close()`, a task's `Wait()`, or a function whose `takes` clause names it. Nothing runs for it at scope end, and a later use is an error. A task's `Stop()` lends; it does not consume ([resources](docs/spec/resources.md#early-release) R19).

**Consume (a value)**: Take a slice, `Map` or record that holds one at a position that may keep or write its storage: the value of a `let mut`, an owned parameter, an owned form's first operand, the source of an `Append`. A consumed value or path is taken in place only when no other use of it can see the write; otherwise the lowering copies it first. A record is consumed by parts when its body consumes its fields on paths that do not overlap ([types](docs/spec/types.md#bindings), *own-parts*). Not the resource sense above: nothing ends early, and a later read is legal, so it is copied.

**Owned parameter**: A parameter whose function's body may write or keep its storage, because every use consumes it or keeps nothing ([types](docs/spec/types.md#bindings) 2). Its caller passes it unshared: a copy only when the argument is shared, none for a dead local, a fresh value or a rebinding `p = f(p, …)`, which hands the old value over.

**Generated Close**: The `Close` the compiler gives a struct with resource fields. It closes those fields in reverse declaration order.

**Task**: A unit of concurrent work started by `task.Go`, of type `Task[T, E]`. It is a resource (it declares `Close()`): one owner, moved by a hand-off, taken by `Wait`, which returns its result; `Stop` interrupts it and waits for it; its owner's end releases it when nothing took it.

**Task tree**: The parent and child relation of tasks. Stopping a task stops every task below it.

**Set**: `tasks.Set(policy)`, a resource that owns an unbounded number of tasks and applies a fail-fast, log or restart policy (G4). Not std `set.Set`, a key set.

**Key set**: A value of std `set.Set[K comparable]`: keys with no order, each once, over a Map. It is a value, as a Map is: `set.Add` and `set.Remove` return a new set, and a copy never sees a later change. Effect's `HashSet`; Go's `map[K]struct{}`. See [stdlib](docs/spec/stdlib.md#slices-maps-and-cmp) 13.

**Decimal**: A value of std `decimal.Decimal`: an exact decimal number, a sign, a 128-bit coefficient and a scale (the digits after the point), so 12.50 is 1250 at scale 2. Arithmetic is exact or fails with `Overflow`; only `Round` and `Div` drop digits, by a named **rounding mode** (`HalfUp`, `HalfEven`, `Down`, `Up`, `Floor`, `Ceil`). Effect's `BigDecimal`. See [stdlib](docs/spec/stdlib.md#decimal).

**Secure source**: The operation type std `rand.Secure`, whose `Bytes(n)` is random bytes no one can predict, for keys, tokens and ids. `rand.NewSecureHost()` reads Go's `crypto/rand` and needs `Ambient`; `rand.NewSeededSecure(seed)` replays a stream for tests. It is apart from `Random`, whose draws are fast and replayable and so predictable. `uuid.V4` and `uuid.V7` draw from it. See [stdlib](docs/spec/stdlib.md#keyed-digests).

**Interruption**: A request that a task stop. It is cooperative and is not a typed error: no error row names it.

**Interruption point**: A place where a task checks for interruption: a capability call, a channel operation, `Wait`, `Sleep` or a loop back-edge in a function with a footprint (I1).

**Mask**: A region that interruption cannot enter: releases, `Close`, `scope.OnExit` bodies, context cleanup and the step between acquisition and its registration (I2).

**Exit cause**: How a task ended when it did not succeed: one or more typed failures, defects and interruptions, all kept.

**Defect**: A failure outside every row, such as a Go panic in Ergo code or a crash in `Close`. It re-raises in the parent at the join. Source raises one with the prelude function `Unreachable(reason)`, whose call has the bottom type: it takes the type its position expects (owner q29; [types](docs/spec/types.md#unreachable)).

**Span**: A named trace region, scoped by `with(trace.Span(…))`, keyed to a checked definition identity.

**Machine**: A declaration `type X machine[S, E] { initial v; step f }` of a state machine over the unions `S` and `E`. Its meaning is a plan any lawful provider can run. In the earlier language (specified), a machine is a checked definition of state and event types, transition policies and state-owned behavior.

**Decision**: The value a machine step returns: `Enter{next}`, `Stay{next}`, `Emit{next, output}`, `Ignore{}`, `Reject{}`, `Done{output}` or `Tell{next, output}`. Its type is the prelude `Decision[S, O]`. Only the first state, an `Enter` and an `Emit` open an entry; `Stay` and `Tell` keep the entry's timer and work, and `Emit` and `Tell` give an output (runtime.md, Machines and actors 2).

**State's exit action**: The action a machine's `exit: x` entry gives a state, `x(s) Option[func() needs R]`. The runner runs it once when an entry that holds `s` ends, with the state the entry holds then, on every path that ends it, `Stop` included. Not `actor.Exit`, the value of how an actor ended. See [runtime](docs/spec/runtime.md#machines-and-actors) Machines and actors 12a.

**Actor**: A machine run by `actor.Start` as one task with a bounded mailbox, behind a resource handle with `Send`, `Ask`, `State` and `Stop`. In the earlier language (specified), an actor is an owned, addressable instance of behavior with typed messages and replies and an explicit admission and concurrency policy; an effect without a message interface remains a fiber.

**Duration literal**: An integer with a unit suffix (`ns`, `us`, `ms`, `s`, `m`, `h`), of type `duration`.

**Codec**: The pair a type's wire form is found by: `ParseX` decodes and `x.Wire()` encodes. A derived codec's rows are the union of its fields' rows. In the earlier language, a codec is a checked witness relating a wire type and a domain type through separately contracted decoding and encoding operations.

**Go import**: `import go path`, which imports a Go package through its binding (an Ergo file of `extern go` declarations that `ergo bind go` generates; Go's standard library is embedded). The name it binds is the Go package name (`import go math/rand/v2` binds `rand`). A call into a package of the reviewed pure list charges nothing; any other Go call charges the predeclared `go.Foreign` capability, which only the entry provides. A Go pointer that is stored or returned is a `go.Ref[T]`; Ergo's `*T` stays a borrow.

**Foreign fact**: A statement about what a Go function does with an operand that may change the code at its calls, such as `reads p`: the function only reads `p`. A fact is checked when the pinned Go toolchain derives it (escape analysis, read by `ergo bind go` into the binding's `// ergo:reads` line) and vouched when a reviewed row asserts it. A law can guard a fact; it never licenses one. See [interop](docs/spec/interop.md#foreign-facts).

**Vouched**: The level of a foreign fact that a reviewed row asserts because Go's analysis cannot see it (an assembly body, a function variable, C). A vouched fact whose guard has a current, passing line in the ledger is validated, and only then changes code; without one it is assumed. Contrast a checked fact, which the toolchain derives.

## Constructs, contracts and laws

**Dense id**: A std `dense.ID[K]`, the id of a row of a store of `K`: one `uint32`, four bytes and no pointer, made only by the store's `Put` or `Next`, so it is never forged and never of another row type. `dense.Opt[K]` is a dense id or none in the same four bytes. See [stdlib](docs/spec/stdlib.md#dense-stores).

**Dense store**: A std `dense` store of rows addressed by dense ids. Rows are never removed from a `Table[T]` of whole rows, a `Count` with `Col`s (one column per field), a `Side` of data few rows have, a `Pool` of short lists; and the interning stores `Interner` and `Strings` (one id per distinct value), the `Counted` set (ids freed when no holder holds them) and the `Sparse` set of ids. A store of rows with no pointer field is pointer-free, so the collector does not scan it. Distinct from an arena, whose refs carry a generation because its values are removed. See [stdlib](docs/spec/stdlib.md#dense-stores).

**Arena**: A std `arena.Of[T]`, a growable store of values of one type in one slice, addressed by `arena.Ref[T]` handles that only the store makes. A Ref carries its slot's generation, so a Ref kept after `Free` is stale and every operation on it fails with `arena.Stale`, also after the slot is reused. See [stdlib](docs/spec/stdlib.md#arenas).

**Text span**: A std `text.Span`, two byte offsets that name a piece of a text and hold no pointer; the text is held once, by the value that owns the spans, and a read takes both. Not a trace **Span**. See [stdlib](docs/spec/stdlib.md#text-spans).

**Atomic handle**: A shared `atomic.Int64` counter or `atomic.Bool` flag. Copies name the same cell. Each operation is one sequentially consistent Go atomic operation; a load followed by a store is not one operation. See [stdlib](docs/spec/stdlib.md#atomic-counters-and-flags).

**Embedded asset**: A file selected at build time by `embedded[T](pattern)` in an immutable package declaration. Its bytes are build inputs and Go's `//go:embed` stores them in the executable. It is a string, a byte slice, or a read-only `embed.FS` provider of `fs.Files`. See [stdlib](docs/spec/stdlib.md#build-time-assets).

**Construct admission** (owner rule, 2026-10-07; the 2026-10-12 tests and checks are direction, a recorded synthesis and elaboration of owner direction, not owner words): The rule that a language construct, meaning syntax or a checker rule, exists only to make bad code unrepresentable where a library cannot. Since 2026-10-12 a construct must also pass the guardrail test, the portability test and the composability checks.

**Guardrail test** (direction; recorded synthesis of owner direction, 2026-10-12, not owner words): The first construct admission test. A construct passes when it makes an illegal state unrepresentable that neither a library function nor a general type feature can. Behavior that fails it is a primitive function plus library.

**Portability test** (direction; recorded synthesis of owner direction, 2026-10-12, not owner words): The second construct admission test. A construct passes when its meaning is a protocol, a canonical plan or operations plus laws, that any lawful provider can implement, including a userland provider that is not an Effect runtime.

**Composability checks** (direction; recorded elaboration of owner direction, 2026-10-12, not owner words): The five checks every construct and primitive decision passes with evidence. Lossless wrapping: an ordinary function abstracts the construct without losing ownership, rows or observations. Nesting: it nests inside every other construct and library combinator with defined meaning. First-class recipes: effects stay values that library combinators can take. Orthogonality: the primitive set stays small, and derived behavior is library. Composition laws: the construct's algebra is stated as laws.

**Lawful provider** (direction): A userland implementation of a construct's protocol that is admitted because it passes that protocol's laws. It need not be an Effect runtime or share Effect's API. An effect runtime provider and a machine provider are two kinds.

**Notation exception**: A syntax form admitted without an unrepresentability argument because it desugars one-to-one, at parse time, into ordinary calls and adds no checking, typing, evaluation order or runtime behavior.

**Compiler-known construct**: A form whose meaning the compiler must know for checking, inference, plan facts, inspection or special lowering. By owner direction (2026-10-09) it is spelled as syntax or a predeclared language identifier. In the Go-shaped language: `try`, `catch`, `needs`, `provides`, `context`, `machine`, `go` and `with`. A dot form is never one: `timeout.After` is an ordinary library function over the kernel primitives `go`, `Task.Stop` and `Scheduler`, and the prototype's name-keyed handling of it is a recorded exception ([spec README, Gap 31](docs/spec/README.md#gaps)).

**Predeclared language identifier**: A name the language specification defines in every package without an import or library declaration, following Go's predeclared `len` and `error`, such as `with`, `duration` and the prelude types. Capability services are not predeclared language identifiers, even when the compiler declares them.

**Special-cased library item** (rejected pattern): A bundled, prelude or library declaration that the compiler recognises by identity or name to produce a semantic fact about it (a diagnostic, checked type, row or ownership fact), also called a lang item. An item stops being one only by losing every such branch or by becoming syntax. Backend emission, optimization or tooling keyed on the item makes it one too, unless the item is a predeclared name (owner, 2026-11-12).

**Seam**: The files of the compiler that may know that the standard library exists, to find, ship, cache and name it; they change no meaning. The list is in [stdlib.md, Std is ordinary](docs/spec/stdlib.md#std-is-ordinary); every other read of std membership is a special-cased library item.

**Predeclared name**: A name the language gives a meaning to with no import: a prelude declaration the compiler knows, or an operation name of a language protocol (`Close`, `Wire`). The readers of each are listed in [stdlib.md, Std is ordinary](docs/spec/stdlib.md#std-is-ordinary).

**Body contract**: The written facts and the calling convention of a Go body that an `extern go` declaration binds: whether it is pure, moves or keeps its arguments, waits, releases, or reads the context, and which hidden arguments it takes. Any module's extern reads the same contract. Each fact and its home are listed in [stdlib.md, The body contract](docs/spec/stdlib.md#the-body-contract).

**Library intrinsic**: A target implementation the compiler substitutes for an ordinary declared library signature. The signature, together with any declared contract written only in the inferred contract vocabulary, is the only source of diagnostics, checked types, rows and ownership, provided that the declared contract is consumed by the same path as an inferred one and no checker, emitter or reachability branch is keyed on the item's identity beyond backend selection (amended for declared contracts). The intrinsic may add backend facts such as retained runtime modules. The implementation of a syntax construct is part of that construct, not a library intrinsic.

**Declared contract**: A library primitive's statement of what its body does with its parameters, written only in the vocabulary that inference produces, for a primitive that has no reference body to infer from. It is consumed exactly as an inferred contract is, so it grants no meaning that inference could not.

**Recipe-parameter contract**: The inferred summary of what a function's body does with each function-typed parameter: calls it, hands it to a task, stores it or replays it. A caller substitutes its actual argument. In the Go-shaped language it carries the non-escaping, concurrent and replayable facts and travels in the interface summary. Superseded as an inferred summary (decision, 2026-11-12, [explicit local signatures](docs/decisions.md#owner-direction-2026-11-12)): when it is built, each fact is a written clause or a written parameter prefix, never inferred from the body.

**Language protocol**: A nominal row label whose charging or discharge a construct's own checking rule defines, such as `go.Foreign` for calls through `import go`, or `sync.Tx` inside `sync.Atomically`. It is predeclared. An ordinary capability, whose label enters a row only through declared operations or requirements, is not a language protocol.

**Constraint** (CONSTRAINTS; was "Where clause"): A requirement on a generic type parameter, written in bound position: `[T C]`. It is an operation type, whose operations the type argument must have by a declared `implements` clause, and whose **type set** it must be in, or the predeclared `comparable`. One mechanism serves std and users alike: the prelude's `ordered` and `number` are ordinary type-set declarations. A bound may name its own parameter, or its own type: `type Ranked[T Ranked[T]]`.

**Type set** (CONSTRAINTS C1): The member of an operation type that lists types by terms joined by `|`: `~int | ~int64`. A `~T` term admits every type defined over `T`; a bare `T` admits `T` alone; the term `comparable` admits the comparable types. An operation type with a type set is a constraint only, never the type of a value. A body may apply to a `T` under it the operators that every type of the set has (Go's rule).

**Order** (CONSTRAINTS C3): std's `cmp.Order[T]`, an operation type with `Compare(a T, b T) int` and the laws `Reflexive`, `Antisymmetric` and `Transitive`. An order is a provider value that a call names (`slices.SortBy(xs, ByPriority)`), so a type may have many orders and a program may order a type it does not own (`cmp.Natural[int]`). Not a type's own method: nothing is looked up.

**Owner package** (was "Owner module"): The package that declares a type. Methods are declared only there, as in Go, so a constraint on a type has one answer: the owner's method. A function type, a row or a resource handle without such a method satisfies no method constraint.

**Method argument**: The operations the compiler passes for a satisfied constraint in the lowered code: a witness, a struct with one function per operation, the same functions a caller could pass by hand, as Go generics carry them in their dictionaries. Where the instance is known, a copy of the callee for it calls the operations directly and takes no witness (types.md, Generics 5).

**Law**: A named behavioral obligation that types cannot state, written as `law X(…) { … }`, with a body that ends in a `bool` expression, and checked like a fuzz target. Its placement sets what it is quantified over: at top level, beside `test`, its parameters; in an operation type, its parameters and every provider of the type; in a machine, the step graph. A type with laws and no operations is S108 (law-top). A refuted law refuses the build. Laws are one general construct with many clients: codecs, providers, schedules, machines and composition algebra.

**Hashing provider**: A provider of the prelude's operation type `Hashing[K]` (`Hash` and `Equal`, with four laws), named as a Map's third type argument: `Map[K, V, H]` finds its keys through `H`. `Structural[K]` is the provider of `==` and `hash`, and `Map[K, V]` is `Map[K, V, Structural[K]]`. See [types](docs/spec/types.md#maps), Maps.

**Normal-form shape**: A Hashing provider whose `Equal(a, b)` is `n(a) == n(b)` and whose `Hash(k)` is `hash(n(k))` for one expression `n` of the key alone, of the key's type. The checker derives its `NormalForm` true and `NormalKey` `n(k)`, so its Map holds Go's map keyed by `n(k)`, each entry keeping the key's first spelling; a provider without it holds the prelude's table. See [types](docs/spec/types.md#maps), Maps 6.

**Admission**: A law parameter's `where` that names an earlier parameter, `b K where Equal(a, b)`: a law of its own over the parameters up to it. A case runs only when every admission holds, and a parameter that names an earlier one of an identical type is found among that value's near variants. See [types](docs/spec/types.md#laws), Laws 15.

**Invariant law**: A machine law `law L always p` or `law L sometimes p`, with `p` a pure function from the machine's state to `bool`. `always` holds when every reachable state satisfies `p`, the start included; `sometimes` holds when one does, shown by a shortest witness run. See [types](docs/spec/types.md#laws) Laws 13.

**Provider law**: A law with a `needs` clause. It is checked against every provider bound to that capability.

**Derived type**: A type declared `type T derive E`. The checker evaluates the type plan `E` at check time, and the plan's value gives `T`, the types `T` needs, and the parse function, failure and laws that `T` owns, as ordinary declarations of `T`'s package. See [patterns](docs/spec/patterns.md).

**Type plan**: A value of the predeclared, closed type `TypePlan`, built only by the predeclared constructors (`Of`, `Refine`, `Record`, `Union`, `Optional`, `List`, `Pattern`, with `Field`) and by plan functions, and read only by a derive. Each node fixes the shape it derives and the input it parses. See [patterns](docs/spec/patterns.md#type-plans).

**Plan function**: An ordinary rowless function whose result is a `TypePlan` or `FieldPlan`. It runs only at check time, within a budget, with no Go call or I/O, and a user package may export one for other packages to derive from. See [patterns](docs/spec/patterns.md#plan-functions).

**Pattern**: The type plan `Pattern(text)`: a byte regular expression with named captures, matched against a whole `string` by the derived `ParseT`, or leftmost by `FindT`. See [patterns](docs/spec/patterns.md#pattern-grammar).

**Capture shape**: The type a named capture gives its field, from how often it takes part in a match of its scope: once gives the value, at most once gives `Option`, more than once gives a slice of every occurrence. A capture with captures inside it gives a record, and an alternation of named captures gives a union. See [patterns](docs/spec/patterns.md#capture-shapes).

**Construction check** (patterns): The compiler's check that a deterministic matcher follows the canonical tagged NFA of a pattern on every reachable state, every byte and the end of input, with the same ordered capture events. Only this check selects a faster matcher; a law never does. See [patterns](docs/spec/patterns.md#lowering).

**Contract**: An operation type used as a constraint together with the laws stated in it. A type satisfies the contract when it declares that it implements the operation type and the laws hold for it: `ergo test` runs each law for each implementing type (types.md, Laws 10). `cmp.Order` is one.

**Provider protocol**: A capability together with its provider laws. A provider is admitted for the capability by passing them, whatever its implementation.

**Satisfaction**: The relation between one type or provider and one contract or provider protocol, with each law's evidence status: tested, refuted, trusted or unresolved.

**Admission**: The build's decision to accept a satisfaction. A refuted law refuses the build; a trusted law needs a written reason; an unresolved law is allowed with a visible note unless evidence rests on it.

**Evidence kind**: A nominal wrapper type, such as `SortedBy[S, compare]`, whose values carry the proof that a law-backed check has run on the carrier. Only the checking function mints it.

**Law subject** (LAWLOC; owner, 2026-11-15): A declaration of a law's or test's own package that the law or test is about: one its parameters or its body name, or one a helper it calls names (a function, a type and its operations, variants and fields, a package `let`, a context). A helper, an unexported function, type, `let` or context that only laws, tests and other helpers use, is not a subject. A top-level law or test stands in a file that declares one of its subjects (S115); a member or machine law's subject is the type that declares it. `ergo fix` moves a misplaced law or test beside its main subject, the one its name names ([types](docs/spec/types.md#laws), Laws 15).

**Law ledger** (owner q116, q122): The checked-in file `ergo.laws` beside `ergo.mod`, module-wide as `go.sum` is, one line per rewrite law, provider and site instance: the verdict of its check run (proved, tested or skipped) and a digest of every input of the verdict (the law, the provider, the functions, types and package lets they reach, whether the provider's construction is closed, the instance; by tokens, never by position or comment). `ergo test` of any binary adds or replaces its entries and removes only those the module no longer declares, so the order of the binaries' tests does not matter; `ergo build`, `run` and `inspect` only read it, so the run build is a function of checked-in files. A rewrite applies only on a current, proved entry; a stale or missing one keeps the reference lowering with one printed line, and `--locked` refuses ([runtime](docs/spec/runtime.md#tests), Tests 10). Std ships its own, embedded. Not a law receipt, which records one run for a revision and changes no code.
**Foreign key** (LAWFFI r2 §3.5; LFL): The pin of a law ledger entry to the Go code its law reaches through `extern go`: the pinned toolchain for Go's standard library, the `go.sum` line of the required module that holds the function (a folder replacement: the hash of the package's Go and assembly files), or, for `runtime/v1` and a trusted module's own Go, the hash of every declaration the function reaches, transitively (widened to the package's files where the walk cannot see). It enters the entry's digest and is shown on its line (`with go1.27.4`), so a stale line names what changed. Not a build key, which keys a cached Go build. See [facts](docs/spec/facts.md#evidence), Evidence 3.
**Validated** (owner q192, q194, q195): The evidence level between proved and tested: a vouched Go fact whose guard has a current, passing ledger entry, or a rewrite law whose licence run (the law-check build, `by native`, or the check-time evaluator, `by eval`) has a current, passing entry and no refutation. It licenses a value-class change of emitted code only (a law rewrite), and of memory-class changes only the shortcut `host-view` rows (q193). Not tested, which is never acted on. See [facts](docs/spec/facts.md#evidence), Evidence 2.

**Skip statement**: `skip "reason"`, a statement of a test or law body that ends that run as skipped, never passed: `ergo test` prints `skip Name: reason` and counts it in the summary's skips, and `ergo check` reports the obligation `skipped` with the reason. The reason is a string literal in the source. `skip` is a contextual keyword: the statement is `skip` followed directly by a string literal, and elsewhere `skip` is an ordinary name ([runtime](docs/spec/runtime.md#tests), Tests 4).

**Law receipt**: The recorded outcome of running a law suite for one revision, seed and target: the status of each law (proved, tested, refuted or skipped), its case count and any counterexample, shrunk. `ergo test DIR --receipt FILE` writes one as JSON ([runtime](docs/spec/runtime.md#tests), Tests 8). A tested law, which held for drawn cases, is not a proved one, which held for every value. The build reads only the canonical receipt; no build reads one yet.

**Rewrite law**: A member law of an operation type whose body is two blocks joined by `==`, `law L(params) [when R() == C] { lhs } == { rhs }`: an equation between two straight-line uses of the type's own operations, the left block a pattern and the right its replacement. It holds for a provider when the two sides read the same, from every state a prefix reaches and the guard admits, for every continuation. The compiler may rewrite a site by it only with evidence for the receiver's provider. A parameter the right side computes with is constant at a site; every other one passes through. A top-level law of the same form, each side one expression, `law L(s string) { Unquote(Quote(s)) } == { s }`, is a rewrite law over functions: the property `lhs == rhs`, which the compiler may use left to right when the law is in the rewrite's scope (pure functions, a value with key equality, fewer calls on the right, no failure added, declared beside its root function) and its ledger entry admits it, for Go functions and Ergo functions alike.

**Post law**: An ordinary member law of the form `{ Op(params); R() == C }`, such as `law BeginStarts(n int) { BeginObject(n); pos() == AtStart }`: the state a read operation `R` reports after the operation. When its run passed for the provider, the lowering takes it as a fact to decide a rewrite law's guard at a site.

**Proof tier**: The check-time stage that sends only decidable facts, such as index ranges, length relations, integer bounds and unreachable arms, to an SMT solver within a fixed time budget, reports "proved" only when the solver discharges the obligation and "unknown" on a timeout, and lowers a proved fact as a removed check, while everything outside it stays tested, never proved.

**Fact** (value-tied): A call of a pure, total, rowless function that returns `bool`, over places (a `let`, a parameter or a field path of one), that holds where the checker can see it was true. A signature asks for one with `given`, gives one for its result with `gives`, and leaves one true across a write with `keeps`. A fact is not a value, and a write to a place it names ends it. A fact the body does not check is a law. See [facts](docs/spec/facts.md).

**Record invariant**: A fact on every value of a record type with construction authority, `type T { … } where P(it)`. It is a fact at the entry of each boundary of the type's package, and each boundary that writes a `T` leaves it true at every exit. See [facts](docs/spec/facts.md#record-invariants).

**Evidence label**: The ground on which a fact at one use rests: checked (a dominating compare, or the decidable fragment), proved (a law that listed every value or state, with a current entry in the law ledger `ergo.laws`), validated, assumed, tested (laws that passed their generated cases) or none. Only checked or proved lets a fact change memory-class code (a direct form, or an unchecked load that removes a bounds check); validated changes value-class code only; a tested fact is reported and never acted on. See [facts](docs/spec/facts.md#evidence).

**Zero-cost abstraction obligation**: A candidate abstraction must make the illegal states and invalid operations covered by its checked contract unrepresentable in checked source, while foreign and trusted behavior remains explicitly qualified, and then erase or directly lower without an abstraction-induced executable size or runtime penalty against a semantic equivalence baseline. Retained allocation, dispatch, module, byte and residual-check costs must be visible in matched receipts. Native output targets parity with or better performance than the optimized idiomatic Go member, and Go lowering pursues every material measured lowering or specialization opportunity while preserving the userland runtime contract. This obligation never removes a required guardrail and does not promise universally zero overhead or a fixed speedup multiplier.

**Semantic equivalence baseline** (acceptance term): A matched Ergo and explicit Go program pair that performs the same validation and observable work while preserving the same rows, ownership, cancellation and completed-cleanup behavior. The Go member is an optimized idiomatic Go control with the same contract. The baseline makes an abstraction's generated and runtime costs attributable rather than inferred from unequal examples. Until 2026-10-26 the baseline also had a TypeScript/Effect member; it went with the JavaScript target.

## Runtime and providers

**Lookup in flight**: In std `cache`, the call of a Get's lookup for a key while it runs. The cache holds one per key; every other Get of that key waits for it to end and then reads the entry it left, or, when it failed or its task stopped, runs its own. Go's `singleflight` call, Effect's pending `Deferred` of a `Cache` entry. See [stdlib](docs/spec/stdlib.md#cache).

**Loose reader**: Std `json`'s `DecodeLoose`, which reads text that is nearly JSON, as a model writes it, into a typed value and returns every repair it made as a `Fix`. The repairs are syntax only (prose and fences around the value, trailing commas, bare and single-quoted keys and strings, comments, what the text's end left open, the first of several values); no value changes its kind. See [stdlib](docs/spec/stdlib.md#json), json 9.

**Fix** (json): One repair the loose reader made, a variant of the closed union `json.Fix`, with the span of the original text it names. A read with no fix is a strict read: `Decode` gives the same value.

**Effect runtime provider** (direction): An implementation of the lawful runtime contract that executes Ergo effects on a target. `runtime/ergo` was the native Go provider of the earlier language (removed in batch 14), and the prototype's `rt` package is the first provider of the Go-shaped language. Pinned Effect as the JavaScript provider went with the JavaScript target on 2026-10-26.

**Lawful runtime contract**: The host operations and laws an effect runtime provider must satisfy, with each obligation's evidence classified as nominally checked, trusted decision, tested law, refuted or unresolved/unavailable.

**Host-driven program** (direction; owner direction 2026-11-02): A program whose control loop a host owns, such as a game engine, a GUI window or a TUI screen that calls it each frame or event on the host's thread through a declared entry signature, while its capabilities stay separate services provided by a context rather than one platform owning them all.

**Frame (ui)**: std `ui.Frame[E]`, the composer a view writes into: a view is `func(f *ui.Frame[E], s S)`, an explicit borrow and not a capability. The frame holds the retained tree, its view-local cells and its loads; `ui.Render` runs one pass and reconciles what it wrote into a patch (`[]ui.Op[E]`). `E` is the program's signal type.

**Load epoch (ui)**: The number of one fetch a `ui.Load` asked for, unique within its frame. A new key, a new place or a removal ends the epoch, and a result is taken only while its epoch is live (`ui.Arrive`), so a result never shows for a key it was not fetched for. See [stdlib](docs/spec/stdlib.md#ui).

**Boundary (ui)**: A `ui.Boundary(f) { … } catch { … }`: a group with no node of its own whose `catch` arms show something else when its body fails, with `ui.Pending` while data has not arrived or with a failure the body's reads declare. See [stdlib](docs/spec/stdlib.md#ui), UI 15.

**Group (ui)**: A `ui` primitive that takes a body: a container, `ui.Each`, `ui.Rows` or `ui.Boundary`. When a failure leaves its body, the group cuts the pass back to where the group started (nothing the body wrote stays) and passes the failure on. See [stdlib](docs/spec/stdlib.md#ui), UI 15.

**Parked (ui)**: Said of a node, cell or load that a pass under a stopped group did not write or read: out of the tree and the host's copy, kept with its `Id`, cells, loads and subtree until a pass writes it again (it comes back with the same `Id`) or a pass in which no group under its parent stops (it is released). See [stdlib](docs/spec/stdlib.md#ui), UI 15.

**Await (ui)**: `ui.Await(a)`, which reads an `Async[T, F]` as `T fails F | ui.Pending`: the value, `Pending` while loading, or the held failure. A view reads a load's value with `try ui.Await(…)` under a boundary. See [stdlib](docs/spec/stdlib.md#ui), UI 19.

**Crash (ui)**: An app's actor that ended with a defect or its step's typed failure (`ui.Crash[F]`). The app's session reports it once and shows the app's crash view instead of a view of the dead state; a signal of the crash view is a reload, a fresh actor from the machine's initial state. A Done or a stop is an end, not a crash. See [stdlib](docs/spec/stdlib.md#ui), UI 20.

**Zone (ui)**: A container that marks its body with a key for the pointer events of the Area around it (`ui.Zone`). The host names the topmost zone under the pointer and its box; the composer gives the Area's function the keys of the zones from the Area down to it, its box and the pointer's offset in it (`ui.ZoneHit`). See [stdlib](docs/spec/stdlib.md#ui), UI 22.

**Pointer capture (ui)**: After a Down on an Area, the host sends that Area every pointer event until an Up, wherever the pointer is (`ui.Captured` on the test host). See [stdlib](docs/spec/stdlib.md#ui), UI 22.

**Stats (ui)**: One pass's work, counted in work units per phase (signals, nodes written and committed, rows run, patch ops by kind, paint ops, fetches and cancels), never time (`ui.Stats`, `StatsOf(h)`). A view's `OnSlow` budget turns a pass over it into `ui.Slow` signals. See [stdlib](docs/spec/stdlib.md#ui), UI 21.

**Same (ui)**: `ui.Same(f, key) { … }`, a Box whose body runs only when its `Key` changed since the last pass: otherwise the pass keeps the subtree the body wrote then, and nothing under it runs or is compared. A cell write under it or a change of the disabled state around it runs it again. See [stdlib](docs/spec/stdlib.md#ui), UI 18.

**Location (ui)**: The `ui.Location` operation type: a program's history of places (`URL`s), with `Current`, `Push`, `Replace` and `Back`, bound in `cmd/<name>/main.ergo` (a `MemoryHistory` on the desktop and in a test). A change the program did not make reaches the view as `URLChanged`, through `OnURL`. See [stdlib](docs/spec/stdlib.md#ui), UI 17.

**Locator (ui)**: A `ui.Locator` value that finds nodes on a screen by role and accessible name, by label, by placeholder or by text, in tree order; a scene checks what it finds with a `ui.Matcher`. Roles and names come from the node kinds and labels. See [stdlib](docs/spec/stdlib.md#ui), UI 16.

**Paint list (ui)**: The content of a `ui.Canvas`: a list of `ui.PaintOp` values (fills and strokes of paths, and transforms and clips each closed by a pop) that the canvas's draw function wrote through a borrowed `*ui.Paint`. It is a field of the canvas node, so an unchanged list is no change, and a host replays it. See [stdlib](docs/spec/stdlib.md#ui).

**Virtual rows (ui)**: A `ui.Rows` list, whose row function the composer runs only for the rows its viewport shows, plus `ui.Overscan` rows on each side. Its viewport is view-local state the host reports (`ui.Scrolled`), and its rows are keyed as `Each` rows are. See [stdlib](docs/spec/stdlib.md#ui).

**Text measure (ui)**: std `ui.Measure`, the operation type through which text layout asks the host how wide text is and how high its lines are (`Width`, `LineHeight`). `ui.LayoutText` breaks text into lines with it, and `ui.CaretAt`, `ui.OffsetAt` and `ui.SelectionRects` give the caret and selection geometry. `ui.Monospace` is the measure of cell grids and tests. See [stdlib](docs/spec/stdlib.md#ui).

**Pacing**: std `pace.Pacing`, the operation type `{ Edge(), Hold(), Release() }` through which a loop marks the end of one unit of its work (a frame, a turn) for Go's collector, and a stretch that must not be paced mid-way (`pace.Held`, a hold: no edge inside collects, and the last release runs the collection owed); the provider `cmd` main binds decides what an edge does (`Default`, `Percent`, `AtEdges`; `Counting` in tests). Not the runtime's heap floor (`rt.Pace`), which every program has. See [stdlib](docs/spec/stdlib.md#pacing).

**UI host**: std `ui.Host[E]`, the operation type of a screen that shows patches and reports events (`Show`, `Next`, `Post`, through which a fetch's task hands its result to the loop, and `Frames`, through which `Run` asks for a `Tick` per frame only while the view animates); a value `ui.Run` drives. `ui.TestHost` is the in-process host of tests and agents: it steps the same `ui.Session` as `Run`. Not a host package's `Host` and not an **Actor host**. See [stdlib](docs/spec/stdlib.md#ui).

**Uninterruptible host wait**: A foreign Go call whose Go declaration takes no `context.Context`, so an interruption reaches the task only after Go returns. `ergo inspect` lists it for each function that reaches it (IG8; [tooling](docs/tooling.md#inspect)).

**Source** (`io.Source`): An input given chunk by chunk to a reader that asks for the next chunk when it has used the one before, so the input is never held whole. A chunk is a string that is not empty and never changes; the chunks, in order, join to the input. See [stdlib, io 10](docs/spec/stdlib.md#io).

**Stream** (`json.Stream`): A JSON `Reader` over a Source that checks the text as it reads it, so a value read from it before it is settled may come from a text that is not JSON; settling reads the rest and reports the first break as `Open` would. Not Effect's `Stream`, a sequence of values over time. See [stdlib, json 7](docs/spec/stdlib.md#json).

**Elements** (`json.Elements`): The values of a JSON Stream read one at a time with a type's derived codec, the elements of a top-level array or the values of a sequence stream (newline-delimited JSON), by a pull call `Next` that gives the next value or none after the last. Only the value being read is held, never the text. See [stdlib, json 8](docs/spec/stdlib.md#json).

**Exit accumulator**: The runtime record that combines a body's outcome with release defects and interruption into one exit cause, so a release never hides a failure.

**Definition ID**: The stable name of one checked definition, derived by the checker from the declaration: `<package>.<Name>`, `<package>.<Type>.<Method>`, or `<enclosing ID>/<construct>#<n>` for a block such as a timeout. It is meaningful only with the source revision it was computed from. Runtime traces, defect chains and `ergo inspect` share this ID space ([traceability](docs/research/traceability.md)).

**Source revision**: The SHA-256 digest of a source file, the revision that qualifies its definition IDs. A trace header carries it once, and a trace query refuses a trace whose revision differs from the file.

**Trace event**: One record of a run. It has an occurrence ID, a kind from the runtime-law vocabulary or the three additions `call`, `call-exit` and `interrupt`, a task and its parent, a definition ID and a source site, and it may carry an occurrence link. Its serialized form names failures and defects by type and never carries argument or result values.

**Occurrence ID**: The number the runtime gives a trace event when it raises it, unique within one traced task tree and taken from one counter, so an event that happens before another has the smaller ID. A capability call's occurrence ID joins its `call` and `call-exit` events.

**Occurrence link**: The occurrence ID that one trace event records for another, set by the runtime and never inferred by a reader: a `handler` links the failure, deadline or child exit it caught; a `call-exit` its call; an `observe` the child's `body-exit`; a `panic` the capability call it left. A `body-exit` has no single link: each reason of its exit carries its own origin, an occurrence link for that reason alone (a typed failure its failed call, deadline or child exit; a defect its `panic`; an interruption its `cancel-request`). An origin names the occurrence the reason came from, never an occurrence that merely carried an equal value; when the runtime cannot tell which of several equal failures a reason came from, it records none. A trace query states a cause only along these links, says "origin not recorded" for a reason without one, and labels anything else a candidate.

**Point event**: A trace event that emitted code raises at its own source site: a capability `call` and its `call-exit` with the outcome (ok, a failure, a defect or interrupted), an `interrupt` at an interruption point, a context `construct` or a catch `handler`. The runtime raises the other kinds for a task, keyed by the task's definition ID.

**Trace evidence**: The answer of a trace query (`sketchc trace`, later `ergo trace` and MCP `code.trace`): the `trace-evidence-1` envelope with the fields of the review evidence schema, then the tasks, the capability calls with their spans, the resolved events and the cause trace. The query validates the trace before it answers and refuses a malformed one with the reason.

## Domain modeling

The building blocks of domain-driven design, as Ergo writes them. [docs/domain.md](docs/domain.md) states each as a rule and quotes it from `examples/pipeline`. *Pattern* means a shape of ordinary Ergo code, with no construct or std package of its own.

**Aggregate** (pattern): A consistency boundary whose state is a union of modes, changed only by its commands: a `Decide` that gives the events of a command or its rejection, an `Evolve` that folds one event into the state, and a machine whose step is `Decide` then the fold. One actor runs the aggregates of a bounded context, by id (docs/domain.md, rule 4).

**Decider** (pattern): The pair of an aggregate's pure functions, `Decide(s S, c C) []E fails R` and `Evolve(s S, e E) S`, with its initial state. `Decide` has no `needs` row and `Evolve` no row at all, so a replay of the same commands gives the same events and state. A rejection is a record in `Decide`'s error row.

**Envelope** (pattern; module `domain`'s record planned, DK2): The record a command travels in, with its id and the context the domain reads (actor, tenant, time), so `Decide` takes the context as data and never as an ambient value.

**Receipt** (pattern): The answer to a command the context took: the command's id and the position of its events in the context's log. A resend of the same id gets the first receipt and changes nothing.

**View** (pattern): A read model: a fold over the events of a log, with a law that the fold equals the view computed from the states (`BoardIsFoldOfEvents`). Distinct from a UI view, which is a function that draws a screen.

**Policy** (pattern): A pure function from an event to the commands or notices it causes, `React(e E) []C`, so a replayed log causes the same things. A **follower** of the log runs it: module `domain`'s `Follow` delivers each record after the follower's saved cursor, at least once, and names each command it sends by the follower, the stream, the record's position and the command's index, so a second delivery is a resend. Distinct from a retry policy, which is data a saga reads, and from a **transport policy** (below).

**Api** (pattern): A bounded context's one operation type, in its domain package: its commands and reads are the operations, their failure rows the typed rejections, and its laws stand beside them (docs/domain.md, rule 5). Every surface is a provider or a caller of it. No construct of its own: an Api is an operation type.

**Hub**: The package `ergo gen api` writes for an Api, `<pkg>api`: the operation type as data (`Request`, `Op`, `Reply`, `Failure`, `Outcome`), the one dispatch `Call`, the `Client` over any way to carry a `Request`, and the laws `CallIsTheOperation` and `ClientIsCall`. Every transport is written over it. See [api.md](docs/spec/api.md#ergo-gen-api).

**Projection**: A generator that writes, from the checked Api, the plain Ergo of one surface over the hub (`ergo gen rpc`, `cli`, `mcp`), or the pattern of a UI (docs/domain.md, rule 6). No reflection at run time.

**Transport**: A projection that carries a call: its codecs of `Request` and `Outcome` and a host loop that answers through the hub's `Call` (Effect RPC over HTTP, a command line, MCP tools).

**Transport policy**: What a binary decides about a transport, as functions in `cmd/<name>` over the hub's unions, each an exhaustive `match` (an exit code per `Failure`, an HTTP status), so a new failure does not compile until each binary maps it. Distinct from a **Policy** (above), which reacts to events.

**Saga** (pattern): A process over time as a machine of its own, run durably, whose signals are the events it reacts to and its timers (`after`), and whose `work` calls the world.

**Story** (pattern): The pure reference of what a program should do for a list of acts: the domain's rules written again over its `Decide` and fold, with the world's answers read from a table.

**Scene** (pattern): The same acts played on the real program, with each external system a `testing.Script`, a clock only the scene moves, and every held call released by the end. A law says the scene ends where the story does (`SceneAgreesWithStory`).

**Stage**: `testing.Stage`, the one kernel that domain, UI and page scenes share: play an act, settle until no work runs, advance the clock, show; `testing.Play` plays a list of steps on one. `actor.NewStage` is an actor's (stdlib.md, Testing 14). Distinct from a stage of the worked example's pipeline, which is a step of a run.

**Stack**: An ordinary function that asks kinds for the objects of a deployment, passing the value of one object to the next.

**Kind**: An operation type a stack calls for one kind of cloud object, `Ensure(id, props) A`. Plan and apply are two providers of it.

**Driver**: What a provider module implements for one kind: `Attrs`, `Apply`, `Read` and `Delete`, keyed by id, with the laws `AttrsIsApply`, `RetrySafe`, `ReadAfterApply` and `DeleteIsIdempotent`. Distinct from **Resource**, a value with a checked `Close`.

**Civil date**: A day of the calendar with no zone, `civil.Date`: ISO 8601's proleptic Gregorian calendar, years 1 to 9999. It holds the count of days from 1970-01-01, and only package `civil` builds one, so it is always a real day. Temporal's `PlainDate`; Go's `cloud.google.com/go/civil.Date`. See [stdlib](docs/spec/stdlib.md#civil).

**Period**: An amount of calendar units, `civil.Period { Years, Months, Weeks, Days }`. Adding one moves the years and months as one count of months, with the day clamped to the new month's last day, then the weeks and days. A period is not a duration: a month has no fixed length.

**Civil span**: A range of civil dates with both ends included, `civil.Span`, built only by `NewSpan`. A range of instants is `time.Interval`, half open.

**Civil format**: A format of dates and times as a value, `civil.DateFormat` or `civil.DateTimeFormat`: a list of `civil.Field` values, built once by `Pattern` (LDML letters), `FromLayout` (Go's layout) or `Fields`, and checked when built. Go's layout is a string read on each call. See [stdlib](docs/spec/stdlib.md#civil).

**Relative distance**: How far apart two instants or two dates are, as a count of one unit and a direction, `civil.Approx { Count, Unit, Past }`, by date-fns's `formatDistanceStrict` rule; `civil.Phrase` writes it in a `Words` value ("3 days ago"). See [stdlib](docs/spec/stdlib.md#civil).

**Zone**: A time zone, `tz.Zone`: the offsets from UTC a place has used and the instants it changed them, read from the IANA database's TZif bytes. Go calls it a `Location`. See [stdlib](docs/spec/stdlib.md#tz).

**Zoned time**: An instant seen on a zone's wall clock, `tz.Zoned { At, Zone }`. Its calendar arithmetic (`tz.Add`) moves the wall date and keeps the wall time; its text is RFC 9557's (`2027-03-14T03:30:00-04:00[America/Toronto]`). Temporal calls it a `ZonedDateTime`. See [stdlib](docs/spec/stdlib.md#tz).

**Local zone**: The zone a program's host calls its own, the capability `tz.Local`: a program that shows local time has it in its row, and its context chooses the provider (`tz/host`'s, which reads `TZ`, or a fixed name). Go's `time.Local` is ambient. See [stdlib](docs/spec/stdlib.md#tz).

**Zone database**: The zones by IANA name, the operation type `tz.Zones` in a function's row. Std's own copy of one IANA release (`tz/iana`) is the default provider; the host's zone files (`tz/host`) are an explicit choice.

**Gap**: A wall time a zone skips, as 02:30 on the day clocks go forward: `tz.Gap`, one variant of what `tz.Resolve` gives. No instant shows it.

**Overlap**: A wall time a zone shows twice, as 01:30 on the day clocks go back: `tz.Overlap`, with the earlier and the later instant.

**Cron expression**: When a job fires, as five fields of wall time (minute, hour, day of the month, month, day of the week), `cron.Expr`, read by `cron.Parse`. It fires at wall minutes; in a zone, `cron.Next` gives each the instant `tz.Compatible` chooses, so a minute in a gap fires once, a gap late, and a minute in an overlap fires once, at its first showing. See [stdlib](docs/spec/stdlib.md#cron).

## Earlier `.ef` language

These terms describe the retired `.ef` language, with its `effect fn`, `run`, `raises` and `service` forms. The .ef migration removed its compiler ([retirement record](docs/receipts/retire-ef.md)); its runtime, `runtime/ergo`, was removed in batch 14. The terms stay as design reference. The Go-shaped language replaces it; where a term has a counterpart above, the entry says so.

**Record**: A nominal data type with declared fields. Construction checks every required field and its type. Counterpart: struct.

**Closed enum**: A nominal set of alternatives, each with its own declared payload. Counterpart: union.

**Absence**: An explicitly selected closed alternative describing a missing value, distinct from an uninitialized field or a bare nil/null value.

**Void**: The no-value type, spelled `void`, describing successful completion without an information-bearing result. Its explicit expression is `void`. It is distinct from absence and from a computation that does not complete successfully.

**Option**: A generic closed enum with `None` and `Some(T)` alternatives. `Some` contains an admitted value of `T`, never an unchecked nil host reference.

**Exhaustive match**: An interpretation that covers each declared alternative exactly once and executes only its selected arm. Counterpart: totality.

**Signed-64 wrap**: The portable `i64` arithmetic rule: each admitted `+`, `-`, `*`, `/` and unary `-` result is reduced to its two's-complement signed 64-bit residue, so `MIN / -1` is `MIN`. Division and remainder truncate toward zero.

**Saturating float conversion**: The portable rule of `T(x)` for a float `x` and an integer type `T`: `x` truncates toward zero, a result past `T`'s bounds is the bound on its side (the infinities included), and NaN is 0, on every host. The checked forms, `math.ToInt` … `math.ToUintptr` (one per integer type), fail `OutOfRange` where the conversion saturates. Counterpart: Signed-64 wrap, the rule of integer arithmetic ([types.md](docs/spec/types.md#conversions) Conversions 4).

**Divisor proof**: Checked evidence that an `i64` divisor is not zero: a nonzero integer literal, or a local binding inside an `if` branch whose comparison of that binding with an integer literal excludes zero. `/` and `%` are admitted only with one, so division by zero is not representable in checked source.

**Failure payload**: The declared data carried by a named failure, separate from an ordinary success value.

**Entry failure report**: The deterministic stderr text and exit status a program entry produces when `main` fails. It lists each cause reason in order, as a named failure with its payload, a defect message or an interruption. Payloads are rendered from the checked types of `main`'s failure row, so the report is deterministic for every failure in that row, including built-in, codec and HTTP listener failures. The one exception is an unclassified HTTP listener failure, which keeps the host's text.

**Effect contract**: The success value, named failures, and required services of a deferred Ergo program.

**Failure row**: The unordered set of nominal failures admitted by an effect contract. Counterpart: error row.

**Requirement row**: The unordered set of nominal services needed to execute an effect. Counterpart: capability row.

**Callable value**: A named function carried as a value with explicit parameter, result, failure and requirement contracts. Pure functions and functions constructing deferred effects have distinct callable kinds.

**Row parameter**: A declaration-qualified variable representing a finite failure or requirement row. Ordinary function application obtains its least bound from direct callback argument rows.

**Callback-result relation**: Retained evidence relating a callback invocation's returned handles to its resolved named callee and input ownership. It is separate from ownership of the callable value; an unresolved relation remains potential ownership.

**Postfix construct**: A compiler-known construct written after a receiver with `.`, such as `.catch<E>(h)`, `.provide<S>(p)` or `.timeout(ms)`. The parser claims its word after any receiver. The 2026-10-12 construct bar no longer admits this spelling for constructs; the Go-shaped language replaces these forms with `catch`, `with` and `timeout.After`.

**Pipe**: The notation `x |> f(args)`, which is the ordinary call `f(x, args)`.

**Receiver method**: A function declared with an explicit receiver in its type's owner module, such as `fn (u: User) display()`. The call `u.display()` is exactly `User.display(u)`.

**Where clause** (earlier spelling): A requirement that each type argument's owner module declares a method with a given name and signature. Counterpart: constraint.

**Owner module**: The module that declares a type and its receiver methods. Counterpart: owner package.

**Constant parameter default**: A literal or named constant declared for a parameter and inserted at the call site when the argument is omitted.

**Visibility**: Whether another module may name a declaration. Declarations are private by default and exported with `pub`; the case of a name carries no meaning. Superseded 2026-10-27 by exported names (owner: "Yeah, let's go with your [recommendations]"). Counterpart: exported name, implemented for fields, operations and construction (196b Q6).

**Refinement**: A defined type with a decidable predicate, `type Port int where 1 <= it && it <= 65535`. The compiler decides the predicate to spans of the base's values, checks every constant against it, derives the parse function `ParsePort` (the only way in for a value that is not a constant) and the law `PortWhere`, and generates values inside it. Distinct from construction authority, which a function checks and the compiler cannot decide. See [types](docs/spec/types.md#refinements).

**Construction authority**: The right to construct values of a nominal type. In the `.ef` language a `pub readonly` type let other modules read and match its values while only its owner module constructed them. In the Go-shaped language, an unexported field gives the same authority: outside the package that declares the fields, the struct, a defined type over it and a type that holds it by value have no literal, no record update and no implicit zero value (`lib.Q{}`, `lib.Q{..q, N: 2}`, `let q lib.Q`, a zero-filled field of an enclosing struct), S114, so only the owner package builds the value and its exported functions are the way in. `check.Constructible` exports the rule. See [types](docs/spec/types.md#zero-values-and-definite-initialisation) Zero values 6.

**JSX pragma**: A user-defined selection of the factory, fragment and result types to which JSX notation desugars.

**Target provider**: An implementation of a service on a particular execution target.

**Provider construction contract**: The configuration and required services used to create a provider value, distinct from the contract of invoking its service operations.

**Provider recipe**: A deferred constructor with explicit configuration and construction requirements. Each execution materializes a provider value.

**Materialized provider value**: An already constructed service implementation that can be explicitly reused without reexecuting its recipe.

**Layer**: A lazy checked acquisition plan selecting implementations and composing their construction dependencies. Its contract records exposed services, construction failures and unsatisfied inputs. Counterpart: context.

**Layer binding**: A nominal service selection at a declaration site, distinct from its implementation's type and from an acquired provider instance.

**Shared acquisition node**: One identified construction selection reused within a graph build under compatible input identities; concurrent consumers await the same acquisition.

**Graph build**: One scope-owned execution of a layer plan, with its own acquisition table and completed startup or rollback outcome. Separate builds are independent. Counterpart: build.

**Layer replacement**: A checked pre-acquisition substitution of one binding throughout a composed graph, followed by reconstruction of its effective construction edges and contract.

**Fresh layer build**: An explicitly separate provision/build boundary with independent acquisition. A flat graph does not admit a second binding of one nominal service by copying its subtree.

**Ownership provenance**: The relationship between a retained value and the lifetime that owns it. Borrowed, newly owned and unknown relationships carry different evidence.

**Semantic revision**: The identity of the checked snapshot described by inspection or diagnostics, including imported declaration data and behavior contracts when present.

**Managed fiber**: An execution of an Ergo effect with an owner and a completion result. Its cancellation request and completed shutdown are distinct states. Counterpart: task.

**Owning scope**: The lifetime that owns managed fibers and resource releases. Its closure establishes their completed shutdown and cleanup. Counterpart: owner scope.

**Host declaration**: A Go declaration supplying the native shape and identity of an imported value or callable.

**Initialization root**: A declared foreign Go import's package initialization, retained in a native application independently of reachable calls and lowered as a named or blank Go import. Module availability, export-data discovery and metadata lookup are not initialization roots.

**Native Go protocol**: An imported Go interface and its method set, interpreted by Go's assignment rules. Its behavioral obligations remain distinct from type compatibility.

**Host borrow**: Use of a native value under an existing owner's lifetime without acquiring release authority; retention and aliasing constraints remain explicit.

**Host adoption**: Establishing managed release authority for an admitted native resource and its tracked aliases under an owning scope.

**Native descriptor**: An OS-specific handle whose validity follows a native resource lifetime; it is distinct from the resource object and its interfaces.

**Binding contract**: Supplemental behavioral facts attached to a host declaration, including its cancellation, failure, resource, and trust policy.

**Structural derivation**: Generating a codec's structural rules from canonical checked data declarations and an explicit representation policy.

**Codec plan**: The bounded, shared structural DAG one derivation checks for a domain type under a profile. An application retains it only through an executed direction.

**Codec transformation**: A checked conversion in a codec's decoding or encoding direction, carrying its own expected failures and required services. The two directions need not be mathematical inverses. A witness states one with `decode:` and `encode:` functions over a structurally derived representation.

**Failure witness**: A structural codec witness of a failure declared in the module, used only as an endpoint's failure body. It encodes the failure tagged with its name and has no direction functions.

**Endpoint**: A declaration `derive name = Http.endpoint(...)` binding a method, an exact static path, optional input and output witnesses, a success status, a handler function and the response of every named failure its input decoding, handler and output encoding can raise. It synthesizes an ordinary handler function from `HttpRequest` to `HttpReply`.

**Route table**: A declaration `derive name = Http.routes(...)` composing endpoints into one handler that dispatches on exact method and path; no two entries serve the same pair.

**Transformed witness**: A codec witness that states a codec transformation. A parent witness composes transformed witnesses only by listing them in its `with` clause; each applies wherever its domain type occurs in the parent's representation.

**Machine plan**: The finite, source-revisioned provider-independent facts lowered from a checked machine declaration, including nominal state/event/outcome/output identities, callable rows, transition edges, entry/work identity rules and inspection data.

**Machine provider**: An ordinary typed library, function or service implementation that consumes a machine plan and owns its execution, admission, scopes, cleanup and runtime observation without changing the plan's identities or public rows.

**Provider conformance**: Evidence that independently authored machine providers execute the same checked plans on the same target with unchanged declarations/compiler selection while preserving the declared observable contracts and recording their separate runtime obligations.

**Machine-backed actor**: An actor whose behavior follows a checked machine plan, adding transitions and state-entry lifetimes to ordinary actor ownership and messaging.

**Supervisor**: An owner that observes completed child exits and applies an explicit restart, stop or escalation policy using fresh child factories and an explicit restart budget: a finite restart intensity, or `unbounded` flagged by the `no-unbounded-restart` justifier rule. Counterpart: set.

**Actor address**: A typed identity used to route to an actor independently of its current running instance; an address alone promises neither persistence nor delivery.

**Actor incarnation**: One running instance associated with an address, distinguished from its predecessors and from its individual state entries.

**Durable actor**: A machine actor whose committed transitions a journal keeps before anyone sees them, so a new actor recovers its state by replaying them with the machine's pure step (std `actor.StartDurable`, `journal.Start`). See [runtime](docs/spec/runtime.md#machines-and-actors) 17. A durable entity adds an address and ownership to it.

**Actor host**: std `actor.Host[A]`, an empty slot for one child, made by `actor.NewHost` in a step and owned by the entry the step enters, whose work fills it once (`actor.ChildIn`); the entry's end closes it. A host is entry data, not durable data: a durable step may make one, and a host made in a durable recover's replay is **unbound** until the recovered actor's first entry binds it (when its first state holds it) or closes it. A journal never brings back a live child. Not a host package's `Host`. See [runtime](docs/spec/runtime.md#machines-and-actors) 16 and 17.

**Journal**: The std operation type `journal.Journal[S, E]` that keeps a durable actor's committed signals after the last snapshot of its state, in order. Its providers are `journal/memory` and `journal/file`.

**Durable entity**: An addressable actor whose provider contract preserves declared domain data and acknowledged progress across process failure, with explicit storage, codec and ownership policies.

**Workflow replay**: Reconstructing execution from versioned recorded steps and outcomes under an explicit replay contract; ordinary effect recipes do not imply replay safety.

**Machine step**: An ordinary pure or effectful function that consumes a state and event and returns an explicit transition decision. Effectful evaluation has a machine-backed actor-owned scope and completes before state commit.

**State entry**: One owned lifetime of a machine-backed actor's current state, identified separately from that state's tag and payload.

**Re-entry**: Closing a state entry and starting a fresh one, including when the state tag stays the same.

**Entry epoch**: An identity distinguishing a state entry from prior entries so obsolete work cannot update a later entry.

**Actor host**: An `actor.Host[A]`, an empty slot for one child that a step puts in the state it enters. The entry that state opens owns it, its work fills it once with `actor.ChildIn`, and it closes when the entry ends, so a reader's `Get` gives a child of that entry or fails `Stopped`. Not a host provider (see **Ambient**): it reaches no host state. See [runtime](docs/spec/runtime.md#machines-and-actors) 16.

**Transition decision**: The value a machine step produces for one admitted event: `enter`, `stay`, `ignore`, `reject` or `done`. Its type is the predeclared `decision<S, O>`. Counterpart: decision.

**Region**: A payload field of one state variant, bound by the machine declaration to a separately declared child machine whose state it holds. Several regions on a variant are orthogonal; one region is a nested (hierarchical) child.

**Region entry**: The state entry of one active region, nested in its parent's entry with its own epoch, so re-entering one region leaves its siblings' work untouched.

**Machine history**: A remembered child state kept as ordinary data in an inactive variant and restored by a later `enter`; deep history restores the value, shallow history keeps only its top-level variant. Restoring always starts fresh entries.

**Request scope**: A fresh owning scope for one HTTP request, linked to connection and server cancellation. Completed shutdown includes its handler cleanup.

## Tooling and testing

**`ergo fix`**: The command that writes the missing clauses of a signature on request. A diagnostic for a body that exceeds its signature names the exact clause to add, and `ergo fix` writes that clause into the source. It is the tool half of the tiebreak **Explicit local signatures** beat **inference**: the checker never infers a clause, and the tool writes it for the author ([decision](docs/decisions.md#owner-direction-2026-11-12)).

**Fact table**: What the checker knows about one package, as rows that tools read instead of walking typed trees: its symbols, its sites, their positions, and relations (union to variant, caller to callee, operation to its provider, machines). A table names a symbol of another package by its handle, never by a pointer. Std `code` answers from them, and `ergo code` is its command line ([code](docs/spec/code.md)).

**Site**: One place in source that names a symbol, with a kind: `def`, `read`, `write`, `call`, `typeuse`, `construct`, `match`, `import`, `needs`, `fails`, `provides`, `binds` or `handles`. References, type uses and the uses in a signature are one question with a kind filter.

**Handle**: The printable, stable name of a declaration: its package's import path, a dot and its name (`calc/ast.Parse`), with a member after its type (`calc/ast.Token.Kind`) and a parameter after its function (`calc/ast.Parse.src`). One declaration has one handle, and the declaration at a handle prints that handle back. A local that its declaration binds more than once has none and is named by its position.

**Address**: How a question names a declaration: a handle, a position (`file:line:column`) or a short name (`Parse`, `Token.Kind`). A short name that fits two declarations is `Ambiguous` with every candidate; the reader never picks one.

**Linked Ergo package**: An Ergo module that the `ergo` binary runs as checked-in Go: `go run ./scripts/linked` lowers it, as `ergo lib` does, into `internal/linked/<name>`, and a test fails when the Go is stale. It stands in for the mixed binary of capstone stage S1 ([tooling](docs/tooling.md)).

**Plan**: A computed change to source that writes nothing: its edits, the files and revisions it read, the check of the changed packages in an overlay, its proof class, and the contracts it changes. Only `Apply` of a plan writes, and only against the revisions the plan read.

**Proof class**: What a plan claims about behaviour: `Alpha` (a rename: the fact tables after equal those before under the name map), `SameCode` (the lowered program is equal), `Law` (a rewrite that is a law, with its last run), or `Checked` (the result checks; nothing is claimed about behaviour).

**Name capture**: A rename whose result resolves differently: a use of the renamed declaration now resolves to another one (a local that shadows the new name), or a word now names the renamed declaration. It shows as a fact-table row that differs from the tables before under the name map, and the plan is refused `Capture` ([code](docs/spec/code.md#rename)). Not a pattern's named capture.

**Pattern**: Code to find, written as an Ergo function and checked by the ordinary checker. Its body is the code; a name in it matches a use of the same symbol, whatever the qualifier at the site.

**Hole**: A parameter of a pattern. It matches an expression of its type; used twice, it matches equal code both times.

**Codemod**: A program that changes code through plans: an ordinary Ergo program that imports std `code`, asks a `Reader` for sites, and applies plans with a `Writer`.

**Lint advice**: Optional guidance about an admitted program, distinct from diagnostics that determine whether its contracts are valid.

**Justifier rule**: A default-preset lint rule that flags a legal but reviewable choice, such as an `unbounded` budget, until a suppression records the reason.

**Suppression status**: The outcome of one reasoned lint suppression in one analysis. It is applied when it removed a finding of its rule, unused when that rule ran and found nothing to remove, or not evaluated, with a reason, when the rule did not run.

**Lint cost receipt**: Raw per-run wall-clock measurements of one lint analysis. Frontend checking, fact extraction, each rule pack's fact serialization and process phases, and the merge are kept separate. A receipt carries no aggregate and supports no performance claim on its own.

**Expression anchor**: A source location identifying the expression described by a diagnostic or local type query.

**Diagnostic code**: The stable identifier of one checker rule, such as `S102` in the prototype or an `EF` code in the main compiler's registry. [docs/spec/diagnostics.md](docs/spec/diagnostics.md) maps each prototype code to its rule and a fixture.

**Inlay hint**: Text the language server shows in the editor without changing the source, such as `closes: out, in` at a block end that releases resources.

**Dependency graph**: A revision-scoped view of program contracts and the relationships between functions, services and providers. Incoming relationships identify dependents.

**Layer construction path**: A source-grounded sequence of acquisition dependencies explaining a selected node, remaining input or startup failure, separate from service-operation call edges.

**Owning test case**: A checked effect whose normal completion includes shutdown of its children and release of its resources.

**Live test mode**: Explicit permission for a test program to use host capabilities or real program time, distinct from fixture substitution.

**Test watchdog**: A harness deadline independent of program time. Forced termination leaves managed cleanup unconfirmed.

**Application receipt**: A raw measurement of one built application: the runtime modules and declarations its plan retained, its generated files, imports, dependencies, effective build settings, executable or module bytes and symbols, and the external runtime it does not contain. It reports the compiler distribution separately and makes no performance claim.

**Unmatched floor** (size conformance): A control program that does less than the fixture it accompanies, such as printing a result without the entry's cancellation and scope. It is recorded only as a lower bound and is never compared as a member of a semantic equivalence baseline.

**Program time**: The time authority under which managed sleeps and deadlines execute. Test program time can advance independently of the watchdog's wall clock.

**Virtual clock**: `testing.VirtualClock`, a provider of `Clock` and `Scheduler` whose time jumps to the next timer when every task in the test is blocked.

**Quiescence**: The scheduler state in which no managed fiber is running or runnable. A wake in flight is not idle: a fiber that has been signalled, fired or cancelled but has not run again is runnable. At quiescence, every pending timer that managed work can register without outside input is registered. `Scheduler.awaitRegistration()` returns at quiescence, and `Scheduler.advance` selects each deadline only at quiescence. When several fibers wait for quiescence at once, the order in which they are released is unspecified. Interrupting a waiter is an ordinary interruption. Unmanaged goroutines, host promises and foreign blocking calls are outside it.

**Latch handoff**: What `Sync.signal` does to the fibers parked on a latch. It completes the latch, makes each waiter runnable and returns. Wherever execution is serialized into turns (Go under the managed test scheduler), the signaller keeps its turn and each waiter resumes later on a turn of its own, never inside the signaller's turn. Interrupting the signaller after `signal` returns cannot undo the signal or affect a waiter it does not own. A waiter interrupted after it is signalled but before it runs, directly or by its owner's close, ends interrupted, and the latch stays signalled. Live Go has no turns: a waiter runs on its own goroutine, possibly in parallel with the signaller, so an interrupt after `signal` races with it.

### Bench

The referee of `ergo bench` ([specs/bench.md](docs/specs/bench.md); std `bench`).

**Bench**: A declaration that names a piece of work to measure, its bench sizes, the counter it is judged on and the claims it holds. Every build but a bench build erases it, as every build but a test build erases a `test`.

**Bench case**: One `bench` declaration, run on every side the run names.

**Bench size**: A value of a bench case's one `int` parameter. A case names two, a small and a large one.

**Bench unit**: The piece of work one step of a bench size stands for (one lookup, one row). A bench block's value is the cost of one bench unit: the counter at the large size less the counter at the small one, over the difference of the sizes, so a fixed cost falls out.

**Bench block**: One fresh process of one side at one bench size, with the counters it reports. Not the language's **Block**.

**Bench side**: What one half of each pair runs: a commit, a compiler, a lowering, a hand Go control or a command. A run has two, base and head.

**Bench control**: A side that is a hand-written Go package, named by the bench case with a no-worse claim, so Ergo is held to the hand Go it must match or beat.

**Pair**: One bench block of each side, run back to back, base first and head first in turn. Its difference is `ln(head) - ln(base)` when every value of the series is above 0, else `head - base`.

**Series**: The pairs of one bench case and one counter, judged together at the planned looks.

**Verdict**: What the referee says of one series: `Better`, `Worse`, `Same`, `NoWorse`, `Unclear`, `Underpowered`, or `Refused` with its refusal. Every verdict but a refusal carries its interval and pairs.

**Refusal**: Why a series has no value (`NoCounter`, `Multiplexed`, `RunIncomplete`, `NotEquivalent`, `CounterUnstable`, `NoMetric`, `FewPairs`, `TooManyPairs`, `NotFinite`, `SharedRunner`). A refusal carries no number; it is never read as 0.

**Exact counter**: A counter that holds one value per side in every bench block (elements copied, checks left, allocations under one processor, a program's own count). Compared exactly, with no test; one that moves is refused. Only an exact counter may stand in a `hold` clause or a growth law.

**Counted counter**: A hardware count (instructions, branches), tight on a loaded host but not exact. Judged over pairs, never held exactly.

**Timed counter**: A time or a memory peak (CPU, wall, peak resident memory). Judged over pairs, never held exactly, and never by the gate.
