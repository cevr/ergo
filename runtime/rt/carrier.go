package rt

import "sync"

// A Carrier is a goroutine kept for the successive child tasks of one
// owner, so a child that GoOn starts does not start a goroutine
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
}

// GoOn is Go on c's goroutine.
func GoOn[T any](s *Scope, c *Carrier, span string, body func(*Task) (T, error)) *Handle[T] {
	h := &Handle[T]{}
	parent := s.task
	Point(parent, Op{OpSpawn, parent})
	h.owner = parent
	h.task.init(parent, span, false, false)
	schedSpawn(parent, &h.task)
	b := funcBody[T](body)
	c.run(func() { h.run(b) })
	s.push(entry{run: closeResource{}, v: h})
	return h
}

// run runs f on c's goroutine, or on a goroutine of its own when c is
// closed or holds a job, or under the explored scheduler, whose runs the
// carrier's hand-off would add steps to.
func (c *Carrier) run(f func()) {
	if Exploring {
		go f()
		return
	}
	c.mu.Lock()
	if c.closed || c.job != nil {
		c.mu.Unlock()
		go f()
		return
	}
	c.job = f
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
	for {
		c.mu.Lock()
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
