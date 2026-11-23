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
		if t.Stopped() {
			return out, nil // no call starts after a stop of t
		}
		for running < limit && next < len(xs) {
			i := next
			next++
			running++
			hs[i] = spawn(t, span, false, false, func(ct *Task) (B, error) {
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

// RaceSpan is tasks.Race (docs/spec/stdlib.md, Tasks): it calls f(i) for
// each i in 0..n-1, each call on a child task of t started under span, and
// the first call to end decides. n is at least 1 (the caller checks it).
//
//   - RaceSpan waits for whichever call ends first, then stops every other
//     call and drains all of them before it returns.
//   - The winner is the call that ended first. A call that ended with its
//     own exit before the stop reached it (it did not end by the
//     interruption) ended at the same instant: on a VirtualClock no time
//     passes between them. Among those the lowest index wins, so a tie goes
//     to the earliest block.
//   - The winner's exit is the result: its value, or its typed failure.
//     The losers' typed failures are dropped (laws R3, R6); their defects
//     are kept after the winner's exit.
//   - A stop of t, before or while RaceSpan waits, stops and drains every
//     call; the exit is their defects with an interruption.
//
// Prior art: Effect's Effect.raceAll (the first to complete; the rest are
// interrupted and awaited); Go's select over result channels with a cancel
// of the losers' context; Elixir's Task.yield_many with Task.shutdown.
func RaceSpan[B any](t *Task, span string, n int, f func(*Task, int) (B, error)) (v B, err error) {
	if t.Stopped() {
		return v, Interrupted
	}
	var (
		c     Cause
		hs    = make([]*Handle[B], 0, n) // the children, all joined before return
		own   = make([]bool, n)          // call i returned its own exit, vals[i] and errs[i]
		vals  = make([]B, n)
		errs  = make([]error, n)
		e     = &ends{}
		floor int
		win   = -1
	)
	if t.trace != nil {
		floor = t.traceDepth()
	}
	defer func() {
		if p := recover(); p != nil {
			c.recovered(t, p, false, floor)
		}
		m := t.Mask()
		for i, h := range hs {
			if i != win {
				h.task.Interrupt()
			}
		}
		for _, h := range hs {
			h.join(t)
		}
		// The winner is the lowest index that ended with its own exit; the
		// call seen first is one of them, unless a stop of t ended it.
		if win >= 0 {
			for i := range own {
				if own[i] {
					win = i
					break
				}
			}
		}
		for i, h := range hs {
			if h.task.trace != nil {
				h.task.childEvent(TraceObserve, t, i == win)
			}
			switch {
			case i == win && own[i]:
				// Its own exit: a stop that reached it after f returned
				// (a tie) does not change it.
				c.Add(errs[i])
				v = vals[i]
			case i == win:
				c.Add(h.err)
				v = h.value
			default:
				c.Add(defectsOf(h.err))
			}
		}
		m.Unmask()
		t.settle(&c, nil)
		if err = c.Err(); err != nil {
			var zero B
			v = zero
		}
	}()
	for i := range n {
		hs = append(hs, spawn(t, span, false, false, func(ct *Task) (B, error) {
			// A call that panics keeps own[i] false: its defect stays in
			// its handle's exit.
			defer e.end(t, i)
			v, err := f(ct, i)
			own[i], vals[i], errs[i] = err == nil || !OnlyInterruption(err), v, err
			return v, err
		}))
	}
	first, ok := e.next(t, t.Done())
	if !ok {
		return v, nil // stopped: the deferred drain settles the exit
	}
	win = first
	return v, nil
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
		Block(t)
		select {
		case <-ch:
		case <-stop:
			Unblock(t)
			m.unpark()
			return 0, false
		}
		Unblock(t)
		m.unpark()
		e.mu.Lock()
	}
	i = e.idx[0]
	e.idx = e.idx[1:]
	e.mu.Unlock()
	return i, true
}
