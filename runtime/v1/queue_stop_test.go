package v1

import (
	"runtime"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// interruptedBy runs op and reports whether it raised rt.Interrupted; any
// other panic is re-raised.
func interruptedBy(op func()) (interrupted bool) {
	defer func() {
		if p := recover(); p != nil {
			if p != rt.Interrupted {
				panic(p)
			}
			interrupted = true
		}
	}()
	op()
	return false
}

// noGoroutinesAbove waits until at most base goroutines run; the deadline
// fails a leak.
func noGoroutinesAbove(t *testing.T, base int) {
	t.Helper()
	if !testwait.Until(t, func() bool { return runtime.NumGoroutine() <= base }) {
		buf := make([]byte, 1<<16)
		t.Fatalf("%d goroutines; want at most %d:\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
	}
}

// TestQueueWaitsEndOnAStop (gap 1 ruling): a Push waiting on a full queue,
// a Pop waiting on an empty one and a Values loop waiting for its next
// value each end when their task is stopped, with an interruption, not
// Closed. The stopped Push's value never enters the queue; the stopped Pop
// takes nothing. A task stopped before the operation begins is interrupted
// at once. Inside a mask the wait is uninterruptible (I2) and ends on the
// queue's change. Each waiter is proven parked (under the queue's mutex)
// before the stop.
func TestQueueWaitsEndOnAStop(t *testing.T) {
	base := runtime.NumGoroutine()

	t.Run("a waiting Push", func(t *testing.T) {
		q := New[int](1)
		push(t, q, 1)
		task := rt.Root("producer")
		done := make(chan bool, 1)
		go func() { done <- interruptedBy(func() { _ = q.Push(task, 2) }) }()
		parked(t, q, 1)
		task.Interrupt()
		if !waitBool(t, done) {
			t.Fatal("the stopped Push returned instead of raising the interruption")
		}
		if got := pop(t, q); got != 1 {
			t.Fatalf("Pop = %d; want 1", got)
		}
		q.End()
		if _, err := q.Pop(bg); !closed(t, err) {
			t.Fatal("the stopped Push's value entered the queue")
		}
	})

	t.Run("a waiting Push at capacity 0", func(t *testing.T) {
		q := New[int](0)
		task := rt.Root("producer")
		done := make(chan bool, 1)
		go func() { done <- interruptedBy(func() { _ = q.Push(task, 7) }) }()
		parked(t, q, 1)
		task.Interrupt()
		if !waitBool(t, done) {
			t.Fatal("the stopped rendezvous Push returned")
		}
		q.mu.Lock()
		waiting := len(q.senders)
		q.mu.Unlock()
		if waiting != 0 {
			t.Fatalf("%d Pushes still in line after the stop; want 0", waiting)
		}
	})

	t.Run("a waiting Pop", func(t *testing.T) {
		q := New[int](1)
		task := rt.Root("consumer")
		done := make(chan bool, 1)
		go func() { done <- interruptedBy(func() { _, _ = q.Pop(task) }) }()
		parked(t, q, 1)
		task.Interrupt()
		if !waitBool(t, done) {
			t.Fatal("the stopped Pop returned instead of raising the interruption")
		}
		push(t, q, 5)
		if got := pop(t, q); got != 5 {
			t.Fatalf("Pop = %d; want 5: the stopped Pop took a value", got)
		}
	})

	t.Run("a waiting Values loop", func(t *testing.T) {
		q := New[int](2)
		push(t, q, 1)
		task := rt.Root("consumer")
		got := make(chan int, 4)
		done := make(chan bool, 1)
		go func() {
			done <- interruptedBy(func() {
				for v := range q.Values(task) {
					got <- v
				}
			})
		}()
		parked(t, q, 1)
		task.Interrupt()
		if !waitBool(t, done) {
			t.Fatal("the stopped Values loop ended normally")
		}
		if len(got) != 1 || <-got != 1 {
			t.Fatal("the loop did not take exactly the queued value before the stop")
		}
	})

	t.Run("a task stopped before it begins", func(t *testing.T) {
		q := New[int](1)
		task := rt.Root("stopped")
		task.Interrupt()
		if !interruptedBy(func() { _ = q.Push(task, 1) }) {
			t.Fatal("Push on a stopped task returned")
		}
		if q.End(); !interruptedBy(func() { _, _ = q.Pop(task) }) {
			t.Fatal("Pop on a stopped task returned")
		}
		if _, err := q.Pop(bg); !closed(t, err) {
			t.Fatal("the stopped Push's value entered the queue")
		}
	})

	t.Run("a masked wait is uninterruptible", func(t *testing.T) {
		q := New[int](1)
		push(t, q, 1)
		task := rt.Root("masked")
		done := make(chan error, 1)
		go func() {
			m := task.Mask()
			defer m.Unmask()
			done <- q.Push(task, 2)
		}()
		parked(t, q, 1)
		task.Interrupt()
		select {
		case err := <-done:
			t.Fatalf("a masked Push ended on the stop with %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		if got := pop(t, q); got != 1 {
			t.Fatalf("Pop = %d; want 1", got)
		}
		if err := result(t, done); err != nil {
			t.Fatalf("the masked Push failed with %v after the Pop made room", err)
		}
		if got := pop(t, q); got != 2 {
			t.Fatalf("Pop = %d; want the masked Push's 2", got)
		}
	})

	noGoroutinesAbove(t, base)
}

// waitBool receives one result; the deadline fails a wait nothing woke.
func waitBool(t *testing.T, done <-chan bool) bool {
	t.Helper()
	select {
	case v := <-done:
		return v
	case <-testwait.After(t):
		t.Fatal("the operation did not end by the test's deadline: the stop did not wake it")
		return false
	}
}

// flushFailed is the poller's typed failure.
type flushFailed struct{}

func (flushFailed) Error() string { return "FlushFailed" }

// TestPollerEnds (gap 1 ruling): the corpus program poller.ergo, lowered
// by hand. Run starts a producer task whose exit action closes the queue
// and which pushes without end, so it waits on the full queue; batch, in
// Run's frame, takes a batch and fails its flush. Run's frame then ends
// with FlushFailed, stops the producer, which is waiting in Push, and
// drains it: the Push wakes with the interruption, the exit action closes
// the queue, and Run returns FlushFailed. No goroutine outlives it. Before
// gap 1's repair the producer kept waiting and Run never returned.
func TestPollerEnds(t *testing.T) {
	base := runtime.NumGoroutine()
	var q *Queue[int]
	run := func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			q = New[int](16)
			producer := Go(s, "producer at poller.ergo:52:17", func(pt *rt.Task) (struct{}, error) {
				frameNoRow(pt, func(ps *Scope) {
					OnExit(ps, func() { q.End() })
					for i := 0; ; i++ {
						if err := q.Push(pt, i); err != nil {
							return // closed: the producer ends
						}
					}
				})
				return struct{}{}, nil
			})
			// batch: wait until the producer waits on the full queue, take
			// one batch of five, then fail the flush.
			parked(t, q, 1)
			batch := 0
			for v := range q.Values(root) {
				_ = v
				if batch++; batch == 5 {
					return flushFailed{}
				}
			}
			_, err := producer.Wait()
			return err
		})
	}
	done := make(chan error, 1)
	go func() { done <- runMain(run) }()
	select {
	case err := <-done:
		if err != (flushFailed{}) {
			t.Fatalf("Run ended with %#v; want FlushFailed", err)
		}
	case <-testwait.After(t):
		t.Fatal("Run did not end by the test's deadline: the producer waiting in Push never saw the stop")
	}
	q.mu.Lock()
	closedNow, waiting := q.closed, q.parked
	q.mu.Unlock()
	if !closedNow || waiting != 0 {
		t.Fatalf("after Run: closed %v, %d waiters; want the producer's exit action to have closed it and nobody waiting", closedNow, waiting)
	}
	noGoroutinesAbove(t, base)
}
