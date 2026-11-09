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
}

type queueBox struct{ *Queue[int] }

func (q queueBox) pop(t *rt.Task) (int, error) { return q.Pop(t) }
func (q queueBox) waiters() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.parked
}

type mailBox struct{ *mailbox[int] }

func (m mailBox) pop(t *rt.Task) (int, error) {
	var v int
	err := m.popInto(t, &v)
	return v, err
}

// mailboxTrace runs one generated trace on b and returns what each
// operation gave. The trace depends only on seed and a model of the
// contents (queued values, waiting Pushes, closed), never on b, so the
// Queue and the mailbox get the same operations.
func mailboxTrace(seed int64, capacity int, b box) []string {
	r := rand.New(rand.NewSource(seed))
	var log []string
	var queued, pending []int // the model: values in the queue, waiting Pushes' values
	closed := false
	next := 0
	type waiter struct {
		v    int
		task *rt.Task
		res  chan string
	}
	var waiting []*waiter // in the order they began
	results := map[int]string{}
	settle := func(n int) {
		for b.waiters() != n {
			runtime.Gosched()
		}
	}
	// accept moves the oldest waiting Push into the model's queue, as a
	// take does (its Push returns nil).
	accept := func() {
		w := waiting[0]
		waiting = waiting[1:]
		pending = pending[1:]
		results[w.v] = <-w.res
	}
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
	for range 40 {
		switch k := r.Intn(8); {
		case k == 0 && !closed && len(queued) < capacity && len(pending) == 0:
			next++
			log = append(log, fmt.Sprintf("push %d: %v", next, b.Push(rt.Root("p"), next)))
			queued = append(queued, next)
		case k <= 1 && !closed && (len(queued) == capacity || len(pending) > 0):
			// A Push that waits for room.
			next++
			w := &waiter{v: next, task: rt.Root("w"), res: make(chan string, 1)}
			go func() {
				var err error
				if interrupted(func() { err = b.Push(w.task, w.v) }) {
					w.res <- "interrupted"
					return
				}
				w.res <- fmt.Sprint(err)
			}()
			waiting = append(waiting, w)
			pending = append(pending, next)
			settle(len(waiting))
		case k == 2 && len(waiting) > 0:
			// A waiting Push stopped: its value never enters.
			i := r.Intn(len(waiting))
			w := waiting[i]
			w.task.Interrupt()
			results[w.v] = <-w.res
			waiting = slices.Delete(waiting, i, i+1)
			pending = slices.Delete(pending, i, i+1)
			settle(len(waiting))
		case k == 3:
			v, ok := b.Poll()
			log = append(log, fmt.Sprintf("poll: %d %v", v, ok))
			switch {
			case len(queued) > 0:
				queued = queued[1:]
				if len(pending) > 0 {
					queued = append(queued, pending[0])
					accept()
				}
			case len(pending) > 0:
				accept()
			}
			settle(len(waiting))
		case k == 4 && !closed && len(queued) == 0 && len(pending) == 0:
			// A take that waits, then a Push that it takes.
			got := make(chan string, 1)
			go func() {
				v, err := b.pop(rt.Root("taker"))
				got <- fmt.Sprintf("%d %v", v, err)
			}()
			settle(1)
			next++
			log = append(log, fmt.Sprintf("push %d to a waiting take: %v", next, b.Push(rt.Root("p"), next)))
			log = append(log, "the waiting take: "+<-got)
			settle(0)
		case k == 5 && !closed && len(queued) == 0 && len(pending) == 0:
			// A take that waits, stopped.
			taker := rt.Root("taker")
			got := make(chan bool, 1)
			go func() { got <- interrupted(func() { _, _ = b.pop(taker) }) }()
			settle(1)
			taker.Interrupt()
			log = append(log, fmt.Sprintf("stopped take raised: %v", <-got))
			settle(0)
		case k == 6 && r.Intn(4) == 0:
			b.Close()
			log = append(log, "close")
			closed = true
			for _, w := range waiting {
				results[w.v] = <-w.res
			}
			waiting, pending = nil, nil
			settle(0)
		case k == 7 && closed:
			next++
			log = append(log, fmt.Sprintf("push %d after close: %v", next, b.Push(rt.Root("p"), next)))
			if len(queued) > 0 {
				v, err := b.pop(rt.Root("taker"))
				log = append(log, fmt.Sprintf("pop after close: %d %v", v, err))
				queued = queued[1:]
			} else {
				_, err := b.pop(rt.Root("taker"))
				log = append(log, fmt.Sprintf("pop of closed and empty: %v", err))
			}
		}
		log = append(log, fmt.Sprintf("len %d", b.Len()))
	}
	b.Close()
	for _, w := range waiting {
		results[w.v] = <-w.res
	}
	for {
		v, ok := b.Poll()
		if !ok {
			break
		}
		log = append(log, fmt.Sprintf("left %d", v))
	}
	ids := make([]int, 0, len(results))
	for v := range results {
		ids = append(ids, v)
	}
	slices.Sort(ids)
	for _, v := range ids {
		log = append(log, fmt.Sprintf("push %d returned %s", v, results[v]))
	}
	return log
}

// TestMailboxAgreesWithQueue is the mailbox's equality property (MU18):
// over generated traces (seed 1, or ERGO_ACTOR_SEED) at capacities 0 to
// 9, the mailbox gives what the reference Queue gives for each Push, a
// Push that waits for room (taken, stopped or rejected by a Close), a Poll,
// a take that waits (fed or stopped), a Close, the operations after it,
// and Len. A failure prints the seed and both logs.
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
		want := mailboxTrace(caseSeed, capacity, queueBox{New[int](capacity)})
		got := mailboxTrace(caseSeed, capacity, mailBox{newMailbox[int](capacity, false)})
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d, case %d (trace seed %d, capacity %d):\nmailbox:\n  %s\nQueue:\n  %s", seed, i, caseSeed, capacity,
				strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}
