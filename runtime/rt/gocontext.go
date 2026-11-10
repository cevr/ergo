package rt

import (
	"context"
	"time"
)

// GoContext is the context.Context a Go call of the task t receives as its
// hidden first argument (IG10; docs/spec/interop.md, Host calls): Done is
// closed and Err is context.Canceled once a stop of t is requested, so an
// interruption cancels the Go wait. Inside a masked region (a release, an
// acquisition) the call is uninterruptible (I2), so it gets a context that
// is never cancelled.
//
// It allocates nothing: the value is one pointer, which an interface holds
// directly, and Done makes the task's stop channel only when Go first asks
// for it, as hand Go's context.WithCancel makes its channel lazily.
func GoContext(t *Task) context.Context {
	if t.Masked() {
		return context.Background()
	}
	return taskContext{t}
}

// taskContext is the task as a context.Context. Go may call its methods
// from any goroutine; each takes the task's lock, never its mask.
type taskContext struct{ t *Task }

func (taskContext) Deadline() (time.Time, bool) { return time.Time{}, false }

// Done is the task's stop channel, made on first use (Interrupt closes it).
// A Go goroutine may make it while the task runs; makeStop publishes it
// through the made flag, which the task's own Done reads first.
func (c taskContext) Done() <-chan struct{} {
	t := c.t
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.makeStop()
}

// Err is context.Canceled once a stop is requested. Interrupt sets the
// flag and closes the channel under the lock, so Err is nil exactly while
// Done is open (context.Context's contract).
func (c taskContext) Err() error {
	c.t.mu.Lock()
	defer c.t.mu.Unlock()
	if c.t.stopped.Load() {
		return context.Canceled
	}
	return nil
}

// Value has no keys: Ergo's context values are typed and reach Go only as
// arguments.
func (taskContext) Value(any) any { return nil }

func (c taskContext) String() string { return "ergo task context" }
