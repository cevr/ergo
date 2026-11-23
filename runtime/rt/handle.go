package rt

import "sync/atomic"

// Handle is a started task: the runtime side of `Task[T, E]`. It is a
// resource (runtime.md Tasks, rule 3): Wait and Stop consume it, and an owner
// scope stops and drains it at scope end if neither ran. Its methods belong to
// the owner task's goroutine.
type Handle[T any] struct {
	task Task
	// The first scope's release uses starter. It is owner-local, like
	// consumed, and the owner clears it upon consumption.
	starter *Task
	// waiter is nil until a caller other than the linked parent joins.
	// The parent's child-list lock serializes registration and finish.
	waiter atomic.Pointer[Task]
	value  T
	err    error // canonical exit; written before the end is published
}

// Go starts body as a child of s's task, on its own goroutine, owned by s.
// The child starts interruptible, even when Go runs inside a masked region.
// span names the task in defect chains (G2). When s ends with the handle
// unconsumed, s stops and drains it before releasing anything acquired
// earlier: the structured-lend guarantee (resources.md R27).
func Go[T any](s *Scope, span string, body func(*Task) (T, error)) *Handle[T] {
	h := spawn(s.task, span, false, false, body)
	s.push(entry{run: closeResource{}, v: h})
	return h
}

// GoShielded is Go for a child that its owner stops itself: the stop that
// cascades from s's task does not reach it, as for a release-owned child
// (I2), with no mask of s's task. Its handle's Stop, a timeout and s's
// drain still interrupt it directly. An owner that must stop its children
// in an order of its own uses it (runtime/v1's actor stops an entry's
// children, then its work, MU5): a cascade would stop them all at once.
func GoShielded[T any](s *Scope, span string, body func(*Task) (T, error)) *Handle[T] {
	h := spawn(s.task, span, false, true, body)
	s.push(entry{run: closeResource{}, v: h})
	return h
}

// GoMasked is Go for a child that starts inside a masked region, the explicit
// inherit mode (Effect 4 fork with inherited interruptibility; ZIO
// uninterruptibleMask). A stop is deferred for the whole body except inside
// restore.Interruptible, which runs with the child unmasked: the restore of
// ZIO and Effect. A stop the body never observes does not replace its result.
// restore belongs to the body: use it only on the child's goroutine and only
// while the body runs; it must not escape the callback.
func GoMasked[T any](s *Scope, span string, body func(t *Task, restore Mask) (T, error)) *Handle[T] {
	h := spawn(s.task, span, true, false, func(t *Task) (T, error) {
		return body(t, Mask{t: t, index: 0})
	})
	s.push(entry{run: closeResource{}, v: h})
	return h
}

// A Body is a task body that carries its own state: Start calls its Run
// once, on the task's goroutine, as Go calls a body function.
type Body[T any] interface{ Run(*Task) (T, error) }

// Start is Go for a handle that its caller holds inside a value of its own
// (a field) and owns itself: it starts b as a child of parent, on its own
// goroutine, in h, which must be a zero Handle, and registers it on no
// scope, so the caller stops and drains it (Stop) when it releases the
// value. runtime/v1's Task holds its handle so (PARK1): one allocation
// for the task and its handle, and none for the body.
func Start[T any](h *Handle[T], parent *Task, span string, b Body[T]) {
	start(h, parent, span, false, false, b, nil)
}

// spawn starts body as a child of parent.
func spawn[T any](parent *Task, span string, masked, shield bool, body func(*Task) (T, error)) *Handle[T] {
	h := &Handle[T]{}
	start(h, parent, span, masked, shield, funcBody[T](body), nil)
	return h
}

// done is only for a caller that selects on the end and another channel (After).
func start[T any](h *Handle[T], parent *Task, span string, masked, shield bool, b Body[T], done chan struct{}) {
	Point(parent, Op{OpSpawn, parent})
	h.starter = parent
	h.task.init(parent, span, masked, shield)
	schedSpawn(parent, &h.task)
	Spread() // before the go statement (alone.go)
	go h.run(b, done)
}

// funcBody is a body function as a Body; a func value is one word, so the
// conversion allocates nothing.
type funcBody[T any] func(*Task) (T, error)

func (f funcBody[T]) Run(t *Task) (T, error) { return f(t) }

func (h *Handle[T]) run(b Body[T], done chan struct{}) {
	schedStart(&h.task)
	defer h.finish(done)
	h.value, h.err = Run(&h.task, b.Run)
}

// finish unlinks the task and publishes its exit by clearing the parent
// link. The same parent lock serializes registration with this publication.
// A clock observes the waiter become runnable and the exit become visible
// in one step, under the waiter's clock lock.
func (h *Handle[T]) finish(done chan struct{}) {
	Point(&h.task, Op{OpEnd, &h.task})
	p := h.task.lockParent(h.task.parent.Load())
	h.task.unlink(p)
	waiter := h.waiter.Load()
	if waiter == nil {
		waiter = p
	} else {
		h.waiter.Store(nil)
	}
	m := waiter.waking(h)
	h.task.parent.Store(nil)
	if done != nil {
		close(done)
	}
	p.mu.Unlock()
	m.unlock()
	waiter.wake.unpark()
	if own := h.task.member.Load(); own != nil {
		own.leave()
	}
	schedEnd(&h.task)
}

