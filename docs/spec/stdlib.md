# Standard library outline

Status: **designed, not yet implemented**, except the prelude's `Console` (`Println`) and the `Sleep` of its live scheduler, which `ergo build` lowers ([runtime.md](runtime.md#host-and-lowering-notes)). `ergo build` lowers `timeout.After` to its `runtime/v1` body, which takes its deadline from the program's `Scheduler` ([runtime.md](runtime.md#timeouts); `TestTimeoutAfterLowers`, `TestTimeoutDeadlineIsTheProgramScheduler`). The four v1 packages `task`, `scope`, `queue` and `slice` have Ergo signatures, which the checker checks like user code, bound to Go bodies in `runtime/v1` ([v1 packages](#v1-packages-task-scope-queue-and-slice)); `ergo build` lowers their calls (check-v1 stage D3b; `TestTaskGoAndTaskResults`, `TestErgoRunQueueOperations`, `TestErgoRunSliceWithCapacity`). The context packages `trace` and `log` lower too (#199 stage 4a; [Context values](#v1-packages-task-scope-queue-and-slice)).

The standard library is the "Effect for Go" track, [#206](https://github.com/cevr/ergo/issues/206). Its module map is the contract: each family states the Effect modules it covers, the construct it uses and its tier. Modules are restated in Ergo's idioms, not ported API for API. Effect is prior art for the guarantees, not a dependency (owner, 2026-10-26). Go names are kept wherever Go has one. Definitions are in the [glossary](../../GLOSSARY.md).

**Tiers.**
- **kernel**: a checker or runtime rule the module needs.
- **library**: ordinary Ergo code over the kernel, with no compiler branch keyed on its identity.
- **host**: a host package, the only code that calls Go.

## Module map

| Family | Ticket | Effect counterpart | Construct or package | Tier |
| --- | --- | --- | --- | --- |
| Tasks and supervision | [#207](https://github.com/cevr/ergo/issues/207) | Fiber, `Effect.all`/`forEach` concurrency, `race`/`raceAll`, FiberSet, FiberMap, FiberHandle, Supervisor | `task.Go`, `Task`, `tasks.Map`, `tasks.Set`, `tasks.Race` | kernel + library |
| Scope and resources | [#65](https://github.com/cevr/ergo/issues/65) | Scope, acquireRelease, ensuring, onExit, addFinalizer, Layer.scoped, forkScoped | automatic release ([resources.md](resources.md)); `build.Scope` for dynamic registration (README Gap 13) | kernel |
| Schedule, retry and timeout | [#179](https://github.com/cevr/ergo/issues/179) | Schedule, retry, repeat, timeout, Clock.sleep, Duration | `schedule`, `retry.On`, `timeout.After(d) { }`, duration literals | library |
| Channels, queues and streams | [#64](https://github.com/cevr/ergo/issues/64) | Queue, PubSub, Mailbox, Stream, Sink, Channel | `queue.Queue`, `for … in`, iterators that are resources | kernel + library |
| Schema and codecs | [#49](https://github.com/cevr/ergo/issues/49) | Schema, Arbitrary | `ParseX`/`Wire` by name, `json.Codec` | library + coherence rule |
| Sync and STM | [#208](https://github.com/cevr/ergo/issues/208) | Ref, SynchronizedRef, Deferred, Semaphore, Latch, TxRef, TxQueue, TxSemaphore | `sync.Ref`, `sync.Atomically() { }`, `sync.Tx` | kernel + library |
| Batching, caching and pools | [#68](https://github.com/cevr/ergo/issues/68) | Request, RequestResolver, Cache, ScopedCache, Pool, KeyedPool, RcRef, RcMap, ScopedRef | `batch.Resolve` in `context` | library |
| Config | [#67](https://github.com/cevr/ergo/issues/67) | Config, ConfigProvider, Redacted | `config`, `Redacted[T]` | library |
| Observability | [#69](https://github.com/cevr/ergo/issues/69) | Tracer, Logger, Metric, LogLevel, References | `with trace.Span(…)`, `log`, `metric` | library on kernel G1/G2 ([#200](https://github.com/cevr/ergo/issues/200)) |
| Clock, random and testing | [#209](https://github.com/cevr/ergo/issues/209) | Clock, Random, Console, TestClock, TestServices, TestContext, `it.effect` | prelude capabilities, `testing.VirtualClock` in `context` | kernel seams + library |
| HTTP | [#51](https://github.com/cevr/ergo/issues/51) | HttpClient, HttpServer, HttpRouter, HttpApi | `func(http.Request) http.Response`, total `catch` | library + host |
| RPC | [#53](https://github.com/cevr/ergo/issues/53) | Rpc, RpcGroup | over `http` and `json` | library |
| HTML | [#212](https://github.com/cevr/ergo/issues/212) | (none; Lustre and Elm prior art) | `html` functions | library |
| Actors | [#57](https://github.com/cevr/ergo/issues/57) | cluster Entity (OTP, Gleam and Akka Typed prior art) | `actor.Start`, `actor.Ask` with an `actor.Reply[T, E]` in the message, `actor.Address[M]`, `actor.StartMachine(m, n)`, `actor.StartAgent` and `actor.Call` with a function on a lent `*S` ([#101 note](../research/actor-library.md)) | library: no compiler rule names it (#101) |
| Host packages | [#210](https://github.com/cevr/ergo/issues/210) | platform FileSystem, Path, Command, Socket; sql | `os`, `fs`, `net`, `sql` with cancel hooks | host |
| Core data | [#211](https://github.com/cevr/ergo/issues/211) | Option, Array, Record, HashMap, HashSet, Chunk, Order, Equivalence, Equal, Hash, Data | prelude `Option`, `slices`, `maps`, `cmp` | library + built-in constraints |

Out of scope for now: durable workflows.

## Prelude

The prelude is in scope in every package without an import: `Option`, `Range`, `Seq`, `Task`, `Decision`, `Clock`, `Scheduler`, `Random`, `Console` and `Ambient`. `Ambient` is the authority to choose the host, which only the entry provides ([Host packages](#host-packages)). Its functions on slice and Map types, `Append`, `Get` and `Delete` (below, Slices and Maps), are in no scope: they are reached only by the dot form, `xs.Append(x)`, so a package may declare its own function of the same name (check-v1 D-D6, D-D8). The same holds for a prelude function on a prelude type, which is that type's operation (196m): `Sleep` is `LiveScheduler`'s, reached as `LiveScheduler.Sleep(d)` or through a context that binds `Scheduler: LiveScheduler`. A bare `Sleep(…)` is S104 naming the dot form, and a program may declare its own Scheduler provider with its own `func Sleep(s Instant, d duration)` (checker-fixes prelude-scope S1). `check.IsPredeclared` reports the names the prelude and the universe predeclare, for tools such as bind-c. The four capabilities are ordinary interfaces bound by `context` (decision #102), so a test replaces any of them ([#209](https://github.com/cevr/ergo/issues/209)).

## v1 packages: task, scope, queue and slice

Language v1 ([#196](https://github.com/cevr/ergo/issues/196)) has four small std packages beside the prelude. A file imports each one it uses; none is ambient (parse-v1 ruling 6). Each pairs ordinary Ergo signatures with a Go body in `runtime/v1`, bound by the identity of the declaration it implements, never by its target text ([runtime.md](runtime.md#host-and-lowering-notes), Extern bodies). The signatures are `extern go` declarations in `internal/sketch/std/task`, `scope`, `queue` and `slice`; the checker checks them, and a program that imports them, with the rules of user code. Their calls lower (check-v1 stage D3b), and their waits take the caller's task (#199 stage 2).

| Package | Signatures | Contract | Go body |
| --- | --- | --- | --- |
| `task` | `func Go[T, E, R](body func() T fails E needs R) Task[T, E] needs R`; the prelude's `Task[T, E]` has `Wait() T fails E` and `Stop()` | [runtime.md, Tasks](runtime.md#tasks) rules 7 to 12 | `Go(s *Scope, span Site, body func(*rt.Task) (T, error)) *Task[T]`; the methods `Wait`, `Stop` and `Move(dest *Scope)`, the return of a Task, which only the lowering calls (task.go) |
| `scope` | `func OnExit[R](body func() needs R) needs R` | [runtime.md, Exit actions](runtime.md#exit-actions) | `OnExit(s *Scope, body func())` (task.go) |
| `queue` | `type Closed {}`; `type Queue[T] { Push(v T) fails Closed, Pop() T fails Closed, Close(), Values() Seq[T] }`; `func New[T](capacity int) Queue[T]` | [runtime.md, Queues](runtime.md#queues) | `New(capacity int) *Queue[T]`; the methods `Push(t *rt.Task, v T) error`, `Pop(t *rt.Task) (T, error)`, `Close()`, and `Values(t *rt.Task) iter.Seq[T]`; `Closed` (queue.go) |
| `slice` | `func WithCapacity[T](n int) []T` | below | `WithCapacity(n int) []T` (data.go) |

**`Values`.** `q.Values()` returns the prelude's `Seq[T]` (lower-v1 Q1; [types.md, Loops and len](types.md#loops-and-len)): `for v in q.Values()` pops each value in turn, waiting like `Pop`, and ends when the queue is closed and empty; a `break` leaves the values not yet popped in the queue. Its Go body returns Go's `iter.Seq[T]`. A `for` over the queue is never an eagerly collected slice. The call evaluates its receiver once, where the Seq is made, and returns a Seq that calls the Go `Values` body each time it is iterated, with the task of the code that iterates it, so a stop of the iterating task ends a wait inside the loop, also when the Seq was made in another task (check-v1 stage D3b).

**Hidden arguments.** Some Go bodies need a value that the Ergo signature does not show and the lowering supplies at the call. The Go body names each one by its Go type, which no Ergo type lowers to, so the body's own signature is the binding; there is no attribute in the Ergo source:

1. `*Scope` is the **frame**: the frame scope of the innermost source function at the call ([Exit actions](runtime.md#exit-actions) 5). In an exit action's block, it is the scope of the function that registered the action; the scope itself routes to the action's frame. `task.Go` and `scope.OnExit` take it, and so does every function whose result holds a `Task`, as its hidden destination ([Tasks](runtime.md#tasks) rule 8): its return is `Task.Move(dest)`.
2. `Site` is the **site**: the call's Ergo identity, the calling function, the called declaration and the call's source position, such as `main calls task.Go at main.ergo:12:10`, the form of a call frame in a defect report. `task.Go` names the task by it, which a defect's task chain shows ([G2](runtime.md#g2-fork-tree-defects); [#230](https://github.com/cevr/ergo/issues/230)).
3. `*rt.Task` is the **task**: the task that runs the call. `Push`, `Pop` and `Values` take it, because a stop of that task ends their wait ([Queues](runtime.md#queues) rule 8). `ContextBound` and `ContextValue`, the bodies of the context reads in `trace` and `log` (below), take it because they read that task's context values.
4. `Sleeper` is the **scheduler**: the `Scheduler` capability of the caller's environment, which is the provider the program bound (`env.Scheduler`). `Sleeper` is a `runtime/v1` interface with the one method `Sleep(*rt.Task, time.Duration)`, so the program's own `Scheduler` interface satisfies it. Lowering supplies it to `After`, whose Ergo signature needs `Scheduler`, so the checker has bound it at the call. A body that takes it and whose signature needs no `Scheduler` is an internal compiler error (`TestStdExternsHaveBodies`). The deadline is time, and time is a capability (Q-S4-7): a test `Scheduler` controls it, as Effect's `TestClock` drives `Effect.timeout` and a Go test injects a clock.
5. `ContextPackage` is the **reading package**: the namespace of the package that declares the context-read extern, `std:` and its path (`std:log`; [runtime.md, G1](runtime.md#g1-context-through-with) 3). Lowering supplies it to `ContextBound` and `ContextValue`. Each binding is stored under the pair of its type's declaring package namespace and its protocol key, so a package reads only its own bindings. Std source does not spell a package prefix.

The hidden parameters come first, in this order, each at most once; a hidden type after an ordinary parameter breaks the convention. The parameters after them stand for the Ergo parameters, one each, in order. The Go results are the Ergo results, then `error` when the Ergo signature has an error row. A function parameter is a Go function under the same convention: the block of `task.Go` is `func(*rt.Task) (T, error)`, which receives the child's task, and the lowering adapts its function value to that shape (the block's providers are captured, never passed). An operation of a type that a std extern returns (`queue.Queue[T]`, the prelude's `Task[T, E]`) is the method of that name on the Go type the extern's body returns (`*Queue[T]`, `*Task[T]`), under the same convention, and the lowering calls that method directly (Types 3).

The lowering supplies these values from the caller's frame and the checked declaration:

1. **Every impure function takes the environment.** Every lowered function, method, function literal, function value, context builder and package initializer takes a hidden `env *Env` first, except a pure function; the environment holds one field per capability and the running task (`env.task`, a `*rt.Task`). `main` gets its task from `v1.Main`, and a `task.Go` block runs with a copy of the caller's environment that holds the child task. A pure function of the program (no `fails`, no `needs`, no witnessed type parameter, no `Task` result, an empty footprint, and only calls of pure functions, `len` and conversions; lower-v1 stage 5) takes no environment and is called with none, so `func Fold(n int) int` lowers to Go's `func Fold(n int) int`. Its function value keeps the one Go shape and discards the environment. No source name changes purity; the exception is decided from the Go method that the lowering emits. The host protocols are `Error() string`, `String() string`, `GoString() string` and every canonical method of go vet's stdmethods check (`ReadByte() (byte, error)`, `Seek(int64, int) (int64, error)` and the others). A pure function that lowers to a Go method whose name and Go signature meet a protocol exactly keeps the environment, so its type never joins the protocol by accident. A pure function that lowers to a Go method with a protocol's name and another signature stays pure, and its Go method name gets an underscore (`Seek_`, one more while a field or another function on the type has that name, as a renamed `Error` field does). A plain function, `func String(n int) string` included, is pure by the rule above. Kotlin draws the same line: it passes its hidden continuation only to `suspend` functions.
2. **A frame scope where one is needed.** A function that calls `task.Go`, `scope.OnExit` or a function whose result is a `Task` opens `fs := v1.NewScope(env.task)` and `defer fs.End(&err)` (`fs.End(nil)` when the function cannot fail), and passes `&fs` as the frame. The checker records per call whether the result holds a `Task` (`Info.Calls[x].TaskResult`, from the one owner of Task containment), for direct calls, calls through function values and function literals; the lowering never derives it again. A function whose result is a `Task` takes the caller's scope as a hidden destination after the environment, and its return is `v.Move(dest)`. A result that holds a `Task` inside another type (`Option[Task[T, E]]`) is refused with S109 naming check-v1 stage E, and so is a `Task` as a type argument.
3. **The site** of `task.Go` is `FUNC calls task.Go at FILE:LINE:COL`: the source function that makes the call, as the call table names it, and the position of the call.
4. **The task** of `Push`, `Pop` and `Values` is `env.task`.
5. **A hidden task is bound where the blocking happens, never where a value is made.** A std value that defers a runtime call takes its task from the environment it is called with, and evaluates its receiver and arguments once, where it is made, in source order:
   - The Seq of `q.Values()` calls `Values` with the iterating environment (above).
   - A method value of an operation, `q.Push` or `t.Wait` of a std operation type or `s.Area` of the program's operation type, is a function value of the uniform shape: it takes the caller's environment first, and calls the operation with that environment's task. One lowering writes every operation call, direct or through a method value. The checker still refuses `t.Wait` and `t.Stop` as values (a Task rule, [runtime.md, Tasks](runtime.md#tasks)), and `task.Go` and `scope.OnExit` as values: a generic function is only called or instantiated, and an explicit instantiation used as a value is S109 naming check-v1 stage E.
   - The block of `task.Go` is bound once, where `task.Go` is called: `let mut f = one; task.Go(f); f = two` runs `one`.
6. **A block with no value.** A type argument that stands for no value ([types.md, Generics](types.md#generics) 6) lowers to `struct{}`: `task.Go() { work() }` is a `*v1.Task[struct{}]`, and `t.Wait()` discards the `struct{}`. A declared generic function whose `T` is no value has its `func(…) T` parameters adapted to return `struct{}{}`, and its `T` result discarded. A `T` that is no value in any other position, such as the result of a nested function type or a type argument of a declared generic type, is refused with S109 naming check-v1 stage E.

`timeout.After` lowers to `v1.After(site, task, scheduler, d, body)` (lower-v1 stage 4, Q-S4-7). Its deadline is a second child of the caller's task that calls `Sleep(d)` on the scheduler argument; `rt.AfterSpan` races it against the body and drains both before it returns. So the deadline is the time the program's `Scheduler` gives: a test `Scheduler` decides when it passes, and the timeout test needs no real sleep. `v1.After` arms no host timer of its own. Its failure is `v1.Expired`, the `runtime/v1` struct that `timeout.Expired` binds (Types 4 below). A std extern that is neither an intrinsic nor a `runtime/v1` body is an internal compiler error, because `TestStdExternsHaveBodies` proves that none exists. `v1.BodyOf` (runtime/v1/files.go, compiler only) reads a body's shape from the embedded source, as the lowering reads it; `TestBodiesFollowTheHiddenConvention` pins each body's shape, and the test of `internal/sketch/std` ties each extern and operation to its body. *Prior art:* Gleam's `@external(erlang, "m", "f")` and ReScript's `external f: t = "f"` bind a host function by name with the signature the source states and pass nothing hidden; Go's own `iter.Seq` carries its stop through `yield`; Effect passes its fiber context implicitly through the runtime; Kotlin passes a hidden `Continuation` as the last parameter of a `suspend` function, the closest relative of the hidden task here.

**Types.** Each Ergo type in a std signature has one Go type, which the body writes and the lowering must use:

1. A basic type is its Go type (`int` is `int`), a type parameter keeps its name, and `[]T` is a Go slice.
2. A row parameter (`E`, `R` in `task.Go[T, E, R]`) has no Go type parameter: rows are erased (rows.md), so the body is `Go[T any]`, and `Task[T, E]` is `*Task[T]`. The type parameters left after erasure are the Go ones, with the same names in order and the same constraint (none or `any` is `any`, `comparable` is `comparable`), so the checker admits a type argument exactly when the Go call compiles: every v1 body takes `T any`, and `slice.WithCapacity[[]int](3)` is valid on both sides. An operation takes the type parameters of its type (`Queue[T]`, `Task[T, E]`). An error row is the last Go result `error`; a capability row is captured by the lowered block, never passed.
3. An operations type that a std extern returns (`queue.Queue[T]`, the prelude's `Task[T, E]`) is the runtime type that the extern's body returns, `*v1.Queue[T]` or `*v1.Task[T]`; the lowering emits no interface for it. A returned Task needs the concrete `*v1.Task[T]` to `Move`, and `Close`, `Wait` and `Stop` take no task, so one emitted shape would need an adapter per type. A value of the program converted to a std operations type is refused with S109 naming check-v1 stage E; a type defined over one and a pointer to one are S108 (types.md, Defined types 1 and Operation types).
4. A record type declared in a v1 package, which is not an operations type, is the exported `runtime/v1` struct type of the same name, with the same fields: `queue.Closed` is `v1.Closed`. The lowering never generates a struct for it, because `Push` and `Pop` return `v1.Closed{}`, and a `catch` arm `case queue.Closed:` must match that value. An error type's Go text is its Ergo name: `v1.Closed{}.Error()` is `queue.Closed`.

The std test compares each Ergo type parameter, parameter and result type with its body's Go one under these rules (`TestEachExternHasItsGoBody`), and ties each record of a v1 package to its Go struct and text (`TestStdTypesBindRuntimeTypes`, through `v1.StructFields`).

**Slices and Maps.** `slice.WithCapacity[T](n)` is an empty slice with room for `n` elements; it makes no element, and a negative `n` is a defect, as Go's `make` panics. There is no `slice.Repeat`: not every type has a zero value (parse-v1 ruling 7). The prelude's `xs.Append(x)` returns the slice with `x` at the end, `m.Get(k)` returns `Option[V]` and `m.Delete(k)` returns the Map without `k` (check-v1 D-D6). They are prelude functions, `func Append[T](xs []T, x T) []T`, `func Get[K comparable, V](m Map[K, V], k K) Option[V]` and `func Delete[K comparable, V](m Map[K, V], k K) Map[K, V]`, reached by the dot form of a function on a type ([types.md, Methods](types.md#methods)) from a value whose type is exactly an unnamed slice or Map type; the bare call `Append(xs, x)` is S104 naming `xs.Append(x)`. `Append` takes one element. A defined type over a slice reaches `Append` after assignment ([types.md, Assignability](types.md#assignability) clause 2): `let raw []int = xs`, then `raw.Append(x)`; a defined Map uses `let raw Map[string, int] = m`, then `raw.Get(k)` or `raw.Delete(k)` (check-v1 D-D8). `Append` and `Delete` have Go bodies in data.go. `Get` builds the program's own `Option`, so its body is lowered at the call. A slice and a Map alias as in Go in v1: `Append` may share the array, and `Delete` changes the Map it is given ([#222](https://github.com/cevr/ergo/issues/222), [#197](https://github.com/cevr/ergo/issues/197)). *Prior art:* Go `make([]T, 0, n)` and `append`; Gleam's `list` module and Elixir's `Enum` keep collection functions out of the prelude.

**Context values: trace and log.** Two packages make and read the context values of [runtime.md, G1](runtime.md#g1-context-through-with) (#199 stage 4a). They are the first std packages with Ergo bodies; the lowering emits those bodies as it emits a user package's.

| Package | Exported declarations | Key | Go body of the read |
| --- | --- | --- | --- |
| `trace` | `func Span(name string) span`, the value `with trace.Span("checkout") { … }` binds; `func Current() Option[string]`, the innermost span's name or `None` | (`trace`, `span`) | `ContextBound(t *rt.Task, pkg ContextPackage, key string) bool` and `ContextValue(t *rt.Task, pkg ContextPackage, key string) string` (context.go), through unexported externs |
| `log` | `func Annotate(key string, text string) annotation`; `func Annotation(key string) Option[string]` | (`log`, the annotation's key) | the same |

Each value's type (`span`, `annotation`) is an unexported record with the protocol's unexported functions `contextKey` and `contextText` on it, so it binds by the rule every type uses and the packages export only the four declarations above. A read has empty rows and is effectful ([rows.md, Footprints](rows.md#footprints)). The rest of the trace and log APIs is [#206](https://github.com/cevr/ergo/issues/206). The lowering opens and ends a region with `Scope.OpenContext(pkg, key, text string) *Context` and `Context.End()` (context.go), which only the lowering calls. *Prior art:* OpenTelemetry's `Tracer.start_as_current_span` and Elixir's `Logger.metadata` set a value for the running process; Java's `ScopedValue` scopes it to a block, as `with` does.

## strings and strconv

Std `strings` and `strconv` work on text (wave std-1, unit G5a). They are pure functions: no operation type and no capability. Each export is an `extern go` of a `runtime/v1` body in `text.go` that calls the Go function of the same name, under the convention of [v1 packages](#v1-packages-task-scope-queue-and-slice): no hidden argument, a `runtime/v1` Go name with the std package as prefix (`StringsSplit`, `StrconvAtoi`), because the lowering keys its runtime bindings by bare name, and one `runtimeBindings` row each, which neither waits nor releases. `TestStdTextRuns` runs each function.

| Package | Signatures |
| --- | --- |
| `strings` | `Split(s, sep string) []string`, `Join(xs []string, sep string) string`, `HasPrefix`, `HasSuffix`, `Contains(s, x string) bool`, `TrimSpace(s string) string`, `TrimPrefix`, `TrimSuffix(s, x string) string`, `Fields(s string) []string`, `Repeat(s string, n int) string`, `ToLower`, `ToUpper(s string) string`, `Index(s, sub string) int`, `Slice(s string, i int, j int) string`; `type Halves { Before string, After string }`, `func Cut(s, sep string) Option[Halves]` |
| `strconv` | `type Malformed { Text string }`; `Itoa(n int) string`, `Atoi(s string) int fails Malformed`, `Quote(s string) string` |

1. Lengths and indexes are bytes, as `len(s)` is. Ergo has no substring syntax yet (`s[i..j]` is an index by a `Range`, S108), so `strings.Slice(s, i, j)` is Go's `s[i:j]`. A bound outside `0..len(s)`, or an `i` after `j`, is a defect, as an index out of range is; so is a negative `n` in `Repeat`.
2. `Cut` is Ergo over `Index` and `Slice`: `Some` of the text before and after the first `sep`, or `None` when `s` has no `sep`. A `sep` at index 0 gives an empty `Before`.
3. `Atoi` fails `Malformed` with the text for every text Go's `strconv.Atoi` refuses, a value out of `int`'s range included. `Malformed` is the `runtime/v1` struct `v1.Malformed` (Types 4), so `case strconv.Malformed:` matches the value the body returns. Its text is `strconv.Malformed{Text:…}`, the generated text of a record.
4. Each call is transparent in footprints: every body in `text.go` is marked pure ([Pure bodies](#pure-bodies)).
5. `slice.SortFunc` and `SortStableFunc` are not here: a std function parameter with parameters of its own does not bind yet (`lower/std.go`, `adaptFunc`); they are follow-up G5c.

*Prior art:* Gleam's `gleam/string` and ReScript's `Js.String2` bind host text functions as direct externals with no wrapper; Elixir's `String` and `Integer.parse/1` (`{n, rest} | :error`) are plain BEAM calls. Ergo binds Go's `strings` and `strconv` the same way, and gives `Atoi` a typed failure in its row in place of Go's `error`.

## Pure bodies

A std extern whose `runtime/v1` Go body is marked pure is transparent in footprints (wave std-1, unit G5b; [rows.md, Footprints](rows.md#footprints)). A call of it adds no capability, no task start and no opacity, so a function that calls `strings.TrimSpace` has the footprint of one that calls nothing: empty, with no entry poll. Every other extern call stays opaque.

1. The mark is the directive line `//ergo:pure` in the doc comment of an exported top-level function of `runtime/v1`. `v1.BodyOf` reads it and reports `Body.Pure`; the checker's footprint (`check/footprint.go`, `runs`) reads only that fact.
2. The mark is a reviewed claim. `BodyOf` refuses a marked body, with an error that names the rule, when:
   - it is a method;
   - its file imports a package outside `v1.PureImports`: `strings`, `strconv`, `unicode/utf8`, `slices`, `sort`, `math`;
   - it takes a hidden argument (the frame, the call site, the task, the Scheduler, the reading package);
   - it takes a function, whose code is unknown;
   - its body names a top-level function or variable of `runtime/v1` (so it reaches no other body and no package state), starts a goroutine or uses a channel. A local name that shadows a top-level one is refused too.
   For a refused body `BodyOf` returns no shape, so the checker treats its call as opaque. A body that is not marked is not pure.
3. Purity is only std's. A user `extern go strings.TrimSpace` stays opaque: its Go body is outside the reviewed set.
4. An Ergo std function over pure externs, such as `strings.Cut`, is pure through its own footprint, which its package records.
5. Today the marked bodies are the bodies of `strings` and `strconv` in `text.go`. `TestPureBodiesKeepTheAllowlist` reads every embedded runtime file and checks each marked body against the rules independently of `BodyOf`; `TestFootprintsPureStd` checks the footprints.

*Prior art:* GHC trusts a foreign import's declared type: an import whose result is not in `IO` is pure on the author's word. Gleam's and ReScript's externals carry no effect fact, and Elixir's compiler sees a remote call as unknown code. Koka infers effects only for code it can see, and its externs state their effect in the declared type. Ergo keeps the claim on the Go body, where a reviewer reads it, and checks its mechanical part: what the file can reach.

## Packages by origin

- **Same as Go:** `strings`, `slices`, `maps`, `cmp`, `math`, `time`, `os`, `fs`, `net`, `sql`.
- **Same name, extended:**
  - `http`: client, handlers, routes, server limits;
  - `json`: `Codec`, `Decode`, `Encode`, `ParseX`/`Wire`;
  - `sync`: `Ref`, `TxRef`, `Atomically`, `Latch`;
  - `testing`: `T`, `VirtualClock`, `Discard`, `Idle`.
- **New:** `tasks`, `schedule` (the schedule algebra, with laws), `retry`, `timeout`, `trace`, `log`, `metric`, `batch`, `actor`, `html`.

## Tasks

[#207](https://github.com/cevr/ergo/issues/207). `tasks.Map(xs, limit, f)` keeps input order and runs at most `limit` calls at once; `limit >= 1` is a precondition. It waits for whichever task ends first, so a failure stops the rest promptly. It collects into `[]Option[B]` slots. `tasks.Race` is n-ary. `tasks.Set` is G4 ([runtime.md](runtime.md#g4-tasksset)). The race and fail-fast laws pass as tests. *Prior art:* Go `errgroup` with `SetLimit`; Effect `Effect.forEach` with `concurrency`; Elixir `Task.async_stream` keeps order with `ordered: true`.

## Sync and STM

[#208](https://github.com/cevr/ergo/issues/208).

1. `sync.Ref`, `Latch`, `Semaphore` and `Deferred`. A blocking wait is an interruption point.
2. `sync.TxRef[T]`, `sync.Atomically() { … }` and `sync.Retry()`. `Load` and `Store` need the `sync.Tx` capability, which only `Atomically` provides.
3. A transaction can re-run, so its body is a replayable parameter ([types.md](types.md#function-values)): its footprint holds only `sync.Tx`, and I/O inside a transaction is an error.
4. `Retry` rolls back, registers the read set and waits, interruptibly, until a `TxRef` it read changes. No lock-freedom or starvation-freedom is claimed.

*Prior art:* Haskell STM, ZIO STM, Effect `TxRef`; Go offers mutexes only.

## Batching

[#68](https://github.com/cevr/ergo/issues/68). A provider implements `FindMany`, and `context` wraps it: `Users = batch.Resolve(UsersDB, batch.Window(2ms), batch.Max(100))`. Go has no scheduler tick to batch on, so the window is explicit. Duplicate keys share one result. A stopped waiter leaves the batch; the batch is cancelled when no waiter is left. "One query" holds only when every call arrives inside the window. *Prior art:* DataLoader states its scheduling policy; Haxl, ZIO Query and Effect `RequestResolver` batch per round.

## HTTP

[#51](https://github.com/cevr/ergo/issues/51). A handler is an ordinary `func(http.Request) http.Response`. A `catch` without `try` must cover every error, so a handler cannot leave a decode, handler or encode failure without a status. Routes use Go 1.22 `ServeMux` patterns: `"POST /orders": placeOrder`. Tests call `api.Serve(req)` with no server. Declared difference: an unmatched method answers 405, as `ServeMux` does. Open trade: handlers lose codec identity, so `ergo inspect` derives endpoint metadata from signatures and literal statuses in `catch` arms. *Prior art:* Effect `HttpApi` declares endpoints; Go `net/http` handlers; Phoenix controllers.

## HTML

[#212](https://github.com/cevr/ergo/issues/212). Owner decision, 2026-10-26: HTML is "Functions", not markup syntax. There is no markup grammar.

```go
import html

func OrderRow(o Order) html.Node {
	return html.Tr([]html.Child{
		html.Class(statusClass(o.Status)),
		html.Td([]html.Child{html.Text(o.Member.Name)}),
		html.Td([]html.Child{html.Int(o.Quantity.Wire())}),
	})
}
```

1. The `html` package is ordinary functions: elements (`Tr`, `Td`, `P`, …), attributes (`Class`, `Href`, …), `Text`, `Int`, `Each` and `When`. A component is a function that returns `html.Node`.
2. An element takes one slice, `[]Child`, where `Child` is the closed union `Attr | Node`; Ergo has no variadic parameters. A void element such as `Br` or `Img` takes only `[]Attr`, so it cannot get children.
3. Escaping is decided by type. `Text` escapes its string; only an `html.Node` is inserted as markup. `Href` and `Src` take a `url.Safe`, not a string. `Raw` takes an `html.Trusted`, which only `html.Trust` builds, so `ergo inspect` lists every trust site.
4. **Lowering.** `html.Node` is a render function that writes to a buffered writer; `Text` escapes at write time, so no intermediate string is built. Folding static subtrees at compile time is a later, measured optimisation. `http.HTML(status, node)` renders a node into a response.

*Prior art:* Gleam Lustre (`html.td([], [text(name)])`), whose surface Ergo takes; Elm `Html`, which escapes by type with separate attribute and child lists, merged here into one `[]Child` slice of the union, so an element takes one argument; templ (Go), whose render-function representation Ergo takes, not its syntax; Phoenix HEEx (Elixir), whose compile-time validation Ergo gets from closed types instead of a parser.

## Host packages

[#210](https://github.com/cevr/ergo/issues/210). `os`, `fs`, `net` and `sql` are the only standard-library code that calls Go. They are reviewed. Every blocking call has a cancel hook, or is recorded as an uninterruptible host wait that `ergo inspect` lists (G8). Each package states the Go targets it supports. File handles are resources ([resources.md](resources.md)). Through host packages and `import go`, every effect appears in a footprint ([rows.md](rows.md#footprints)).

A host package has five parts: an operation type, the capability a program names in its rows; a record `Host { live hostOnly }`, the host's provider, whose unexported field of a zero-field type gives the package the construction authority and keeps `Host` zero size; one constructor `func NewHost() Host needs Ambient`; the provider's functions, with Ergo bodies; and unexported externs that bind the Go bodies in `runtime/v1` (`host.go`).

**Who chooses the host (host-authority H1).** Only code whose row holds the prelude's `Ambient` makes a host provider, and only the entry provides `Ambient`:
1. Outside its package, `fs.Host` as a value, `fs.Host{}`, `let h fs.Host`, a record update of it, and a type that holds it at zero are S114 "fs.Host has an unexported field, so only package fs builds it; use fs.NewHost" (types.md, Zero values 6). A generator or codec does not build it (`check.Constructible` is false).
2. A call of `fs.NewHost()` charges `Ambient`. Where no row holds it, the call is S102: in an exported function or `main` whose written row does not list it, in a `test`, in a package-level `let` and in a named context ("context Live requires Ambient while it is built"). In an unexported function the need is inferred and reaches its callers, up to `main`. A local context charges its `with`, and that need does not widen an inferred row ([rows.md](rows.md#composition-laws) 9).
3. The entry provides `Console` and `Ambient`: a `main` that lists `Ambient` gets it from the host, and any other capability in `main`'s row is S109 "the host provides only Console and Ambient". `Ambient`'s one operation is unexported, so a program type does not implement it and `with context { Ambient: Fake }` is S106.
4. Delegation stays open: a function that takes a host provider or a capability value as a parameter uses it with no row (GLOSSARY, Interface value). The caller that made it holds `Ambient`.
5. A test cannot get `Ambient`, so it binds its own provider: tests are hermetic. An opt-in for a test that needs the live host is a later unit (H3).
6. The prelude's `LiveScheduler` reaches the host's clock and is not under this rule yet (host-authority-sched, H2). `TestStdHostProvidersHaveAuthority` finds every std record whose function reaches a host extern and fails on one that has no construction authority or whose exported constructor does not list `Ambient`; `LiveScheduler` is its one listed exception.

So `main` reads `func main() needs Console | Ambient { with context { fs.Files: fs.NewHost(), os.Process: os.NewHost() } { run() } }`. The context in `cmd/<name>/main.ergo` is the one place that chooses the host, by convention: a library may wrap the choice when its own row lists `Ambient` (`func RunLive(f func() needs Console | fs.Files) needs Console | Ambient`). A test binds its own provider in the same place (`TestHostAuthority`, `TestHostAuthorityDomain`, lower `TestHostAuthorityRuns`).

### os

Std `os` is the host process (wave std-1, unit G2).

```go
import os

func run() fails os.Exit needs Console | os.Process {
	match os.Process.Getenv("TOPIC") {
	case Some[string] as t:
		Console.Println(t.Value)
	case None:
		return os.Exit{Code: 3, Message: "[NOT_FOUND] no TOPIC"}
	}
}

func main() fails os.Exit needs Console | Ambient {
	with context { os.Process: os.NewHost() } { try run() }
}
```

| Declaration | Meaning |
| --- | --- |
| `type Process { Args() []string, Getenv(key string) Option[string], law GetenvIsStable(key string) }` | The capability. |
| `type Host { live hostOnly }`; `func NewHost() Host needs Ambient`; `func Args(_ Host) []string`; `func Getenv(_ Host, key string) Option[string]` | The host's provider and its one constructor ([Host packages](#host-packages)). |
| `type Exit { Code int, Message string }` | The failure that ends `main` with status `Code`. |

1. `Args` is the arguments after the program name, Go's `os.Args[1:]`.
2. `Getenv` is `None` when `key` is unset and `Some("")` when it is set to the empty string. The law `GetenvIsStable` states `Getenv(key) == Getenv(key)`: two reads of one key agree, for any provider.
3. `os.Exit` is a failure in the row, not a call. When it is `main`'s only reason, the entry writes `Message` and a line feed to standard error, verbatim, and exits with `Code`; `Code` must be in 1..125 ([runtime.md](runtime.md#exit-causes) 8). It never skips cleanup: every scope's exit actions run before the entry writes it. Go's `os.Exit` skips deferred calls; Ergo has no call that does.
4. The Go bodies are `v1.OSArgs`, `v1.OSHasEnv` and `v1.OSGetenv`, each with one `runtimeBindings` row that neither waits nor releases. They read the host process, so none is pure. `os.Exit` is the `runtime/v1` struct `v1.Exit` (Types 4).
5. A program implements `os.Process`. A std operation type is sealed only when the result of a std extern names it, as `queue.New` names `queue.Queue` and `task.Go` names the prelude's `Task` (`check.SealedStd`, S109). No std extern returns `os.Process`, so it is open, as the prelude's `Console` is: `os.NewHost()` and a program's own type with an `Args` and a `Getenv` both convert to it, in `context { os.Process: … }` and at every other site that converts by assignability. It lowers as an interface of the program, not as a `runtime/v1` type. A test binds a memory provider in the same place as `cmd/<name>/main.ergo` binds `os.NewHost()`: `with context { os.Process: FakeEnv{…} } { run() }` (`TestOSProcessRuns`; check `TestStdProtocolOpen`).

*Prior art:* Roc's basic-cli platform gives `Arg` and `Env` as platform effects, and its `main` can fail with `Exit code message`, which the platform writes and exits with; `os.Exit` takes that shape and adds the 1..125 check. Effect Platform reads the environment through a `ConfigProvider` layer and leaves argv to `@effect/cli`. Gleam's `argv` and `envoy` packages, Elixir's `System.argv/0` and `System.get_env/1`, and ReScript's Node bindings read the process directly, with no capability; Elixir's `System.get_env/1` returns `nil` for an unset key, and Gleam's `envoy.get` returns `Result(String, Nil)`. Elixir's `System.halt/1` ends the VM at once with any status; Ergo's `os.Exit` is a failure, so cleanup runs first and the status is checked.

### fs

Std `fs` is the host file system, read only (wave std-1, unit G4). Writes (`WriteFile`, `MkdirAll`) are a later unit, G4b.

```go
import fs
import strings

func title(path string) string needs fs.Files {
	let text = fs.Files.ReadFile(path) catch {
	case fs.NotFound as n:
		return "no file \(n.Path)"
	case fs.Denied, fs.Failed:
		return "unreadable"
	}
	strings.TrimSpace(text)
}

func main() needs Console | Ambient {
	with context { fs.Files: fs.NewHost() } { Console.Println(title("doc.md")) }
}
```

| Declaration | Meaning |
| --- | --- |
| `type Files { ReadFile(path string) string, ReadDir(path string) []string, Stat(path string) Info, law ReadIsRepeatable(path string) }`, each operation `fails NotFound \| Denied \| Failed` | The capability. |
| `type Host { live hostOnly }`; `func NewHost() Host needs Ambient`; `func ReadFile(_ Host, …)`, `func ReadDir(_ Host, …)`, `func Stat(_ Host, …)` | The host's provider and its one constructor ([Host packages](#host-packages)). |
| `type Info { Name string, Size int, Dir bool }` | What `Stat` gives. |
| `type NotFound { Path string }`; `type Denied { Path string }`; `type Failed { Path string, Reason string }` | The failures. |

1. `ReadFile` is the whole text of the file at `path`. It returns `string`, not bytes: the language has no conversion from `[]byte` to `string` yet. A path is the host's; a relative path is relative to the working directory.
2. `ReadDir` is the names of the entries of the directory at `path`, sorted by name (Go's `os.ReadDir` sorts them). `Stat` follows symbolic links, as Go's `os.Stat` does (not `os.Lstat`). A link to a directory has `Dir: true`, and a link whose target is missing is `NotFound`. `Info.Name` is the last element of the path, so for a link it is the link's own name. `Info.Size` is the size in bytes of the target.
3. The failures are a closed row of three. `NotFound` is a path that does not exist (Go's `fs.ErrNotExist`), `Denied` a path the process may not read (`fs.ErrPermission`), and `Failed` every other failure, with the host's `Reason` without the operation and the path, such as `is a directory` for a directory read as a file. A `catch` that lists the three is exhaustive. An uncaught one is written with its generated text and status 1, such as `error: fs.NotFound{Path:missing.md}`.
4. The Go bodies are `v1.FSReadFile`, `v1.FSReadDir` and `v1.FSStat`, each with one `runtimeBindings` row that neither waits nor releases. They read the host, so none is pure. No body keeps a file open after it returns, so this set exposes no handle and needs no resource. The four records are the `runtime/v1` structs `v1.NotFound`, `v1.Denied`, `v1.Failed` and `v1.Info` (Types 4). The lowering binds a record by its bare name, so `runtime/v1` `TestStructHasOneStdRecord` holds that each `runtime/v1` struct is the Go type of at most one std record: a later `exec.NotFound` needs its own Go name first.
5. A program implements `fs.Files`. No std extern returns it, so it is open, as `os.Process` is ([os](#os) 5, `check.SealedStd`). A test binds a memory provider of its own, `type Files { Docs Map[string, string], Names []string }` in a package `memory`, in the same place as `cmd/<name>/main.ergo` binds `fs.NewHost()`: `with context { fs.Files: memory.Files{…} } { run() }` (lower `TestFSFilesMemory`).
6. **Authority.** The externs are unexported, and only package `fs` builds an `fs.Host`: other code gets one from `fs.NewHost()`, which needs `Ambient`, or as a parameter ([Host packages](#host-packages), host-authority H1). So code that reads the disk with no `fs.Files` in its row, by `fs.ReadFile(h, p)`, by passing `h` as an `fs.Files` value, or by its own `with context { fs.Files: h }`, either has `Ambient` in its row (or in an inferred row that reaches `main`) or was given `h` by code that has it. `fs.ReadFile(fs.Host, p)` is S114; the same call on `fs.NewHost()` in a function whose row does not hold `Ambient` is S102. The same holds for `os.Host`.
7. The law `ReadIsRepeatable` states that two reads of one path agree. It holds for a world with no writer: this set has none, so it holds for `fs.Host` on a quiet disk and for any memory provider. A law runner (#162) cannot build `fs.Host` by a generator (`check.Constructible` is false): it builds it through `fs.NewHost()` under `Ambient`, or it skips the host provider and says so.

*Prior art:* Gleam's `simplifile.read(path) -> Result(String, FileError)` gives a closed enum of POSIX reasons (`Enoent`, `Eacces`, …) plus `Unknown`; Effect Platform's `FileSystem` service gives `readFileString` and a `SystemError` whose `reason` is `NotFound`, `PermissionDenied` and others, bound by `NodeContext.layer`; Elixir's `File.read/1` returns `{:ok, binary} | {:error, posix}`; Go's `os.ReadFile` and `os.ReadDir` with `errors.Is(err, fs.ErrNotExist)`; ReScript binds Node's `readFileSync` as an `external` that throws. `NotFound | Denied | Failed` is their common core: the two reasons a program acts on, and one that carries the host's text. Like Effect and Roc, and unlike Gleam, Elixir and ReScript, the file system is a capability in the row, so a test swaps it in a context. Like Roc's platform and unlike Effect's layer, which any module may provide locally, Ergo keeps the disk out of reach of code that neither holds `Ambient` nor was given a provider (rule 6): Pony's `AmbientAuth` and Austral's `RootCapability` are the model.

## Testing

[#209](https://github.com/cevr/ergo/issues/209). `testing.VirtualClock` provides `Clock` and `Scheduler` and jumps to the next timer when every task in the test is blocked. `T.Idle()` reports the tasks that keep a test from reaching idle. The test harness is an ordinary `context`, so production code needs no change to run on virtual time. *Prior art:* Go `testing/synctest` bubbles; Trio `MockClock(autojump_threshold=0)`; Effect `TestClock`.
