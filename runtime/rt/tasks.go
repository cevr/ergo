package rt

import "sync"

// MapSpan is tasks.Transform (docs/spec/stdlib.md, Tasks): it calls f on each
// element of xs, each call on a child task of t started under span, with at
// most limit calls running at once, and returns the results in the order of
// xs. limit is at least 1 (the caller checks it).
//
//   - A child starts only while fewer than limit run; when one ends, the
//     next element starts. So at most limit calls of f run at once.
//   - MapSpan waits for whichever child ends first. The first exit that is
//     not a success it sees (a typed failure, a defect, an interruption)
//     ends the map: no child starts after it, every child still running is
//     stopped, and all are drained before MapSpan returns. That exit is the
//     result, with the defects of the drained children after it.
//   - A stop of t, before or while MapSpan waits, stops and drains every
//     running child; the exit is their defects with an interruption.
//
// Every child is drained before MapSpan returns, on every path, a panic
// included (structured concurrency: runtime.md, Tasks). In an auto clock's
// domain the waiting caller is parked (idle.go), so a map over sleeping
// children moves virtual time. Prior art: Go's errgroup.Group with SetLimit
// (the first error cancels the group's context; Wait waits for all);
// Effect's Effect.forEach(xs, f, { concurrency }) (fail-fast, interrupts the
// rest); Elixir's Task.async_stream with max_concurrency and ordered: true.
func MapSpan[A, B any](t *Task, span string, xs []A, limit int, f func(*Task, A) (B, error)) (out []B, err error) {
	if t.Stopped() {
		return nil, Interrupted
	}
	out = make([]B, len(xs))
	if len(xs) == 0 {
		return out, nil
	}
	var (
		c     Cause
		hs    = make([]*Handle[B], len(xs)) // the children not joined yet
		e     = &ends{}
		floor int // the calls open in t when MapSpan began, on a traced tree
	)
	if t.trace != nil {
		floor = t.traceDepth()
	}
	defer func() {
		if p := recover(); p != nil {
			c.recovered(t, p, false, floor)
		}
		m := t.Mask()
		// Stop every running child first, so all wind down at once, then
		// drain each.
		for _, h := range hs {
			if h != nil {
				h.task.Interrupt()
			}
		}
		for i, h := range hs {
			if h != nil {
				h.join(t)
				hs[i] = nil
				if h.task.trace != nil {
					h.task.childEvent(TraceObserve, t, false)
				}
				c.Add(defectsOf(h.err))
			}
		}
		m.Unmask()
		t.settle(&c, nil)
		if err = c.Err(); err != nil {
			out = nil
		}
	}()
	stop := t.Done()
	next, running := 0, 0
	for ended := 0; ended < len(xs); ended++ {
		for running < limit && next < len(xs) {
			i := next
			next++
			running++
			hs[i] = spawn(t, span, false, func(ct *Task) (B, error) {
				defer e.end(t, i)
				return f(ct, xs[i])
			})
		}
		i, ok := e.next(t, stop)
		if !ok {
			return out, nil // stopped: the deferred drain settles the exit
		}
		h := hs[i]
		h.join(t)
		hs[i] = nil
		running--
		if h.task.trace != nil {
			h.task.childEvent(TraceObserve, t, true)
		}
		if h.err != nil {
			c.Add(h.err)
			return out, nil // the deferred drain stops the rest
		}
		out[i] = h.value
	}
	return out, nil
}

// ends is the line of children of a MapSpan whose call of f has returned,
// in the order they returned. The parent waits on it.
type ends struct {
	mu   sync.Mutex
	idx  []int
	wake chan struct{} // made by a waiting parent, closed by the next end
}

// end records that child i's call has returned and wakes w, the parent,
// unparking it first when it is parked on e (idle.go).
func (e *ends) end(w *Task, i int) {
	e.mu.Lock()
	e.idx = append(e.idx, i)
	if e.wake != nil {
		ch := e.wake
		e.wake = nil
		Wake(w, e, func() { close(ch) })
	}
	e.mu.Unlock()
}

// next is the index of the next child whose call has returned, waiting for
// one; ok is false when stop closes first. t, the parent, parks on e while
// it waits, under e.mu, so an end, which also runs under e.mu, unparks it
// before it wakes it.
func (e *ends) next(t *Task, stop <-chan struct{}) (i int, ok bool) {
	e.mu.Lock()
	for len(e.idx) == 0 {
		if stop != nil && t.stopped.Load() {
			e.mu.Unlock()
			return 0, false
		}
		ch := make(chan struct{})
		e.wake = ch
		m := park(t, e, nil, stop, nil)
		e.mu.Unlock()
		select {
		case <-ch:
		case <-stop:
			m.unpark()
			return 0, false
		}
		m.unpark()
		e.mu.Lock()
	}
	i = e.idx[0]
	e.idx = e.idx[1:]
	e.mu.Unlock()
	return i, true
}
