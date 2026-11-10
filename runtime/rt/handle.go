package rt

// Handle is a started task: the runtime side of `Task[T, E]`. It is a
// resource (runtime.md Tasks, rule 3): Wait and Stop consume it, and an owner
// scope stops and drains it at scope end if neither ran. Its methods belong to
// the owner task's goroutine.
type Handle[T any] struct {
	task     Task
	owner    *Task // the starting task, until the handle is consumed
	done     chan struct{}
	value    T
	err      error // canonical exit; written before done closes
	consumed bool
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

func spawn[T any](parent *Task, span string, masked, shield bool, body func(*Task) (T, error)) *Handle[T] {
	h := &Handle[T]{owner: parent, done: make(chan struct{})}
	h.task.init(parent, span, masked, shield)
	go h.run(body)
	return h
}

func (h *Handle[T]) run(body func(*Task) (T, error)) {
	defer h.finish()
	h.value, h.err = Run(&h.task, body)
}

// finish detaches the task and publishes the exit that Run settled. In an
// auto clock's domain the waiter's join is unparked in the step that
// closes done, and the task leaves the domain after it. The waiter is the
// parent, the only task that waits for done; detach clears the link, so
// it is read first.
func (h *Handle[T]) finish() {
	waiter := h.task.parent.Load()
	h.task.detach()
	m := waiter.waking(h)
	close(h.done)
	m.unlock()
	if own := h.task.member.Load(); own != nil {
		own.leave()
	}
}

// finished reports whether the task has finished.
func (h *Handle[T]) finished() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// join waits, uninterruptibly, until the task has finished, parked on h
// when w, the waiter, is in an auto clock's domain.
func (h *Handle[T]) join(w *Task) {
	m := h.park(w, nil)
	<-h.done
	m.unpark()
}

// park parks w, the waiter, on h and on stop when w is in an auto
// clock's domain, and returns nil otherwise, with no allocation.
func (h *Handle[T]) park(w *Task, stop <-chan struct{}) *member {
	if w.member.Load() == nil {
		return nil
	}
	return park(w, h, nil, stop, h.finished)
}

// Task is the started task, for its ID and span.
func (h *Handle[T]) Task() *Task { return &h.task }

// Wait waits for the task and returns its value, or its exit in canonical
// form; a defect keeps the chain of tasks that led to it (G2). Wait is an
// interruption point of the owner (I1): if the owner is stopped before or
// while it waits, even when the task has already finished, Wait stops and
// drains the task and returns the task's defects with the interruption. Wait
// consumes the handle.
func (h *Handle[T]) Wait() (T, error) {
	owner := h.owner
	h.consumed, h.owner = true, nil
	if !owner.Stopped() {
		stop := owner.Done()
		m := h.park(owner, stop)
		select {
		case <-h.done:
		case <-stop:
		}
		m.unpark()
		// Done closes only on a stop the owner can observe, so an owner that
		// is still not stopped was woken by the task finishing.
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
// handle it returns nil.
func (h *Handle[T]) Stop() error {
	if h.consumed {
		return nil
	}
	owner := h.owner
	h.consumed, h.owner = true, nil
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
