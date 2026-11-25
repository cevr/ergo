package rt

import (
	"sync"
	"sync/atomic"
)

// maxChain bounds the ancestry a defect records (G2, rule 3).
const maxChain = 32

var nextID atomic.Uint64

// Task is one unit of work in the task tree. A body receives its *Task and
// polls it at interruption points. The task's own goroutine is the only one
// that masks it, reads its context values or starts children from it; any
// goroutine may interrupt it.
type Task struct {
	// stopped is the stop flag, set by Interrupt from any goroutine, and in
	// the same word the flag that publishes stop (stopFlags).
	stopped stopFlags
	// shielded is fixed before t is linked, and widened only when its parent
	// ends and hands it on (adopt), under both parents' locks: t was started
	// while its owner was inside a masked region, so the owner's ambient stop
	// does not cascade into it. The cascade reads only this field, under the
	// parent's lock, never the owner's mask. It
	// sits beside stopped so that the two share one word.
	shielded bool
	// waker marks a task with an OnInterrupt wake in wakers, guarded by mu;
	// it fills the same word, so the task keeps its size.
	waker bool
	// firing marks a task that runs a machine state's After for a timer
	// that fires (runtime/v1 MachineFiringOn). Only the owner goroutine
	// reads or writes it; it fills the same word.
	firing bool
	// consumed is its Handle's: Wait or Stop took the handle. Only the
	// owner goroutine reads or writes it; it fills the same word, so the
	// Handle keeps its size class.
	consumed bool
	// regions is the stack of open mask regions (regions.go). Only the owner
	// goroutine reads or writes it, so masking costs no atomic operation.
	regions regionStack
	id      uint64
	span    string
	parent  atomic.Pointer[Task] // live link; cleared when t finishes
	values  *values              // G1 context; owner goroutine only
	// trace is the tree's tracer, nil when tracing is off (trace.go). It is
	// fixed before t runs, so any goroutine may read it.
	trace *tracer
	// member is t's place in an auto clock's domain (idle.go), nil when t
	// is in none. It is fixed before t runs, except for the task that
	// makes an auto clock, which sets its own.
	member atomic.Pointer[member]
	// wake is t's park word (park.go): t's goroutine parks on it in a
	// wait on one value, and a waker or a stop request wakes it.
	wake parker
	// waiting is the Waiter of a wait of t that parks on a word of its own
	// (waiter.go), so Interrupt wakes it; nil when none is.
	waiting atomic.Pointer[Waiter]

	mu sync.Mutex
	// stop is made on first use under mu, by the task's Done or by its Go
	// context's Done from any goroutine (gocontext.go), and is never
	// replaced; Interrupt closes it under mu. A read under mu sees it; a
	// read without mu must first see stopped.made (makeStop).
	stop  chan struct{}
	first *Task // first child, guarded by mu

	prev, next *Task // siblings, guarded by parent.mu
}

// Root returns a task with no parent: the task that runs main. span names it
// in defect chains.
func Root(span string) *Task {
	return &Task{id: nextID.Add(1), span: span}
}

