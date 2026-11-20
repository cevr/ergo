package rt

import (
	"sync"
	"sync/atomic"
)

// A Waiter is the park word of one wait (park.go), for a wait that more
// than one goroutine of a task may be in at once: a queue's Push and Pop
// (runtime/v1 Queue.waitOr). A task's own word (Task.Suspend) takes one
// parked goroutine, and Go code may run two goroutines on one task (a Go
// test, an escaping callback that keeps its caller's task), so a wait
// that such code reaches parks on a Waiter of its own, which the wait's
// owner keeps for the next wait, as Go's channels keep a sudog per
// blocked goroutine from a per-P cache. A stop of the task reaches the
// Waiter through the task's waiting slot (Task.waiting), or, when another
// wait of the task holds the slot, through moreWaits.
//
// The zero Waiter is empty. Only one goroutine parks on a Waiter at a
// time, the one in the wait that holds it.
type Waiter struct {
	p    parker
	next *Waiter // in moreWaits, under its lock
}

// Wake ends the park of w, or leaves the wake for its next one. Any
// goroutine may call it, after the change the waiter looks for is
// visible.
func (w *Waiter) Wake() { w.p.unpark() }

// Park parks the calling goroutine, which runs t, on w until a Wake, or a
// stop of t when stoppable (a wait outside a mask). It may return for a
// wake that changed nothing, so the caller looks again; it returns at
// once when t is already stopped and stoppable.
func (w *Waiter) Park(t *Task, stoppable bool) {
	if !stoppable {
		w.p.park()
		return
	}
	// The slot is written before the stop flag is read, and Interrupt sets
	// the flag before it reads the slot: one of the two sees the other.
	slot := t.waiting.CompareAndSwap(nil, w)
	if !slot {
		moreWaits.add(t, w)
	}
	if !t.Stopped() {
		w.p.park()
	}
	if slot {
		t.waiting.Store(nil)
	} else {
		moreWaits.remove(t, w)
	}
}

// wakeWaits wakes the Waiters of t's waits, after Interrupt set the stop.
func (t *Task) wakeWaits() {
	if w := t.waiting.Load(); w != nil {
		w.Wake()
	}
	moreWaits.wake(t)
}

// moreWaits holds the Waiters of a task's waits beyond the one in its
// slot: two goroutines of one task waiting at once, which only Go code
// makes, so one lock for the process serves them. A Waiter enters under
// the lock before its goroutine reads the stop flag, and Interrupt takes
// the lock after it set the flag, so a stop reaches every Waiter that
// entered.
var moreWaits waitList

type waitList struct {
	mu sync.Mutex
	m  map[*Task]*Waiter
	n  atomic.Int32 // the Waiters held, so Interrupt skips the lock when none is
}

func (l *waitList) add(t *Task, w *Waiter) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[*Task]*Waiter{}
	}
	w.next = l.m[t]
	l.m[t] = w
	l.n.Add(1)
	l.mu.Unlock()
}

func (l *waitList) remove(t *Task, w *Waiter) {
	l.mu.Lock()
	if head := l.m[t]; head == w {
		if w.next == nil {
			delete(l.m, t)
		} else {
			l.m[t] = w.next
		}
	} else {
		for c := head; c != nil; c = c.next {
			if c.next == w {
				c.next = w.next
				break
			}
		}
	}
	w.next = nil
	l.n.Add(-1)
	l.mu.Unlock()
}

func (l *waitList) wake(t *Task) {
	if l.n.Load() == 0 {
		return
	}
	l.mu.Lock()
	for w := l.m[t]; w != nil; w = w.next {
		w.Wake()
	}
	l.mu.Unlock()
}
