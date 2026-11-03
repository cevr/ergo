# Resources and automatic release

Status: **designed, not yet implemented**, except the providers a `with` builds, which it acquires, owns and releases ([Context ownership as built](#context-ownership-as-built); #199), and the masks of a written `Close` and of `scope.OnExit` (R9). Local, aggregate and moved resources, and generated `Close`, are [#197](https://github.com/cevr/ergo/issues/197). This file follows the owner's direction of 2026-10-26 ("How can we make it simpler so that defers are automatic … Kind of like acquire release"), adopted in the body of [#65](https://github.com/cevr/ergo/issues/65). It replaces the earlier rules that made a resource linear with a checked `defer` and registered constructor resources with `build.Scope` (README Gap 13). Definitions of resource, owner scope, move, lend and consume are in the [glossary](../../GLOSSARY.md).

The guarantee is Effect's `acquireRelease`: if an acquisition succeeds, its release runs, once, on every exit. The spelling is Go's: no `defer` to write and none to forget.

```go
func copyFile(src, dst string) fails NotFound | WriteFailed needs FS {
	let input = try FS.Open(src)
	let out = try FS.Create(dst) // if this fails, input is still closed
	try io.Copy(out, input)      // lends out and input
} // out closed, then input
```

## What is a resource

1. **R1.** A type is a resource when it has a method `Close()`, when it is a struct with a resource field, or when it is a slice, array or map with a resource element type (R34). A `Task` and an actor handle are resources ([runtime.md](runtime.md#tasks)).
2. **R2.** `Close` must not declare an error row (decision #6). Status: **implemented in the checker**: a `Close` method or an operation type's `Close` with a nonempty failure row is a type error (#199 stage 3a). A defect in `Close` joins the exit cause ([runtime.md](runtime.md#exit-causes)). A fallible cleanup handles its failure inside `Close`: `p.conn.Close() catch { case CloseFailed: log.Warn("close failed") }`. *Prior art:* Effect's release has error type `never`; Go's `io.Closer` returns an error that callers routinely drop.
3. **R3. Generated `Close`.** A struct whose fields include resources gets a generated `Close` that closes those fields in reverse declaration order. A hand-written `Close` on such a struct adds behaviour that runs first; the generated field releases still run after it.

## Owner scope

1. **R4.** Every resource value has exactly one owner scope: the block that acquired it, or the scope it moved to.
2. **R5. Release at scope end.** When an owner scope ends, it closes each resource it still owns, in reverse acquisition order. This holds on every exit: normal completion, `return`, an escaping failure, a defect and interruption.
3. **R6. Loop iterations.** The body of a loop is its own owner scope. A resource acquired in an iteration is closed at the end of that iteration, not at function exit.

   ```go
   func importAll(paths []string) fails NotFound needs FS {
   	for p in paths {
   		let f = try FS.Open(p)
   		try load(f)
   	} // f closed at the end of each iteration
   }
   ```

   *Prior art:* Go's `defer` runs at function exit, which leaks one file per iteration; C# `using var` and Python `with` scope to the block.
4. **R7. Failure during acquisition.** When a later acquisition in a scope fails, the resources acquired earlier in that scope are closed before the failure escapes (`copyFile` above). Status: **implemented** for the providers a `with` builds, from a named context and from an inline context whose entries are explicit provider expressions such as `try openB()` (#199 stage 3c; [Context ownership as built](#context-ownership-as-built) 8). A typed failure, a defect or a stop in a later constructor registers nothing for that constructor and closes the earlier providers of the `with`, last built first, before it leaves the `with`. Other owned resources wait for the release of owned values at their scope end.
5. **R8. Atomic registration.** A successful acquisition and its registration for release are one step: interruption cannot fall between them (I2, [runtime.md](runtime.md#i2-automatic-masks)). Status: **implemented** for the providers a `with` builds (#199 stage 3c; [Context ownership as built](#context-ownership-as-built) 8). Each provider with a checked `Close` is built and registered in one masked step, so a constructor that returns a value after a stop arrived still registers it, and its `Close` runs. The stop is observed after the registration, at the next interruption point.
6. **R9. Release runs masked.** Releases run uninterruptibly, to completion, even when the scope ends by interruption. Each release runs even when an earlier one had a defect, and every defect is kept in the exit cause. Status: **implemented** for a written `Close` on every dispatch path and for `scope.OnExit` (#199 stage 3a; [runtime.md, Release masks as built](runtime.md#release-masks-as-built)). The automatic release of the providers a `with` builds is **implemented** (#199 stage 3b; [Context ownership as built](#context-ownership-as-built)). The automatic release of any other owned resource at its scope's end is designed, not yet implemented (#197).

## Moves

A move transfers ownership. After a move, the source binding is unusable, and using it is an error.

1. **R10. Return.** `return r` moves `r` to the caller, which becomes its owner scope.
2. **R11. Provider field.** A resource stored in a field of a provider that `context` builds moves into the provider. The provider is a resource (R1), and the `with` block that built it closes it when the block ends.
3. **R12. `go`.** A `go` task that captures a resource lends it when the structured-lend rule (R27) holds, and moves it into the task otherwise. A moved resource is closed by the task's scope when the task ends.
4. **R13. Channel send.** In v0 a channel whose element type is a resource is an error, so no resource moves by a send ([Channels](#channels); README Gap 30).
5. **R14. Aggregate construction (README Gap 15).** Building a struct value with a resource field moves the resource into the struct. The struct is a resource and has the same owner scope as the binding that receives it. Building a slice, array or map with resource elements moves them into the container ([Containers](#containers)).
6. **R15. Assignment.** `let q = r` moves `r` to `q`. There are no copies of a resource, so two releases of one acquisition cannot be scheduled. *Prior art:* Rust moves by default; Swift `~Copyable` types are moved and `consume`d.

## Structured lend

A task may use a resource that it does not own when the task is certain to be stopped and drained before the borrow of that resource ends.

1. **R27. Borrow lifetime.** `task.Go() { f(x) }`, or any `task.Go` block that captures `x`, **lends** `x` to the task, instead of moving it, when the resulting `Task` is guaranteed to be released (stopped and drained) before the borrow of `x` ends, on every exit. The borrow's end is fixed by the root owner of `x`:
   - **Root owner.** The checker keeps, for every borrowed value, its root owner and that owner's release point. A local resource is its own root, owned by the scope that binds it. A value reached by indexing (R35), by a field of an owned aggregate, or by capture in a non-escaping closure (R30) inherits the root owner of the value it was reached from: `files[0]` has the root owner of `files`. A borrow ends at the root owner's release point, which is the lexical end of the root owner's scope (R5), or the release of the enclosing aggregate (R3, R37).
   - **Incoming borrows.** A parameter (R16), and a value reached from it, has a root owner outside the call, which outlives the call. As seen from the callee, its borrow ends when the function returns. Only incoming borrows take this bound. Inside a non-escaping closure, a captured borrow is an incoming borrow of the closure, bounded by the closure's return.
   - **Release before the end.** The `Task` must be owned by a scope or aggregate that is released before the borrow's end on every exit, including the automatic release edges of R5 and R3 on normal completion, `return`, failure, defect and interruption. Each of these satisfies the condition: the `Task` is bound after the root owner in the same owner scope, so reverse acquisition order (R5) stops and drains it first; the `Task` is owned by a scope nested inside the root owner's scope, which ends first; the `Task` is stored in a struct field declared after the root owner's field, so the generated `Close` (R3) stops and drains it first; or the `Task` is stored in a container or aggregate whose own owner satisfies one of these conditions. For an incoming borrow, every scope local to the function qualifies, because each one is released before the function returns.
   - Moving or returning the `Task` to an owner that does not satisfy the condition is an error.

   Otherwise the capture moves `x` when the function owns `x` (R12), and is an error when `x` is lent to the function (R17) or borrowed through an index or a field, since a borrow cannot move its element out (R35, R36).

   *Prior art:* Rust's `std::thread::scope` requires the borrowed environment (`'env`) to outlive the thread scope (`'scope`), so every scoped thread is joined before the borrowed data can be dropped. R27 is that inequality, checked against the root owner's release point instead of a written lifetime.
2. **R28.** While a task holds a structured lend of `x`, `x` and its root owner must not be consumed, moved alone or converted. Consuming the task first (`t.Stop()`, `t.Wait()`) ends the lend. Moving `x` and the task together into one aggregate keeps the lend when the aggregate preserves their order (R27).
3. **Lossless wrapping.** A helper receives `x` as an incoming borrow whose root owner outlives the call, so any task that the helper releases before it returns satisfies R27. Moving code that starts and drains a task into a helper function therefore never turns a valid program into an invalid one. This keeps the owner's composability rule ([glossary](../../GLOSSARY.md), "Composability checks"). *Prior art:* Swift task groups end every child before `withTaskGroup` returns, so children may use the caller's borrowed values; Rust's `std::thread::scope` lets scoped threads borrow from the enclosing function because they are joined before `scope` returns.
4. **R29.** Inside one composite literal, a field value may be a `task.Go` call that lends a resource given to an earlier field of the same literal. The elements are evaluated in order, and the literal satisfies R27 when it completes.

```go
import task

type Postgres {
	pool    sql.Pool,   // declared first: released last
	refresh Task[Unit], // declared after pool: stopped and drained first
}

func OpenPostgres() Postgres fails ConnectFailed needs Config, Clock, Scheduler {
	let pool = try retry.On[ConnectFailed](retry.Exponential(200ms).Times(5)) {
		sql.Open(Config.String("DATABASE_URL"))
	}
	// pool moves into field pool; task.Go() { keepWarm(pool) } lends it, because its task
	// lands in field refresh, declared after pool (R27, R29). Nothing closes here.
	return Postgres{pool, task.Go() { keepWarm(pool) }}
}
```

Negative examples, each an error:

```go
import task

type Bad {
	refresh Task[Unit], // declared before pool: would be stopped after pool closes
	pool    sql.Pool,
}
func openBad() Bad {
	let p = open()
	return Bad{task.Go() { keepWarm(p) }, p}
} // p is moved into the task by R12, then used: error

func early() {
	let p = open()
	let t = task.Go() { keepWarm(p) } // structured lend: t is bound after p
	p.Close()                         // error: p is lent to t, which may still run (R28)
	t.Stop()
}

func leak(p Pool) Task[Unit] {
	let t = task.Go() { poll(p) } // p is lent to leak
	return t                      // error: the task would outlive the borrow of p (R27)
}

func bad() {
	let mut running = Map[int64, Task[Unit]]{} // owned by bad's scope
	with context {} {
		let files = []File{open()}                // root owner of files[0]: this block
		running[0] = task.Go() { read(files[0]) } // error: running is released at the end of bad,
		"done" // after files is released at the end of this block (R27)
	}
	waitForChildRead()
}
```

A lent parameter, allowed:

```go
import task

func useOnce(p Pool) {
	let t = task.Go() { poll(p) } // structured lend: t is owned by useOnce's scope, released before useOnce returns
	t.Stop()
}
```

The corrected forms of `bad`, allowed:

```go
import task

func good() {
	with context {} {
		let files = []File{open()}
		let mut running = Map[int64, Task[Unit]]{} // bound after files in the same block
		running[0] = task.Go() { read(files[0]) }  // running is released first (R5, R37)
		"done"
	}
}

func goodTask() {
	with context {} {
		let files = []File{open()}
		let t = task.Go() { read(files[0]) } // t is owned by the block, bound after files
		watch(t)                             // lends t; the block stops and drains t before it closes files (R5)
		"done"
	}
}
```

*Prior art:* Trio nurseries and Swift structured concurrency both guarantee that child tasks end before the scope that started them ends, which is what makes the lend safe; Effect's `forkScoped` ties a fiber to the scope that owns the resources it uses.

## Lending

1. **R16.** Passing a resource as an argument lends it. The caller keeps ownership, and the resource is usable again after the call.
2. **R17.** A lent resource must not be stored, returned, sent or captured by an escaping closure or by `go` inside the callee, except by a structured lend (R27, R31) to a task or owner that the callee releases before it returns. Each of these is an error. A call that lends `r` while `r` is consumed in the same call is an error.
3. **R18.** Lending needs no lifetime annotations, because a lent value cannot escape the call. *Prior art:* Swift non-escaping borrowed parameters; Rust borrows, whose lifetimes Ergo drops because a lend never escapes.
4. A lend of a `Task` lets the callee read it but not `Wait` or `Stop` it.

## Closures

1. **R30.** A non-escaping closure, including a trailing block, lends every resource it captures for the duration of the call it is passed to.
2. **R31.** An escaping closure may capture a resource only under the structured-lend rule: the owner that keeps the closure must be released before the borrow of each captured resource ends, on every exit (R27). The end is the release point of the resource's root owner, or the function's return for an incoming borrow; a resource reached by indexing or through a field has the root owner of its container or aggregate. In `set.Go(func() { actor.Pump(b, signals) })`, the Set keeps the literal, so `set` must be released before `b`: `set` is bound after `b` in the same owner scope.
3. **R32.** While such a closure may still run, consuming the captured resource is an error. `b.Stop()` before `set` is consumed is an error.
4. **R33.** Every other escaping capture of a resource is an error. *Prior art:* Swift does not let an escaping closure capture a `borrowing` parameter of non-copyable type; Rust requires `move` closures to own what they capture, or a lifetime that outlives them.

```go
import queue

func run(signals queue.Queue[Signal]) needs Probe, Clock, Scheduler {
	let b = actor.Start(Breaker{}, 16)
	let set = tasks.Set(tasks.Restart(schedule.Exponential(1s).UpTo(5))) // bound after b
	set.Go(func() { actor.Pump(b, signals) })                            // structured lend of b (R31)
	let d = try b.Ask(Retry{}) catch {
	case ProbeDown:
		Reject{}
	}
} // set stopped, then b stopped
```

The 2026-10-26 sketch bound `set` before `b`; that order is an error under R31, because `b` would stop while the Set could still run the literal.

## Containers

1. **R34.** A slice, array or map whose element type is a resource is itself a resource.
2. **R35.** Copying such a container moves it (R15). Indexing it, `xs[i]`, lends the element; the element's borrow keeps the root owner of `xs` (R27). Calling a consuming method through an index (`xs[i].Close()`, `running[j].Wait()`, `ts[k].Stop()`) is an error.
3. **R36.** A standard-library removal moves the element out: `slices.Take(xs, i)` for a slice, and the map's delete-and-return (`m.Take(k)`). The element then has the caller's scope as owner.
4. **R37.** Releasing a container releases its remaining elements: a slice or array in reverse index order, a map in unspecified order. Each release runs even when an earlier one had a defect (R9).
5. **R40. Resource map keys.** A map whose key type is a resource is an error in v0 (S112, designed). Only map values may be resources. README Gap 32 records a later owned-key rule.

```go
func moves(f File) {
	let xs = []File{f} // f moves into xs (R14)
	let ys = xs        // xs moves into ys (R35)
	xs[0].Close()      // error: xs was moved, and Close through an index is a consume (R35)
	read(ys[0])        // allowed: ys[0] is lent to read
}

func keys() Map[Key, string] {
	return Map[Key, string]{openKey(): "v"} // error: Key is a resource, so it cannot be a map key (R40)
}
```

*Prior art:* Rust's `Vec<T>` forbids moving out by index and moves out through `Vec::remove` and `swap_remove`; Ergo keeps that shape and chooses reverse order for release, to match its scope rule (R5). Rust's `HashMap<K, V>` owns and drops its keys as well as its values; Ergo defers owned keys (R40) rather than add key extraction and replacement rules in v0.

## Channels

1. **R38.** In v0, a channel whose element type is a resource is an error (README Gap 30 records the recommended later rule).

## Partial moves

1. **R39.** Moving a single field out of a struct is an error in v0; move the whole struct. `let p = pg.pool` where `pool` is a resource field is an error; the field may be lent (`ping(pg.pool)`). *Prior art:* Rust allows partial moves and then forbids using the whole; Swift forbids consuming a stored property of a non-copyable value outside its `consuming` methods. v0 takes the stricter rule.

## Early release

1. **R19. Consume.** Calling `r.Close()` consumes `r`. So do `task.Wait()`, `task.Stop()` and an actor's `Stop()`. After a consume, nothing runs for `r` at scope end.
2. **R20.** Using a consumed resource is an error. Consuming it twice is an error.
3. **R21. Paths.** At each point where an owner scope ends, the checker knows for each resource whether it is owned, moved or consumed. A resource moved or consumed on one path and not on another is closed at scope end only on the paths where it is still owned.

## Conversions

1. **R22 (README Gap 14).** Converting a resource to an interface type that has no `Close` is an error. Ownership is never erased by a conversion.
2. **R23.** Converting a resource to an interface type that has `Close` moves it. The interface value is a resource with the same owner scope.

## Context

1. **R24.** A constructor that returns a resource gives its release to the `with` that built it. The build closes its providers in reverse dependency order, dependents first, when the `with` block ends. A capability's `Close` is never called: `Store.Close()` on a capability `Store` whose type has a checked `Close` is an error (S108), because the `with` that built the provider releases it, so a call would be a second release. A capability's method value is refused already ([types.md](types.md), Capabilities 2).
2. **R25.** Provider release runs to completion under interruption (R9), and a release defect joins the exit cause beside the body's outcome.
3. **R26.** A `Task` stored in a provider field is a provider resource. The provider's generated `Close` stops and drains it, as Effect's `forkScoped` does. `Wait` gives its result instead.

### Context ownership as built

Status: **implemented** for providers with a written `Close` (#199 stage 3b; item 8 and inline contexts, stage 3c). A generated `Close` and a provider that is a field or an element of another value wait for #197.

1. **What a `with` acquires.** The checker records, for each `with`, the providers it builds (`With.Acquired`), in the order it builds them. The set is the entries that bind the capabilities the `with` discharges, closed over each provider's uses of other capabilities of its own context. The order puts each provider after the providers it uses; ties keep the context's entry order (contexts by name, then source order). An entry that provides several capabilities is one acquisition. An entry that a `replace` overrides, so that it binds no capability the `with` still needs, and an entry that no acquired provider reaches, are not built and not acquired.
2. **Which acquisition is a resource.** An acquired provider whose type resolves a checked `Close` ([runtime.md, Release masks as built](runtime.md#release-masks-as-built) 1) is a resource. The acquisition records that `Close`; lowering reads it there, never by spelling.
3. **One owner.** The lowering of `with` is the one owner of a `with`'s providers. A `with` that acquires a resource opens a scope of its own on the scope of the code around it (the function's frame, or the next such `with` out). The builder registers each provider's release on that scope in the masked step that builds the provider (item 8). The scope ends when the block ends, on every exit: the normal end, a typed failure that leaves the block, a defect and a stop. On the last three, the enclosing frame's end closes each open `with` scope, innermost first, before its own entries. A provider is never released at the end of its function instead of its `with`, and never twice.
4. **Order.** Releases run in reverse acquisition order (R5), dependents first. A task that the block starts, or receives by a call, belongs to the `with`'s scope, so it is stopped and drained before the providers it borrows close (Release before the end).
5. **Masks and causes.** Each release is a call of the provider's checked `Close`, whose body masks itself (R9). The `with` adds no second mask. It keeps the exit a release raises, runs the releases after it, and joins every cause beside the block's outcome (R25). The normal end does not observe a pending stop; the next interruption point does.
6. **Where a release runs.** A release runs when the block ends, in the environment outside the `with`. Each need of each recorded `Close` is charged at the `with`: an outer `with` of the same target that binds it provides it, else the row of the function that holds the `with` must name it, else S102 at the `with`. The charge widens no row, an inferred one included. A nested `with` restores the outer environment, and its providers close when its own block ends.
7. **Refusals.** Each is located:
   - a resource provider whose freshness the checker cannot prove: S109, #197. A composite literal and a unit value of a type name are fresh; a conversion is fresh only when its operand is fresh. A call is fresh only when its declared function has a fresh result: every path that returns a value must yield a fresh expression, including a call of another fresh function or `try` of that call. Returns in a callee expression or argument count even when the call has a trailing block. A function literal's body and a trailing callback are separate frames; a `with` body runs in its enclosing frame, although an explicit return cannot leave that block (S108). A failure return or failure tail yields no value and does not count; it is decided by the rule the return check uses, so a package error value, a generic one and a rethrown catch binding are failures. The per-function fact is a greatest fixpoint over the call graph, so recursive constructors stay fresh unless a value-return path is not fresh. No extern is trusted: a call of a bodiless extern, in the standard library, its prelude or user code, proves no freshness, because no declaration says what it returns (the prelude's `Delete` returns its argument). Ownership facts for externs are #197's. A parameter, local, package value, field, element, function-value call or operation call is not proof of freshness. More ownership forms wait for #197;
   - an imported context's entries use their defining package's type, Close and constructor facts, including through context embeddings. A resource provider that is not proved fresh, or an entry whose required facts cannot be computed, gets S109 at the importing `with`. Fresh imported constructors pass this check. Lowering still refuses a `with` of an imported context until check-v1 stage B supplies its context;
   - a capability's `Close` (R24): S108;
   - an exit action in the block that needs a capability whose provider is an acquired resource of this `with`, or transitively uses one: it runs when the function returns ([runtime.md, Exit actions](runtime.md#exit-actions) 2), after the `with` released its providers, so S108. A plain provider independent of those resources stays admitted;
   - a `with` whose value holds a `Task`: the block drains the task before its providers close, so the value would leave a drained task (S108);
   - a `with` that owns a resource outside a function body, such as in a package `let`'s value: there is no frame scope to own it, so S108 at the `with` ([types.md, Task ownership](types.md#task-ownership) 4; **implemented**, check-v1 stage E2). Inside a package `let`'s trailing block or function literal it has that block's frame scope;
   - a provider whose `Close` is an operation of its type, is generic, or takes a pointer while the provider is a value: S109 "… is released in lower-v1, not yet", once per entry, at the entry's provider, or at the `with` when the entry is another package's (**implemented**, check-v1 stage E4). The checker refuses it, so lowering never meets it. A `Close` that is an extern cannot occur: no std or prelude `Close` is an extern, and a user extern that takes a record is outside its domain (S109, #202).
8. **Acquisition as one step** (#199 stage 3c, R7 and R8). In a `with` that acquires a resource, the `with` lifetime owner builds every acquired provider through one acquisition step of the runtime (`WithAcquire`, over `runtime/rt`'s `AcquireInto`, the step `rt.Acquire` uses). An inline context's entries take the same step as a named context's, from the same `With.Acquired` fact. The step:
   - begins at an interruption point, so a stop that arrived while an earlier provider was built is observed before the next constructor runs;
   - runs the constructor and the registration of its `Close` masked, as one region; a nested mask of the caller stays in force;
   - registers the release only when the constructor returns a value, also when a stop arrived during the constructor. A typed failure or a defect registers nothing;
   - registers on the `with`'s one release list. There is no second list, mask or count.

   A `with` that acquires no resource builds its providers unmasked, as before: a stop reaches its constructors at their next interruption point.

*Prior art:* Effect's `Layer.scoped` releases a layer's resources when the scope that built it closes, in reverse order, uninterruptibly, keeping each finalizer's failure in the `Cause`. Rust drops struct fields in declaration order and locals in reverse; Ergo takes reverse acquisition order for both. Go programmers write the same shape as one `defer` per constructor in the function that builds the graph.

## `scope.OnExit`

`scope.OnExit() { … }` is for cleanup that is not a release, such as `scope.OnExit() { done.Push(i) }`. `scope.OnExit() { r.Close() }` on a resource the scope owns is an error, because the scope already releases `r`; consume it with `r.Close()` where it should end early. *Prior art:* Zig keeps explicit `defer` and `errdefer` to avoid hidden control flow; Ergo answers that cost with visibility (below). Ergo has no `defer` statement; `defer` is a reserved word whose use names `scope.OnExit` ([grammar](grammar.md#reserved-words)).

## Visibility

1. The language server shows an inlay hint `closes: out, in` at each block end that releases resources.
2. `ergo inspect` lists every inserted release with its source position, and every move and consume.

## Lowering

The emitted Go is the `defer` a careful Go programmer writes, with an exit accumulator so that a release defect is not lost. A loop body becomes its own Go function, or gets an explicit close on each exit edge; both shapes are measured before one is chosen ([#190](https://github.com/cevr/ergo/issues/190)).

## Prior art

- **Rust `Drop` with moves.** The model. Ergo keeps moves and drops lifetimes, because a lent value cannot escape.
- **Swift `~Copyable`.** `consume`, `deinit` and non-escaping borrowed parameters.
- **C# `using var` and Python `with`.** Scoped release with a marker at each binding. Ergo infers it from the type.
- **Gleam `use`.** `use f <- with_file(path)` flattens callback-scoped cleanup; it is explicit, and each resource needs a wrapper function.
- **Elixir/OTP.** A port or file belongs to its process and closes when the process exits. Ergo's per-task scope has the same "the owner's end releases it" rule.
- **Effect.** `acquireRelease`, `Scope`, `addFinalizer`, `ensuring` and `forkScoped`, replaced by one rule.
