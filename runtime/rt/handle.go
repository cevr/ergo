package rt

// Handle is a started task: the runtime side of `Task[T, E]`. It is a
// resource (runtime.md Tasks, rule 3): Wait and Stop consume it, and an owner
// scope stops and drains it at scope end if neither ran. Its methods belong to
// the owner task's goroutine.
type Handle[T any] struct {
	task  Task
	owner *Task // the starting task, until the handle is consumed
	// done is closed when the task ends, for a wait that selects on the
	// end with other channels (After's race); nil for every other handle,
	// whose waiter parks on its own task's word (park.go).
	done  chan struct{}
	value T
	err   error // canonical exit; written before the task's ended flag is set
	// The handle's consumed flag and the task's ended flag (its value and
	// err are final) live in task's first word (Task.consumed,
	// stopFlags.ended), so a Handle[struct{}] stays in the 176 B class.
}

// Go starts body as a child of s's task, on its own goroutine, owned by s.
// The child starts interruptible, even when Go runs inside a masked region.
// span names the task in defect chains (G2). When s ends with the handle
// unconsumed, s stops and drains it before releasing anything acquired
// earlier: the structured-lend guarantee (resources.md R27).
func Go[T any](s *Scope, span string, body func(*Task) (T, error)) *Handle[T] {
	h := spawn(s.task, span, false, false, false, body)
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
	h := spawn(s.task, span, false, true, false, body)
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
	h := spawn(s.task, span, true, false, false, func(t *Task) (T, error) {
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
	start(h, parent, span, false, false, b)
}

// spawn starts body as a child of parent. selects makes the handle's done
// channel, for a waiter that selects on the end (After).
func spawn[T any](parent *Task, span string, masked, shield, selects bool, body func(*Task) (T, error)) *Handle[T] {
	h := &Handle[T]{}
	if selects {
		h.done = make(chan struct{})
	}
	start(h, parent, span, masked, shield, funcBody[T](body))
	return h
}

func start[T any](h *Handle[T], parent *Task, span string, masked, shield bool, b Body[T]) {
	Point(parent, Op{OpSpawn, parent})
	h.owner = parent
	h.task.init(parent, span, masked, shield)
	schedSpawn(parent, &h.task)
	Spread() // before the go statement (alone.go)
	go h.run(b)
}

// funcBody is a body function as a Body; a func value is one word, so the
// conversion allocates nothing.
type funcBody[T any] func(*Task) (T, error)

func (f funcBody[T]) Run(t *Task) (T, error) { return f(t) }

func (h *Handle[T]) run(b Body[T]) {
	schedStart(&h.task)
	defer h.finish()
	h.value, h.err = Run(&h.task, b.Run)
}

// finish detaches the task and publishes the exit that Run settled. In an
// auto clock's domain the waiter's join is unparked in the step that
// sets ended, and the task leaves the domain after it. The waiter is the
// parent, the only task that waits for the end; detach clears the link,
// so it is read first. The waiter is resumed after ended is set, so the
// woken waiter sees it.
func (h *Handle[T]) finish() {
	Point(&h.task, Op{OpEnd, &h.task})
	waiter := h.task.parent.Load()
	h.task.detach()
	m := waiter.waking(h)
	h.task.stopped.setEnded()
	if h.done != nil {
		close(h.done)
	}
	m.unlock()
	waiter.wake.unpark()
	if own := h.task.member.Load(); own != nil {
		own.leave()
	}
	schedEnd(&h.task)
}

// finished reports whether the task has finished.
func (h *Handle[T]) finished() bool { return h.task.stopped.ended() }

// join waits, uninterruptibly, until the task has finished, parked on h
// when w, the waiter, is in an auto clock's domain.
func (h *Handle[T]) join(w *Task) {
	m := h.park(w, false)
	Block(w)
	for !h.task.stopped.ended() {
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

// Wait waits for the task and returns its value, or its exit in canonical
// form; a defect keeps the chain of tasks that led to it (G2). Wait is an
// interruption point of the owner (I1): if the owner is stopped before or
// while it waits, even when the task has already finished, Wait stops and
// drains the task and returns the task's defects with the interruption. Wait
// consumes the handle.
func (h *Handle[T]) Wait() (T, error) {
	owner := h.owner
	h.task.consumed, h.owner = true, nil
	Point(owner, Op{OpJoin, &h.task})
	if !owner.Stopped() {
		// The owner parks on its own word until the task ends or a stop it
		// can observe comes (Interrupt resumes it after setting the flag).
		// Inside a mask Stopped stays false, so the wait ignores a stop.
		owner.Waits()
		m := h.park(owner, !owner.Masked())
		Block(owner)
		for !h.task.stopped.ended() && !owner.Stopped() {
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
// handle it returns nil.
func (h *Handle[T]) Stop() error {
	if h.task.consumed {
		return nil
	}
	owner := h.owner
	h.task.consumed, h.owner = true, nil
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
