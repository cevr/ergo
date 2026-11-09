package rt

import "time"

// After is timeout.After (runtime.md, Timeouts): an ordinary library function
// over a child task, Interrupt, a drain and a Scheduler timer. It runs body as
// a child of t and returns only after the child has finished, releases
// included.
//
//   - The body finishes first: its value or its exit.
//   - The deadline passes first: the child is stopped and drained, and the exit
//     is the child's defects followed by Expired. The abandoned child's typed
//     failure and the interruption After caused are normalized (laws R3, R8),
//     so a lone Expired stays a catchable typed failure.
//   - t is stopped first or while the body runs: the child is stopped and
//     drained, and the exit is the child's defects with an interruption.
//
// Every exit of After, including a panic in the Scheduler or Timer provider,
// passes one unwind boundary. It stops and drains a child that is still
// running, stops the timer, and adds each cleanup defect after the exit it
// already holds, so a defect in the child is kept on every path (Exit causes,
// rules 3 and 4).
func After[T any](t *Task, s Scheduler, d time.Duration, body func(*Task) (T, error)) (T, error) {
	return AfterSpan(t, s, d, "timeout.After", body)
}

// AfterSpan is After with the child task started under span: emitted code
// passes the definition ID of the timeout block, so a defect chain and a trace
// name the block, not the library function.
func AfterSpan[T any](t *Task, s Scheduler, d time.Duration, span string, body func(*Task) (T, error)) (v T, err error) {
	if t.Stopped() {
		return v, Interrupted
	}
	var (
		c      Cause
		timer  Timer
		h      *Handle[T]
		joined bool // the child's exit is in c
		floor  int  // the calls open in t when After began, on a traced tree
	)
	if t.trace != nil {
		floor = t.traceDepth()
	}
	defer func() {
		if p := recover(); p != nil {
			c.recovered(t, p, false, floor)
		}
		m := t.Mask()
		if h != nil && !joined {
			h.task.Interrupt()
			<-h.done
			if h.task.trace != nil {
				h.task.traceForget()
			}
			c.Add(defectsOf(h.err))
		}
		if timer != nil {
			stopTimer(t, timer, &c)
		}
		m.Unmask()
		t.settle(&c, nil)
		if err = c.Err(); err != nil {
			var zero T
			v = zero
		}
	}()
	timer = s.NewTimer(d)
	h = spawn(t, span, false, false, body)
	select {
	case <-h.done:
		joined = true
		if h.task.trace != nil {
			h.task.childEvent(TraceObserve, t, true)
		}
		c.Add(h.err)
		v = h.value
	case <-timer.C():
		if h.task.trace != nil {
			h.task.childEvent(TraceDeadline, t, false)
		}
		h.task.Interrupt()
		<-h.done
		joined = true
		if h.task.trace != nil {
			h.task.childEvent(TraceObserve, t, false)
		}
		c.Add(defectsOf(h.err))
		c.AddFailure(Expired{})
	case <-t.Done():
		h.task.Interrupt()
		<-h.done
		joined = true
		if h.task.trace != nil {
			h.task.traceForget()
		}
		c.Add(defectsOf(h.err))
	}
	return v, nil
}

// stopTimer disarms a timer as a release: a panic in the provider's Stop is a
// release defect.
func stopTimer(t *Task, timer Timer, c *Cause) {
	floor := 0 // the calls open when the stop began, on a traced tree
	if t.trace != nil {
		floor = t.traceDepth()
	}
	defer func() {
		if p := recover(); p != nil {
			c.recovered(t, p, true, floor)
		}
	}()
	timer.Stop()
}
