package rt

import "sync"

// A Carrier is a goroutine kept for the successive child tasks of one
// owner, so a child that StartOn starts does not start a goroutine
// (FU-actor-timer-slot). A goroutine starts with a small stack and grows
// it through its body's call chain by copying, every time: a runtime/v1
// actor that arms a timer child per entry spent 43% of a timer fire so. A
// carrier's stack stays grown.
//
// It runs one child at a time, in the order they start. A child that
// starts while the carrier holds another waiting to run gets a goroutine
// of its own, so no child ever waits for a carrier: an owner that starts
// one child at a time and stops the last before the next (the actor's
// rule 5) never needs that. The zero Carrier is ready; it starts its
// goroutine on first use. Close ends it once the child it runs ends.
type Carrier struct {
	mu      sync.Mutex
	job     func() // the child waiting to run, nil for none
	started bool
	closed  bool
	wake    parker // the carrier's goroutine waits for a job on it
	// handed and ran count the jobs given to the goroutine and those that
	// returned there; last is the handle of the last one handed and
	// lastJob its job, for RestartOn (FU-handle-reuse).
	handed, ran uint64
	last        any
	lastJob     func()
}

// StartOn is Start on c's goroutine: it starts b as a child of parent in
// a new handle that it registers on no scope, so the caller stops and
// drains it (Stop). An owner that keeps b, its span and c for its life
// starts each child with the handle as its one allocation and the
// carrier's job (FU-timer-task-reuse).
func StartOn[T any](c *Carrier, parent *Task, span string, b Body[T]) *Handle[T] {
	return RestartOn(nil, c, parent, span, b)
}

// RestartOn is StartOn in h, the handle of c's last child with the same
// body b, once its job has returned on c's goroutine: the end of a task
// touches its handle after the task's Stop returns, until the job
// returns, so only then is h free. The child then starts with no
// allocation. Otherwise, or when h is nil, it starts in a new handle.
// h must be consumed (Stop or Wait) before.
func RestartOn[T any](h *Handle[T], c *Carrier, parent *Task, span string, b Body[T]) *Handle[T] {
	var job func()
	if h != nil && c.idleAfter(h) {
		*h = Handle[T]{}
		job = c.lastJob
	} else {
		h = &Handle[T]{}
	}
	Point(parent, Op{OpSpawn, parent})
	h.waiter.Store(parent)
	h.task.init(parent, span, false, false)
	schedSpawn(parent, &h.task)
	if job == nil {
		job = func() { h.run(b) }
	}
	Spread() // before the job reaches another goroutine (alone.go; I-FLIP: REF-ALONE x PARK's carrier)
	c.run(h, job)
	return h
}

// idleAfter reports whether h's job was the last handed to c's goroutine
// and every job handed has returned.
func (c *Carrier) idleAfter(h any) bool {
	if Exploring {
		return false
	}
	c.mu.Lock()
	ok := c.last == h && c.ran == c.handed
	c.mu.Unlock()
	return ok
}

// run runs f, the job of h, on c's goroutine, or on a goroutine of its
// own when c is closed or holds a job, or under the explored scheduler,
// whose runs the carrier's hand-off would add steps to.
func (c *Carrier) run(h any, f func()) {
	if Exploring {
		go f()
		return
	}
	c.mu.Lock()
	if c.closed || c.job != nil {
		c.last, c.lastJob = nil, nil
		c.mu.Unlock()
		go f()
		return
	}
	c.job = f
	c.handed++
	c.last, c.lastJob = h, f
	if !c.started {
		c.started = true
		c.mu.Unlock()
		go c.loop()
		return
	}
	c.mu.Unlock()
	c.wake.unpark()
}

// loop runs c's jobs until c is closed and holds none.
func (c *Carrier) loop() {
	ran := false
	for {
		c.mu.Lock()
		if ran {
			c.ran++
		}
		for c.job == nil && !c.closed {
			c.mu.Unlock()
			c.wake.park()
			c.mu.Lock()
		}
		f := c.job
		c.job = nil
		c.mu.Unlock()
		if f == nil {
			return // closed
		}
		f()
		ran = true
	}
}

// Close ends c's goroutine once the job it runs, and one it holds, end. A
// child started after Close gets a goroutine of its own.
func (c *Carrier) Close() {
	c.mu.Lock()
	c.closed = true
	started := c.started
	c.mu.Unlock()
	if started {
		c.wake.unpark()
	}
}