// init links t as a child of parent. A child starts interruptible (pinned
// Effect 4: fork is interruptible unless asked to inherit); masked starts it
// inside one masked region, for GoMasked; shield makes it shielded (see
// below) whatever parent's regions are, for GoShielded.
//
// A child is release-owned and shielded when the nearest enclosing masked
// region of parent that is not a restore is a protecting one: a release, a
// defer body or a GoMasked body, but not Acquire's registration step
// (regions.go). The ambient stop that cascades from parent does not reach it, whether the stop came before or
// after the start, so the release runs to completion (I2, R9). Its handle's
// Stop, a timeout and its scope's drain still interrupt it directly. This is
// Trio's shielded cancel scope and Effect's uninterruptible finalizer. Any
// other child starts stopped when parent is stopped.
func (t *Task) init(parent *Task, span string, masked, shield bool) {
	t.id = nextID.Add(1)
	t.span = span
	t.parent.Store(parent)
	t.values = parent.values
	t.trace = parent.trace
	t.shielded = shield || parent.regions.shields()
	t.join(parent)
	if masked {
		t.regions.push(protecting)
	}
	parent.mu.Lock()
	flags := parent.stopped.v.Load()
	if !t.shielded && flags&flagBudget != 0 {
		t.stopped.v.Or(flagBudget | flagPolled)
	}
	if !t.shielded && flags&flagStopped != 0 {
		if t.trace != nil {
			t.traceTask(TraceCancelRequest, nil) // born stopped: the request precedes its start
		}
		t.stopped.set()
	}
	t.next = parent.first
	if t.next != nil {
		t.next.prev = t
	}
	parent.first = t
	if flags&flagParent == 0 {
		parent.stopped.setParent()
	}
	parent.mu.Unlock()
}

// lockParent locks the current parent's child list. Adoption changes the
// link under both parents' locks, so a parent read before locking can be stale.
func (t *Task) lockParent(p *Task) *Task {
	for p != nil {
		p.mu.Lock()
		current := t.parent.Load()
		if current == p {
			return p
		}
		p.mu.Unlock()
		p = current
	}
	return nil
}

// unlink removes t from p's list and hands on its children (Tasks, rule 1).
// The caller holds p.mu and clears t.parent after all of the task's writes.
// Only t's own goroutine uses its context values; a child has its own copy.
func (t *Task) unlink(p *Task) {
	t.values = nil
	prev, next := t.prev, t.next
	if prev != nil {
		prev.next = next
		t.prev = nil
	} else {
		p.first = next
	}
	if next != nil {
		next.prev = prev
		t.next = nil
	}
	if t.stopped.parent() {
		t.adopt(p)
	}
}

// detach drops the links of a finished child that does not have a Handle.
func (t *Task) detach() {
	p := t.lockParent(t.parent.Load())
	t.unlink(p)
	t.parent.Store(nil)
	p.mu.Unlock()
}

// adopt hands t's running children to p, t's parent, whose lock the caller
// holds; locks are taken ancestor first, as Interrupt takes them. A child
// keeps what p's stop did to it through t: it is shielded when t was (the
// cascade reads shielded under the parent's lock, which is held), and it is
// stopped now when p's stop already reached it.
func (t *Task) adopt(p *Task) {
	t.mu.Lock()
	for c := t.first; c != nil; {
		next := c.next
		c.shielded = c.shielded || t.shielded
		c.parent.Store(p)
		c.prev, c.next = nil, p.first
		if p.first != nil {
			p.first.prev = c
		}
		p.first = c
		if !c.shielded && p.stopped.Load() {
			c.Interrupt()
		}
		c = next
	}
	t.first = nil
	t.mu.Unlock()
}

// ID is the task's number, unique in the process.
func (t *Task) ID() uint64 { return t.id }

// Span is the span or definition ID the task was started under (G2).
func (t *Task) Span() string { return t.span }

// envMemos holds the copies of a program environment that host callbacks
// made for tasks (EnvMemo), in a slot chosen by the task's ID. It lives
// outside Task, which keeps its size class (trace_test.go). Two tasks that
// share a slot take turns in it: a lookup that finds the other's copy
// misses, and the caller copies again (the lowering checks the copy's
// task). A held copy keeps its task live, so no new task has its address.
var envMemos [envMemoSlots]atomic.Pointer[envMemo]

const envMemoSlots = 256

type envMemo struct{ env any }

// EnvMemo is the value SetEnvMemo last stored for t, or a value stored for
// another task, or nil: the caller checks it is t's. The lowering keeps
// there the copy of a program environment that a host callback made to run
// an Ergo body on t (internal/sketch/lower closures.go, envMemoText), so
// the next callback on t with the same environment reuses it instead of
// copying it again (ENV-COPY). Any goroutine may call it; a stored value
// is never written.
func (t *Task) EnvMemo() any {
	if m := envMemos[t.id%envMemoSlots].Load(); m != nil {
		return m.env
	}
	return nil
}