// finished reports whether the task has finished.
func (h *Handle[T]) finished() bool { return h.task.parent.Load() == nil }

// join waits, uninterruptibly, until the task has finished, parked on h
// when w, the waiter, is in an auto clock's domain.
func (h *Handle[T]) join(w *Task) {
	m := h.park(w, false)
	Block(w)
	for !h.finished() {
		w.wake.park()
	}
	Unblock(w)
	m.unpark()
}

// park parks w, the waiter, on h, and on its stop when stoppable, when w
// is in an auto clock's domain, and returns nil otherwise, with no
// allocation.
func (h *Handle[T]) park(w *Task, stoppable bool) *member {
	if w.member.Load() == nil {
		return nil
	}
	return parkOn(w, h, nil, stoppable, h.finished)
}

// Task is the started task, for its ID and span.
func (h *Handle[T]) Task() *Task { return &h.task }

// Starter is the initial owner, for releasing a handle that was not handed
// off. After consumption it is nil. Only the owner may read it.
func (h *Handle[T]) Starter() *Task { return h.starter }

// Consumed reports whether the owner already took the handle.
func (h *Handle[T]) Consumed() bool { return h.task.consumed }

// Wait waits for the task and returns its value, or its exit in canonical
// form; a defect keeps the chain of tasks that led to it (G2). Wait is an
// interruption point of the owner (I1): if the owner is stopped before or
// while it waits, even when the task has already finished, Wait stops and
// drains the task and returns the task's defects with the interruption. Wait
// consumes the handle. Wait is WaitBy by the starting task, for a handle
// that its starter consumes.
func (h *Handle[T]) Wait() (T, error) { return h.WaitBy(h.starter) }

// claim takes the handle. A parent joining on its own goroutine cannot
// itself detach while it is joining; the tree already names that waiter.
// Only a different caller needs to register.
func (h *Handle[T]) claim(w *Task) {
	if h.task.consumed {
		secondWaiter()
	}
	h.task.consumed = true
	h.starter = nil
	if p := h.task.parent.Load(); p != w && p != nil {
		h.registerWaiter(p, w)
	}
}

// registerWaiter either registers under the current parent's lock or finds the
// exit already published. Thus finish sees the registration or the caller
// sees a completed handle and does not park.
func (h *Handle[T]) registerWaiter(p, w *Task) {
	if p = h.task.lockParent(p); p != nil {
		h.waiter.Store(w)
		p.mu.Unlock()
	}
}

// secondWaiter raises the defect of a second waiter (claim), out of line so
// that claim stays small enough to inline.
//
//go:noinline
func secondWaiter() {
	panic("task: a second task waits on a handle already waited for, stopped or released (runtime.md Tasks, rule 11)")
}

// WaitBy is Wait by owner, the task that calls it: the Task's owner, which
// a hand-off may have made another task than the one that started it
// (runtime.md Tasks, rule 11). owner's stop, not the starter's, ends the
// wait (rule 9).
func (h *Handle[T]) WaitBy(owner *Task) (T, error) {
	h.claim(owner)
	Point(owner, Op{OpJoin, &h.task})
	if !owner.Stopped() {
		// The owner parks on its own word until the task ends or a stop it
		// can observe comes (Interrupt resumes it after setting the flag).
		// Inside a mask Stopped stays false, so the wait ignores a stop.
		owner.Waits()
		m := h.park(owner, !owner.Masked())
		Block(owner)
		for !h.finished() && !owner.Stopped() {
			owner.wake.park()
		}
		Unblock(owner)
		m.unpark()
		// An owner that is still not stopped left the wait because the task
		// ended.
		if !owner.Stopped() {
			if h.task.trace != nil {
				h.task.childEvent(TraceObserve, owner, true)
			}
			return h.value, h.err
		}
	}
	h.task.Interrupt()
	h.join(owner)
	if h.task.trace != nil {
		h.task.traceForget()
	}
	// An interrupted join does not observe the task (law R4): keep only its
	// defects, then the interruption.
	var c Cause
	c.Add(defectsOf(h.err))
	c.AddInterruption()
	var zero T
	return zero, c.Err()
}

// Stop interrupts the task and waits, uninterruptibly, until it has finished,
// releases included (runtime.md Tasks, rule 4). It returns only the task's
// defects: its interruption and an abandoned typed failure are the expected
// result of the stop (laws R3, R6). Stop consumes the handle; on a consumed
// handle it returns nil. Stop is StopBy by the starting task, for a handle
// that its starter consumes.
func (h *Handle[T]) Stop() error { return h.StopBy(h.starter) }

// StopBy is Stop by owner, the task that calls it (WaitBy): the join parks
// owner, and finish wakes it.
func (h *Handle[T]) StopBy(owner *Task) error {
	if h.task.consumed {
		return nil
	}
	h.claim(owner)
	Point(owner, Op{OpStop, &h.task})
	h.task.Interrupt()
	h.join(owner)
	if h.task.trace != nil {
		h.task.childEvent(TraceObserve, owner, false)
	}
	return defectsOf(h.err)
}

// Close is the release an owner runs: Stop, with any defect raised as a panic
// so the releasing scope records it. It does nothing on a consumed handle.
func (h *Handle[T]) Close() {
	if err := h.Stop(); err != nil {
		panic(transport{err})
	}
}
