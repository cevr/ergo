package v1

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// ERGO_ACTOR_REFERENCE=1 runs this package's actor tests on the
// reference runner (actorReference), so the model-based tests of MU3,
// MU4, MU5 and MU12 hold for both.
func init() {
	if os.Getenv("ERGO_ACTOR_REFERENCE") == "1" {
		actorReference = true
	}
}

// box is what the property drives: the reference Queue or the mailbox.
type box interface {
	Push(*rt.Task, int) error
	pop(*rt.Task) (int, error)
	Poll() (int, bool)
	Close()
	Len() int
	waiters() int
	line() int     // the Pushes waiting for room
	bind(*rt.Task) // t becomes the taker, as the runner is (bindTaker)
}

type queueBox struct{ *Queue[int] }

func (q queueBox) bind(*rt.Task) {}

// Close is the queue's half-close, the mailbox's Close.
func (q queueBox) Close() { q.CloseSend() }

func (q queueBox) pop(t *rt.Task) (int, error) { return q.Pop(t) }
func (q queueBox) waiters() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.parked
}
func (q queueBox) line() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.senders)
}

type mailBox struct{ *mailbox[int] }

func (m mailBox) bind(t *rt.Task) { m.bindTaker(t) }

func (m mailBox) pop(t *rt.Task) (int, error) {
	var v int
	err := m.popInto(t, &v)
	if err == rt.Interrupted {
		panic(err) // as the Queue's Pop raises it
	}
	return v, err
}
func (m mailBox) line() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.senders)
}

// diverged stops a trace at the first line that differs from the
// reference's, before a wait that a broken box might never end.
type diverged struct{}