// SetEnvMemo stores e as t's EnvMemo.
func (t *Task) SetEnvMemo(e any) { envMemos[t.id%envMemoSlots].Store(&envMemo{env: e}) }

// Parent is t's current parent in the task tree, nil for a root or detached
// task. A defect keeps its ancestry as copied frames. Safe from any goroutine.
func (t *Task) Parent() *Task { return t.parent.Load() }

// ChainCutLine follows the last frame of a chain cut at its bound, so a
// reader can tell a cut chain from a complete one of the same length.
const ChainCutLine = "... (the chain is cut at 32 tasks)"

// Chain copies t and its live ancestors as frames, from t to the root, at
// most 32. cut reports that ancestors remained past the bound. Call it from
// t's goroutine while t runs; a defect records it then.
func (t *Task) Chain() (frames []Frame, cut bool) {
	x := t
	for ; x != nil && len(frames) < maxChain; x = x.parent.Load() {
		frames = append(frames, Frame{Task: x.id, Span: x.span})
	}
	return frames, x != nil
}

// Interrupt requests that t and every task below it stop, except the subtree
// of a shielded child (see init), which only a direct Interrupt reaches. It
// does not wait.
// Each task observes the request at its next interruption point outside a
// mask. Interrupt is idempotent and safe from any goroutine.
func (t *Task) Interrupt() { t.interrupt(false) }

func (t *Task) interruptBudget() { t.interrupt(true) }

func (t *Task) interrupt(budget bool) {
	t.mu.Lock()
	if !t.stopped.Load() {
		if budget {
			t.stopped.v.Or(flagBudgetSpent)
		}
		// The event is recorded before the flag is set, so it happens before
		// any observation of the stop and a recorder orders it first.
		if t.trace != nil {
			t.traceTask(TraceCancelRequest, nil)
		}
		// A wait that ends on the stop is unparked in the step that makes
		// the stop visible (idle.go).
		m := t.waking(stopSource{})
		t.stopped.set()
		if t.stop != nil {
			close(t.stop)
		}
		m.unlock()
		// A wait parked on t's word ends on the stop: the wake comes after
		// the flag, so the woken waiter sees it.
		t.wake.unpark()
		t.wakeWaits()
		if t.waker {
			if wake, ok := wakers.Load(t); ok {
				wake.(StopWaker).Wake()
			}
		}
		// Locks are taken ancestor first; detach takes only the parent's
		// lock, so the walk cannot deadlock with a finishing child.
		for c := t.first; c != nil; c = c.next {
			if !c.shielded {
				c.Interrupt()
			}
		}
	}
	t.mu.Unlock()
}

// Stopped is the interruption point (I1): it reports whether t was asked to
// stop and is not masked. While no stop is requested and no WhenPolled hook
// is set it costs one atomic load; the region state it reads after that is
// owner-local.
func (t *Task) Stopped() bool {
	if t.stopped.v.Load()&(flagStopped|flagPolled) == 0 {
		return false
	}
	return t.stoppedSlow()
}

// stoppedSlow is Stopped once its word has a stop or a poll flag:
// it runs any WhenPolled hook, respects masks, charges an active step
// budget, and reports whether a stop is observable.
func (t *Task) stoppedSlow() bool {
	return t.pollSlow(true)
}

func (t *Task) pollSlow(charge bool) bool {
	flags := t.stopped.v.Load()
	if flags&flagPollHook != 0 {
		t.firePollHook()
	}
	if t.regions.top != interruptible {
		return false
	}
	if flags&flagStopped != 0 {
		return true
	}
	if charge && flags&flagBudget != 0 {
		t.chargeStep()
	}
	return t.stopped.Load()
}

