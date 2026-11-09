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
	// stopped is the stop flag, set by Interrupt from any goroutine.
	stopped atomic.Bool
	// shielded is fixed before t is linked: t was started while its owner was
	// inside a masked region, so the owner's ambient stop does not cascade
	// into it. The cascade reads only this field, never the owner's mask. It
	// sits beside stopped so that the two share one word.
	shielded bool
	// waker marks a task with an OnInterrupt wake in wakers, guarded by mu;
	// it fills the same word, so the task keeps its size.
	waker bool
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

	mu    sync.Mutex
	stop  chan struct{} // made by Done on first use, closed by Interrupt
	first *Task         // first child, guarded by mu

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
	if masked {
		t.regions.push(protecting)
	}
	parent.mu.Lock()
	if !t.shielded && parent.stopped.Load() {
		if t.trace != nil {
			t.traceTask(TraceCancelRequest, nil) // born stopped: the request precedes its start
		}
		t.stopped.Store(true)
	}
	t.next = parent.first
	if t.next != nil {
		t.next.prev = t
	}
	parent.first = t
	parent.mu.Unlock()
}

// detach unlinks a finished child from its parent (runtime.md Tasks, rule 1)
// and drops its reference to the parent, so a finished task retains no
// ancestors (G2, rule 3), and its context values, so a handle kept after the
// task finished holds no context text (G1). Every descendant has finished by
// now, so no chain walk or child start can still read either. It runs on t's
// own goroutine, the only one that touches values.
func (t *Task) detach() {
	t.values = nil
	p := t.parent.Load()
	p.mu.Lock()
	if t.prev != nil {
		t.prev.next = t.next
	} else {
		p.first = t.next
	}
	if t.next != nil {
		t.next.prev = t.prev
	}
	t.prev, t.next = nil, nil
	p.mu.Unlock()
	t.parent.Store(nil)
}

// ID is the task's number, unique in the process.
func (t *Task) ID() uint64 { return t.id }

// Span is the span or definition ID the task was started under (G2).
func (t *Task) Span() string { return t.span }

// Parent is the task that started t while t runs, and nil for a root or a
// finished task. It is a live link, not a record: a defect keeps its ancestry
// as copied frames. Safe from any goroutine.
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
func (t *Task) Interrupt() {
	t.mu.Lock()
	if !t.stopped.Load() {
		// The event is recorded before the flag is set, so it happens before
		// any observation of the stop and a recorder orders it first.
		if t.trace != nil {
			t.traceTask(TraceCancelRequest, nil)
		}
		t.stopped.Store(true)
		if t.stop != nil {
			close(t.stop)
		}
		if t.waker {
			if wake, ok := wakers.Load(t); ok {
				wake.(Waker).Wake()
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
// stop and is not masked. While no stop is requested it costs one atomic load;
// the region state it reads after that is owner-local.
func (t *Task) Stopped() bool { return t.stopped.Load() && t.regions.top == interruptible }

// StopRequested reports whether a stop was requested, masked or not.
func (t *Task) StopRequested() bool { return t.stopped.Load() }

// Masked reports whether t is inside a masked region. Call it from the task's
// own goroutine.
func (t *Task) Masked() bool { return t.regions.top != interruptible }

// Check is the interruption point for code with no error row: it panics with
// Interrupted when Stopped would return true. The task boundary recovers it.
func (t *Task) Check() {
	if t.stopped.Load() && t.regions.top == interruptible {
		panic(Interrupted)
	}
}

// A Waker is woken by a stop request (OnInterrupt).
type Waker interface{ Wake() }

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
func (t *Task) OnInterrupt(wake Waker) {
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

// wakers holds the OnInterrupt wakes: *Task -> Waker.
var wakers sync.Map

// Done returns a channel that is closed when a stop is requested, for a
// blocking wait that must also wake on a stop (I1, rule 5). Inside a masked
// region it returns nil, so the wait is uninterruptible (I2). Call it from the
// task's own goroutine.
func (t *Task) Done() <-chan struct{} {
	if t.regions.top != interruptible {
		return nil
	}
	// Only this goroutine writes t.stop (below), so it reads it without
	// the lock once it is made; Interrupt only reads it, under the lock
	// (MU18: an actor's runner and askers wait on it at every park).
	if ch := t.stop; ch != nil {
		return ch
	}
	t.mu.Lock()
	if t.stop == nil {
		t.stop = make(chan struct{})
		if t.stopped.Load() {
			close(t.stop)
		}
	}
	ch := t.stop
	t.mu.Unlock()
	return ch
}