// mailboxTrace runs one generated trace on b and returns what each
// operation gave. The trace depends only on seed and a model of the
// contents (queued values, waiting Pushes, closed), never on b, so the
// Queue and the mailbox get the same operations. With want (the Queue's
// log), it stops at the first line that differs: every wait it does next
// is one the reference's line proves will end.
func mailboxTrace(tw testwait.T, seed int64, capacity int, b box, want []string) (log []string) {
	r := rand.New(rand.NewSource(seed))
	defer func() {
		if x := recover(); x != nil {
			if _, ok := x.(diverged); !ok {
				panic(x)
			}
		}
	}()
	say := func(format string, args ...any) {
		log = append(log, fmt.Sprintf(format, args...))
		if want != nil && (len(log) > len(want) || log[len(log)-1] != want[len(log)-1]) {
			panic(diverged{})
		}
	}
	var queued, pending []int // the model: values in the queue, waiting Pushes' values
	closed := false
	next := 0
	type waiter struct {
		v    int
		task *rt.Task
	}
	var waiting []*waiter // in the order they began
	finished := make(chan string, 64)
	// recv and settle wait at most until the test's deadline (testwait):
	// the reference's line proves each wait ends at once, so a box that
	// never ends one (a lost wake, a stop it ignores) says so and diverges
	// instead of hanging.
	recv := func(c <-chan string) string {
		select {
		case s := <-c:
			return s
		case <-testwait.After(tw):
			return "no end before the test's deadline"
		}
	}
	// push is a Push the reference admits at once (room, or a waiting
	// take), run where it cannot hang the trace.
	push := func(v int) string {
		c := make(chan string, 1)
		go func() { c <- fmt.Sprint(b.Push(rt.Root("p"), v)) }()
		return recv(c)
	}
	settle := func(n int) {
		if !testwait.Until(tw, func() bool { return b.waiters() == n }) {
			say("%d parked, want %d", b.waiters(), n)
		}
	}
	// finish waits for one waiting Push to return, whichever it is, and
	// says which and how.
	finish := func(what string) { say("%s: %s", what, recv(finished)) }
	interrupted := func(f func()) (raised bool) {
		defer func() {
			if x := recover(); x != nil {
				if x != rt.Interrupted {
					panic(x)
				}
				raised = true
			}
		}()
		f()
		return false
	}
	// newTaker is the task of a take that waits: half the time the box's
	// bound taker, which waits on the box's wake token as the runner does,
	// else a task that waits on its own stop.
	newTaker := func() *rt.Task {
		t := rt.Root("taker")
		if r.Intn(2) == 0 {
			b.bind(t)
		}
		return t
	}
	// noWait runs an operation that the reference never parks on (one
	// after a Close). If the box parks it instead, it is stopped and its
	// line says so, so a broken box diverges where it would hang.
	noWait := func(op func(t *rt.Task) string) string {
		t := rt.Root("after close")
		got := make(chan string, 1)
		go func() {
			var s string
			if interrupted(func() { s = op(t) }) {
				s = "parked, then stopped"
			}
			got <- s
		}()
		for {
			select {
			case s := <-got:
				return s
			default:
			}
			if b.waiters() > 0 {
				t.Interrupt()
				return "parked: " + <-got
			}
			runtime.Gosched()
		}
	}
	for range 40 {
		switch k := r.Intn(9); {
		case k == 0 && !closed && len(queued) < capacity && len(pending) == 0:
			next++
			say("push %d: %s", next, push(next))
			queued = append(queued, next)
		case k <= 1 && !closed && (len(queued) == capacity || len(pending) > 0):
			// A Push that waits for room.
			next++
			w := &waiter{v: next, task: rt.Root("w")}
			go func() {
				var err error
				if interrupted(func() { err = b.Push(w.task, w.v) }) {
					finished <- fmt.Sprintf("push %d interrupted", w.v)
					return
				}
				finished <- fmt.Sprintf("push %d returned %v", w.v, err)
			}()
			waiting = append(waiting, w)
			pending = append(pending, next)
			settle(len(waiting))
		case k == 2 && len(waiting) > 0:
			// A waiting Push stopped: its value never enters.
			i := r.Intn(len(waiting))
			waiting[i].task.Interrupt()
			finish("stopped")
			waiting = slices.Delete(waiting, i, i+1)
			pending = slices.Delete(pending, i, i+1)
			settle(len(waiting))
		case k == 3:
			v, ok := b.Poll()
			say("poll: %d %v", v, ok)
			switch {
			case len(queued) > 0:
				queued = queued[1:]
				if len(pending) > 0 {
					queued = append(queued, pending[0])
					waiting, pending = waiting[1:], pending[1:]
					finish("accepted")
				}
			case len(pending) > 0:
				waiting, pending = waiting[1:], pending[1:]
				finish("accepted")
			}
			settle(len(waiting))
		case k == 4 && !closed && len(queued) == 0 && len(pending) == 0:
			// A take that waits, then a Push that it takes.
			got := make(chan string, 1)
			taker := newTaker()
			go func() {
				v, err := b.pop(taker)
				got <- fmt.Sprintf("%d %v", v, err)
			}()
			settle(1)
			next++
			say("push %d to a waiting take: %s", next, push(next))
			say("the waiting take: %s", recv(got))
			settle(0)
		case k == 5 && !closed && len(queued) == 0 && len(pending) == 0:
			// A take that waits, stopped.
			taker := newTaker()
			got := make(chan string, 1)
			go func() { got <- fmt.Sprint(interrupted(func() { _, _ = b.pop(taker) })) }()
			settle(1)
			taker.Interrupt()
			say("stopped take raised: %s", recv(got))
			settle(0)
		case k == 6 && r.Intn(4) == 0:
			b.Close()
			say("close")
			closed = true
			// Each waiting Push returns Closed, in any order.
			var ends []string
			for range waiting {
				ends = append(ends, recv(finished))
			}
			slices.Sort(ends)
			say("rejected: %v", ends)
			waiting, pending = nil, nil
			settle(0)
		case k == 8 && len(queued) > 0:
			// A take by a task already stopped raises and takes nothing,
			// though a value is there.
			taker := newTaker()
			taker.Interrupt()
			say("take by a stopped task raised: %v", interrupted(func() { _, _ = b.pop(taker) }))
		case k == 7 && closed:
			next++
			v := next
			say("push %d after close: %s", v, noWait(func(t *rt.Task) string { return fmt.Sprint(b.Push(t, v)) }))
			if len(queued) > 0 {
				say("pop after close: %s", noWait(func(t *rt.Task) string {
					v, err := b.pop(t)
					return fmt.Sprintf("%d %v", v, err)
				}))
				queued = queued[1:]
			} else {
				say("pop of closed and empty: %s", noWait(func(t *rt.Task) string {
					_, err := b.pop(t)
					return fmt.Sprint(err)
				}))
			}
		}
		say("len %d line %d", b.Len(), b.line())
	}
	b.Close()
	var ends []string
	for range waiting {
		ends = append(ends, recv(finished))
	}
	slices.Sort(ends)
	say("rejected at the end: %v", ends)
	for {
		v, ok := b.Poll()
		if !ok {
			break
		}
		say("left %d", v)
	}
	return log
}

// TestMailboxAgreesWithQueue is the mailbox's equality property (MU18):
// over generated traces (seed 1, or ERGO_ACTOR_SEED) at capacities 0 to
// 9, the mailbox gives what the reference Queue gives for each Push, a
// Push that waits for room (taken, stopped or rejected by a Close), a Poll,
// a take that waits (fed or stopped), a Close, the operations after it,
// and Len and the line of waiting Pushes after each step. A failure prints
// the seed and both logs; the mailbox's stops at its first difference.
func TestMailboxAgreesWithQueue(t *testing.T) {
	seed := int64(1)
	if s := os.Getenv("ERGO_ACTOR_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	for i := range 300 {
		caseSeed := seed*100003 + int64(i)
		capacity := i % 10
		want := mailboxTrace(t, caseSeed, capacity, queueBox{New[int](capacity)}, nil)
		got := mailboxTrace(t, caseSeed, capacity, mailBox{newMailbox[int](capacity, false)}, want)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d, case %d (trace seed %d, capacity %d):\nmailbox:\n  %s\nQueue:\n  %s", seed, i, caseSeed, capacity,
				strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}