// StopRequested reports whether a stop was requested, masked or not.
func (t *Task) StopRequested() bool { return t.stopped.Load() }

// Masked reports whether t is inside a masked region. Call it from the task's
// own goroutine.
func (t *Task) Masked() bool { return t.regions.top != interruptible }

// SetFiring sets t's firing mark (runtime/v1 MachineFiringOn). Call it
// from the task's own goroutine.
func (t *Task) SetFiring(on bool) { t.firing = on }

// Firing reports t's firing mark. Call it from the task's own goroutine.
func (t *Task) Firing() bool { return t.firing }

// Check is the interruption point for code with no error row: it panics with
// Interrupted when Stopped would return true. The task boundary recovers it.
func (t *Task) Check() {
	if t.stopped.v.Load()&(flagStopped|flagPolled) != 0 {
		t.checkSlow()
	}
}

func (t *Task) checkSlow() {
	if t.stoppedSlow() {
		panic(Interrupted)
	}
}

// A StopWaker is woken by a stop request (OnInterrupt).
type StopWaker interface{ Wake() }

// OnInterrupt sets wake, which the stop request of t calls once, so a wait
// on a channel of the caller's own can wake on a stop without a select on
// Done (MU18: an actor's runner waits on its inbox's wake channel). wake
// runs on the goroutine that requests the stop, under t's lock, so it must
// not wait or take a lock that a stop can hold; a send of a token to a
// buffered channel without waiting is the intended body. When the stop was
// requested already, wake runs now. Each task has at most one wake: a
// second call replaces the first, and OnInterrupt(nil) removes it, which
// the waiter does before its task ends. The waiter checks Stopped after
// each wake-up, since wake says only that it should look. Call it from the
// task's own goroutine. The wakes live in a map beside the tree (wakers),
// so a task without one pays nothing and keeps its size. Prior art: Go's
// context.AfterFunc.
func (t *Task) OnInterrupt(wake StopWaker) {
	t.mu.Lock()
	if wake == nil {
		wakers.Delete(t)
		t.waker = false
		t.mu.Unlock()
		return
	}
	wakers.Store(t, wake)
	t.waker = true
	stopped := t.stopped.Load()
	t.mu.Unlock()
	if stopped {
		wake.Wake()
	}
}

// wakers holds the OnInterrupt wakes: *Task -> StopWaker.
var wakers sync.Map

// Done returns a channel that is closed when a stop is requested, for a
// blocking wait that must also wake on a stop (I1, rule 5). Inside a masked
// region it returns nil, so the wait is uninterruptible (I2). Call it from the
// task's own goroutine. Every wait of the runtime calls it after it has
// registered what it waits for (a Sleep after its timer is armed), so it is
// also the point where WhenWaiting's hook runs.
func (t *Task) Done() <-chan struct{} {
	t.Waits()
	if t.regions.top != interruptible {
		return nil
	}
	// Once stop is made, a wait reads it with one atomic load and no lock
	// (MU18: an actor's runner and askers wait on it at every park). A Go
	// goroutine may make it (taskContext.Done), so the load of the made
	// flag, not this goroutine's own write, orders the read.
	if t.stopped.made() {
		return t.stop
	}
	t.mu.Lock()
	ch := t.makeStop()
	t.mu.Unlock()
	return ch
}

// makeStop returns t's stop channel, made now if no Done made it: closed
// when a stop was requested already. It sets the made flag after the
// write, so a read that sees the flag sees the channel. Hold t.mu.
func (t *Task) makeStop() chan struct{} {
	if t.stop == nil {
		t.stop = make(chan struct{})
		if t.stopped.Load() {
			close(t.stop)
		}
		t.stopped.setMade()
	}
	return t.stop
}

// stopFlags holds the stop request, whether the stop channel is ready,
// whether the task has a poll hook, and whether it ever started a child.
type stopFlags struct{ v atomic.Uint32 }

