package rt

// settle is the one finalization step shared by every task boundary (Run, so
// child tasks and the root alike) and every scope end, empty or not. It
// records a recovered panic, then applies stop-wins (runtime.md, Exit causes
// rule 5): when t can observe a stop, interruption joins the exit and beats a
// body's success. A stop that arrives during a release is observed here, after
// the release mask closes.
func (t *Task) settle(c *Cause, p any) {
	if p != nil {
		c.recovered(t, p, false, 0)
	}
	if t.Stopped() {
		c.AddInterruption()
	}
}

// Run runs body as the body of task t on the calling goroutine, inside the
// task boundary every child started by Go also runs in: a panic becomes a
// defect with t's chain, a stop beats success, and the exit comes back in
// canonical form with a zero value. Emitted main calls it for the root task:
//
//	_, err := rt.Run(rt.Root("main"), mainBody)
//	os.Exit(rt.ExitCode(err))
//
// A law adapter (#124) drives the runtime through the same entry. On a traced
// tree Run raises the start and body-exit events of t.
func Run[T any](t *Task, body func(*Task) (T, error)) (v T, err error) {
	if t.trace != nil {
		t.traceTask(TraceStart, nil)
	}
	defer func() {
		if err = t.exitOf(err, recover()); err != nil {
			var zero T
			v = zero
		}
		if t.trace != nil {
			t.traceExit(err)
		}
	}()
	return body(t)
}

// exitOf is the exit of a task whose body returned err, or panicked with p:
// it builds the Cause and settles it in its own frame. The traced body-exit
// then reaches below the body frame through Run's small deferred frame and
// the tracer's frames only. When the Cause lived in the deferred frame, that
// frame's size followed the Cause code's: a coverage build grew it by 72
// bytes and took the traced exit past its stack budget
// (TestTraceExitStaysOnTheFirstStack).
//
//go:noinline
func (t *Task) exitOf(err error, p any) error {
	var c Cause
	c.Add(err)
	t.settle(&c, p)
	return c.Err()
}
