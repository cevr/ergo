// Package rt is the runtime that Ergo's emitted sketch code links against.
// It implements the runtime rules of docs/spec/runtime.md (I1 interruption
// points, I2 masks, G1 context inheritance through the task tree, G2 fork-tree
// defects) and the release rules of docs/spec/resources.md (R5, R7, R8, R9,
// R27). It is a kernel package: it has no dependency on an Effect runtime.
// Effect is prior art for the guarantees, not a runtime.
//
// # Model
//
//   - A [Task] is one unit of work in a tree. Each task records its parent, a
//     task ID and a span or definition ID (G2). [Task.Interrupt] requests a
//     stop of the task and every descendant.
//   - [Task.Stopped] is the interruption point (I1): one atomic load while no
//     stop is requested. [Task.Check] is the same point for code with no
//     error row; it panics with [Interrupted], which the nearest task
//     boundary recovers.
//   - [Task.Mask] opens a masked region (I2). While a task is masked a stop is
//     recorded but not observed; the first poll after the outermost unmask
//     observes it. [Mask.Interruptible] is the poll pattern: an explicitly
//     interruptible wait inside a mask. Every release runs masked.
//   - A [Cause] is the exit accumulator. It keeps a body's typed failure, every
//     defect (including a panic in a release) and interruption, in order.
//   - A [Scope] owns resources and tasks. [Acquire] registers a release in the
//     same masked step as the acquisition. [Scope.End] releases in reverse
//     order on success, failure, panic and stop.
//   - [Go] starts a task owned by a scope and returns a [Handle]. The child
//     starts interruptible, even inside a masked region (pinned Effect 4
//     fork default); [GoMasked] is the explicit inherit mode, whose body gets
//     a restore for interruptible sub-regions (ZIO uninterruptibleMask).
//     [Handle.Wait] returns the value or the cause; [Handle.Stop] stops and
//     drains it. A scope stops and drains an unconsumed handle before it
//     releases the resources acquired earlier, which is what makes a
//     structured lend safe.
//   - [Run] is the task boundary shared by every child and the root: emitted
//     main runs its body through it and exits with [ExitCode]. It and
//     [Scope.End] apply stop-wins: a stop the task can observe beats success.
//   - Mask regions are an explicit owner-local stack of kinds: protecting
//     (a release, a defer body, a GoMasked body), acquiring (Acquire's
//     registration step) and restores. A restore is its own kind and returns
//     to the state of the region enclosing the restored one (ZIO and Effect
//     restore): it records, when pushed, whether that outside is masked and
//     whether it shields. A walk down the stack skips acquiring entries; a
//     child is shielded when the first other entry is protecting, or is a
//     restore whose outside shields. An empty stack does not shield. So a
//     restore under an ordinary Acquire starts cancellable children, and a
//     restore under an Acquire inside a cleanup keeps the cleanup's shield,
//     since a finalizer's enclosing state is uninterruptible. The ambient
//     stop cascade does not reach a shielded child, so the release
//     completes (Trio shielded cancel scopes, Effect
//     uninterruptible finalizers, Go context.WithoutCancel); its handle's
//     Stop, a timeout and its scope's drain still stop it. An acquisition
//     masks only its own registration window; it is not a cleanup region
//     and does not hide one. The stack has no depth limit.
//   - A finished task drops its atomic parent link, so ancestry retention is
//     bounded (G2); a defect keeps its chain as at most 32 copied frames.
//   - An exit crosses code with no error row only inside the package's own
//     transport; any other panic value, a *Cause included, is a defect.
//   - Tracing is off unless a root is made with [TracedRoot]; every task below
//     it inherits the [Tracer]. The runtime raises the task events of the runtime laws'
//     vocabulary (start, body-exit, cancel-request, observe, deadline,
//     release, panic) keyed by the task's span, and emitted code raises call,
//     call-exit, interrupt, construct and handler events at its own sites
//     through [Task.TraceCall], [Task.TraceCallExit] and [Task.Trace]. Each
//     event has an occurrence ID, and the runtime records the links between
//     occurrences (a handler to the failure it caught, each reason of an
//     exit to its own origin), so a reader never infers them. Every unwind
//     closes the capability calls it leaves. An [Event] holds the exit's
//     error and the panic value, and a [Tracer] receives them; the rttrace
//     writer serializes only IDs, sites and type names, never values. With
//     no tracer each emission point is one load and compare, the poll has
//     none, and the off path allocates nothing; the recorder and the JSON
//     Lines writer live in package rttrace.
//   - [Clock] and [Scheduler] are the time capabilities, with a [Live] and a
//     deterministic [Virtual] implementation. [After] is the timeout, written
//     as an ordinary library function over [Go]-style tasks and a [Scheduler].
//
// # Errors
//
// Every error the package returns is canonical: nil on success, a lone typed
// failure as itself, [Interrupted] for an interruption alone, and a [*Cause]
// otherwise. [Failure] extracts a lone typed failure for a catch; a defect or an
// interruption never matches one (docs/spec/rows.md, catch rule 6).
//
// # Prior art
//
//   - Go context: the cancellation tree. A stop flows down the tree as
//     context.WithCancel does, but the poll is one atomic load instead of a
//     select on ctx.Done(), and Stop waits for the work to finish.
//   - Effect Fiber, Scope and Cause: the guarantees. Release runs once on every
//     exit, interruption is not a typed error, and a cause keeps every reason
//     instead of the first.
//   - Trio nurseries and Swift task groups: structured lend. A child ends
//     before the scope that started it, so it may use that scope's resources.
//   - Erlang/OTP links: defect propagation. A child's crash reaches the parent
//     at the join with the chain of tasks that led to it.
//   - Tokio cooperative budget: the poll pattern. Interruption is cooperative
//     and checked at points the compiler inserts, not forced by preemption.
//   - Cats Effect MonadCancel and ZIO: masks nest and a restore returns to the
//     outer region's state. Effect 4 forks interruptible by default and
//     inherits only on request, which GoMasked provides.
package rt