const (
	flagStopped uint32 = 1 << iota
	flagMade
	// flagPolled marks a task with a WhenPolled hook. Only the task's own
	// goroutine sets or clears it.
	flagPolled
	// flagParent marks a task that has had a child linked below it (init),
	// so detach looks for children to hand on only in a task that can have
	// one: a leaf task ends without taking its own lock. adopt needs no
	// mark: it moves a child of t to p, and t is p's child, so p has it.
	flagParent
	// flagPollHook marks a task with an armed WhenPolled hook. Only the
	// task's own goroutine sets or clears it; flagPolled stays set while
	// either flagPollHook or flagBudget is set.
	flagPollHook
	// flagBudget marks a task whose unmasked interruption points count
	// against an active step budget.
	flagBudget
	// flagBudgetSpent marks a task whose own step budget ran out and
	// interrupted it.
	flagBudgetSpent
)

// setParent marks the task as one a child was linked below.
func (f *stopFlags) setParent() { f.v.Or(flagParent) }

// parent reports whether a child was ever linked below the task.
func (f *stopFlags) parent() bool { return f.v.Load()&flagParent != 0 }

// Load reports whether a stop was requested.
func (f *stopFlags) Load() bool { return f.v.Load()&flagStopped != 0 }

// set requests the stop; a stop is never withdrawn.
func (f *stopFlags) set() { f.v.Or(flagStopped) }

func (f *stopFlags) made() bool { return f.v.Load()&flagMade != 0 }

func (f *stopFlags) setMade() { f.v.Or(flagMade) }

// waitFlush is the output the process holds back until a task waits:
// runtime/v1's standard-output buffer (docs/spec/stdlib.md, io 11). Done
// is where a task reaches a wait of the runtime, so a program that writes
// to a pipe and then waits (a sleep, a queue, a join, a child, the
// network, its input) shows what it wrote before the wait, with no timer.
// pending is set while output is held, so Done pays one atomic load when
// none is; flush is set once, at runtime/v1's init, before any task runs.
var waitFlush struct {
	pending atomic.Bool
	flush   func()
}

// SetWaitFlush sets the function Done runs while output is held
// (OutputHeld). Call it once, before the first OutputHeld(true): the
// atomic record orders the set before every Done that reads it.
func SetWaitFlush(f func()) { waitFlush.flush = f }

// OutputHeld records whether output is held for the next wait: true after
// a write the buffer keeps, false after the flush.
func OutputHeld(held bool) { waitFlush.pending.Store(held) }

// IsOutputHeld reports OutputHeld's last record: a flush point with nothing
// held is one atomic load.
func IsOutputHeld() bool { return waitFlush.pending.Load() }

// waitHooks holds the hooks of WhenWaiting, keyed by task, and how many
// there are. A hook lives from WhenWaiting to the task's first wait, so the
// count is almost always zero and Done pays one atomic load. The hooks are
// outside Task so that a task keeps its allocation size class
// (TestTraceHookKeepsTaskSize).
//
// One hook at a time lives in the slot (one, its task in oneTask), the
// rest in m: a hook is stored and removed with no allocation while it is
// the only one (FU-timer-task-reuse: an actor arms one per timer).
var waitHooks struct {
	n       atomic.Int64
	mu      sync.Mutex // guards one; oneTask is written under it
	oneTask atomic.Pointer[Task]
	one     func()
	m       sync.Map // *Task -> func()
}

// WhenWaiting runs f once, on t's goroutine, when t first reaches a wait of
// the runtime (Done), after the wait has registered what it waits for. A
// Sleep on a virtual clock has armed its timer by then, so a caller that
// waits for f knows that an Advance fires the sleep. cancel removes the hook
// if it has not run; call it when t ends. Call WhenWaiting from t's own
// goroutine.
func WhenWaiting(t *Task, f func()) (cancel func()) {
	addWaitHook(t, f)
	return func() { takeWaitHook(t) }
}

// ClearWaitHook is WhenWaiting's cancel for t, which a caller that keeps
// no cancel calls when t ends. Call it from t's own goroutine.
func ClearWaitHook(t *Task) { takeWaitHook(t) }

// WhenWaitingHook is WhenWaiting with no cancel func to allocate: the
// caller ends the hook with ClearWaitHook.
func WhenWaitingHook(t *Task, f func()) { addWaitHook(t, f) }

func addWaitHook(t *Task, f func()) {
	waitHooks.mu.Lock()
	if waitHooks.oneTask.Load() == nil {
		waitHooks.one = f
		waitHooks.oneTask.Store(t)
	} else {
		waitHooks.m.Store(t, f)
	}
	waitHooks.n.Add(1)
	waitHooks.mu.Unlock()
}

// takeWaitHook removes t's hook and returns it, nil when t has none.
func takeWaitHook(t *Task) func() {
	if waitHooks.oneTask.Load() == t {
		waitHooks.mu.Lock()
		if waitHooks.oneTask.Load() == t {
			f := waitHooks.one
			waitHooks.one = nil
			waitHooks.oneTask.Store(nil)
			waitHooks.n.Add(-1)
			waitHooks.mu.Unlock()
			return f
		}
		waitHooks.mu.Unlock()
	}
	if f, ok := waitHooks.m.LoadAndDelete(t); ok {
		waitHooks.n.Add(-1)
		return f.(func())
	}
	return nil
}

// fireWaitHook runs and removes t's WhenWaiting hook, if t has one.
func (t *Task) fireWaitHook() {
	if f := takeWaitHook(t); f != nil {
		f()
	}
}

// PollHook is the storage of one WhenPolled hook that its owner keeps, so
// arming a hook allocates nothing and takes no lock (HTTP-FAST-c5): the
// HTTP engine keeps one per connection for the requests it serves. The
// hook is a context binding of t under an unexported key, the innermost
// one while it is armed; a task with one has flagPolled in its stop word,
// so an interruption point finds the flag in the load it makes for the
// stop, and a task without one pays nothing.
type PollHook struct{ s Slot }

// pollKey is the key of a PollHook's binding; no reader outside rt can
// name it, and a defect's report prints only runtime/v1's context keys.
type pollKey struct{}

// Polled is an armed WhenPolled hook; End disarms it.
type Polled struct {
	t *Task
	c Context
}

// WhenPolled runs f once, on t's goroutine, when t first reaches an
// interruption point: a wait of the runtime (Done), Check or Stopped. f
// runs before that point reads the stop flag, so a stop f requests is seen
// there. It is for a watch that only a task that can be stopped needs (the
// HTTP engine's read that sees a client leave): a task that never polls
// cannot be stopped, so it never pays for one. h holds the hook and must
// stay unchanged until End, which t calls before it ends, on every path,
// after any context binding made after WhenPolled has ended (a binding
// region, as Bind's). Call WhenPolled from t's own goroutine.
func WhenPolled(t *Task, h *PollHook, f func()) Polled {
	c := t.Bind(&h.s, pollKey{}, f)
	t.stopped.v.Or(flagPolled | flagPollHook)
	return Polled{t: t, c: c}
}

// End disarms the hook if it has not run and ends its binding.
func (p Polled) End() {
	p.t.clearPollHook()
	p.c.End()
}

func (t *Task) clearPollHook() {
	for {
		old := t.stopped.v.Load()
		if old&flagPollHook == 0 {
			return
		}
		next := old &^ flagPollHook
		if next&flagBudget == 0 {
			next &^= flagPolled
		}
		if t.stopped.v.CompareAndSwap(old, next) {
			return
		}
	}
}

// firePollHook runs and disarms t's WhenPolled hook if one is armed.
func (t *Task) firePollHook() {
	if t.stopped.v.Load()&flagPollHook == 0 {
		return
	}
	t.clearPollHook()
	if f, ok := t.Value(pollKey{}).(func()); ok {
		f()
	}
}
