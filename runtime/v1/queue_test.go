package v1

import (
	"fmt"
	"iter"
	"math/rand/v2"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// The Go methods the lowering calls for queue.Queue[int]: the waiting
// operations take the caller's task; Poll is Option[int] as Go's comma-ok.
var _ interface {
	Push(*rt.Task, int) error
	Pop(*rt.Task) (int, error)
	End()
	Values(*rt.Task) iter.Seq[int]
	Poll() (int, bool)
	Len() int
} = (*Queue[int])(nil)

// bg is the task of the tests' plain goroutines. It is never stopped and
// never masked, so the operations only read its state.
var bg = rt.Root("queue test")

// closed reports whether err is the queue.Closed failure, and fails the
// test on any other error.
func closed(t *testing.T, err error) bool {
	t.Helper()
	if err != nil && err != (Closed{}) {
		t.Fatalf("failed with %#v; want queue.Closed or nothing", err)
	}
	return err == (Closed{})
}

// push pushes onto a queue that must take the value.
func push[T any](t *testing.T, q *Queue[T], v T) {
	t.Helper()
	if err := q.Push(bg, v); err != nil {
		t.Fatalf("Push(%v) failed with %v", v, err)
	}
}

// pop pops from a queue that must hold a value.
func pop[T any](t *testing.T, q *Queue[T]) T {
	t.Helper()
	v, err := q.Pop(bg)
	if err != nil {
		t.Fatalf("Pop failed with %v", err)
	}
	return v
}

// poll is q.Poll() with a deadline: Poll never waits (runtime.md, Queues 9),
// so a Poll that has not returned after 10s fails the test.
func poll[T any](t *testing.T, q *Queue[T]) (T, bool) {
	t.Helper()
	type result struct {
		v  T
		ok bool
	}
	done := make(chan result, 1)
	go func() {
		v, ok := q.Poll()
		done <- result{v, ok}
	}()
	select {
	case r := <-done:
		return r.v, r.ok
	case <-testwait.After(t):
		t.Fatal("Poll did not return by the test's deadline: it waits")
		var zero T
		return zero, false
	}
}

// TestQueuePoll walks the rules of Poll and Len (runtime.md, Queues 9 and
// 10).
func TestQueuePoll(t *testing.T) {
	t.Run("empty is none, open or closed", func(t *testing.T) {
		for _, capacity := range []int{0, 1, 3} {
			q := New[int](capacity)
			if v, ok := poll(t, q); ok {
				t.Fatalf("capacity %d: Poll on an open, empty queue = %d, true", capacity, v)
			}
			q.End()
			if v, ok := poll(t, q); ok {
				t.Fatalf("capacity %d: Poll on a closed, empty queue = %d, true", capacity, v)
			}
			if n := q.Len(); n != 0 {
				t.Fatalf("capacity %d: Len of an empty queue = %d", capacity, n)
			}
		}
	})
	t.Run("takes the front, also after Close", func(t *testing.T) {
		q := New[int](3)
		push(t, q, 1)
		push(t, q, 2)
		push(t, q, 3)
		if n := q.Len(); n != 3 {
			t.Fatalf("Len() = %d; want 3", n)
		}
		if v, ok := poll(t, q); !ok || v != 1 {
			t.Fatalf("Poll() = %d, %v; want 1, true", v, ok)
		}
		q.End()
		if v, ok := poll(t, q); !ok || v != 2 {
			t.Fatalf("Poll() after Close = %d, %v; want 2, true", v, ok)
		}
		if got := pop(t, q); got != 3 {
			t.Fatalf("Pop() = %d; want 3", got)
		}
		if _, ok := poll(t, q); ok || q.Len() != 0 {
			t.Fatal("Poll on the drained queue took a value")
		}
	})
	t.Run("the room goes to a waiting Push", func(t *testing.T) {
		q := New[int](1)
		push(t, q, 1)
		done := make(chan error, 1)
		go func() { done <- q.Push(bg, 2) }()
		parked(t, q, 1)
		if n := q.Len(); n != 1 {
			t.Fatalf("Len() with a waiting Push = %d; want 1: its value has not entered", n)
		}
		if v, ok := poll(t, q); !ok || v != 1 {
			t.Fatalf("Poll() = %d, %v; want 1, true", v, ok)
		}
		if err := result(t, done); err != nil {
			t.Fatalf("the waiting Push failed with %v", err)
		}
		if v, ok := poll(t, q); !ok || v != 2 {
			t.Fatalf("Poll() = %d, %v; want 2, true", v, ok)
		}
	})
	t.Run("capacity 0 takes a waiting Push", func(t *testing.T) {
		q := New[string](0)
		done := make(chan error, 1)
		go func() { done <- q.Push(bg, "v") }()
		parked(t, q, 1)
		if n := q.Len(); n != 0 {
			t.Fatalf("Len() of a rendezvous queue = %d; want 0", n)
		}
		if v, ok := poll(t, q); !ok || v != "v" {
			t.Fatalf("Poll() = %q, %v; want v, true", v, ok)
		}
		if err := result(t, done); err != nil {
			t.Fatalf("the Push whose value Poll took failed with %v", err)
		}
		if _, ok := poll(t, q); ok {
			t.Fatal("Poll took a second value from one Push")
		}
	})
}

// BenchmarkQueuePoll times Poll and Len beside their hand-written Go
// control, a buffered channel: a Push then a Poll (a send, then a receive
// in a select with a default case), a Poll of an empty queue (the same
// select on an empty channel), and Len (len of the channel). One goroutine,
// no contention.
func BenchmarkQueuePoll(b *testing.B) {
	b.Run("PushPoll/ergo", func(b *testing.B) {
		q := New[int](1)
		b.ReportAllocs()
		for i := range b.N {
			_ = q.Push(bg, i)
			if _, ok := q.Poll(); !ok {
				b.Fatal("Poll found no value")
			}
		}
	})
	b.Run("PushPoll/go", func(b *testing.B) {
		ch := make(chan int, 1)
		b.ReportAllocs()
		for i := range b.N {
			ch <- i
			select {
			case <-ch:
			default:
				b.Fatal("the receive found no value")
			}
		}
	})
	b.Run("PollEmpty/ergo", func(b *testing.B) {
		q := New[int](1)
		b.ReportAllocs()
		for range b.N {
			if _, ok := q.Poll(); ok {
				b.Fatal("Poll found a value")
			}
		}
	})
	b.Run("PollEmpty/go", func(b *testing.B) {
		ch := make(chan int, 1)
		b.ReportAllocs()
		for range b.N {
			select {
			case <-ch:
				b.Fatal("the receive found a value")
			default:
			}
		}
	})
	b.Run("Len/ergo", func(b *testing.B) {
		q := New[int](4)
		_ = q.Push(bg, 1)
		b.ReportAllocs()
		n := 0
		for range b.N {
			n += q.Len()
		}
		queueBenchSink = n
	})
	b.Run("Len/go", func(b *testing.B) {
		ch := make(chan int, 4)
		ch <- 1
		b.ReportAllocs()
		n := 0
		for range b.N {
			n += len(ch)
		}
		queueBenchSink = n
	})
}

// BenchmarkQueuePushPop times a Push then a Pop on one goroutine, which
// S3d moved into front(), beside a send and a receive on a buffered
// channel.
func BenchmarkQueuePushPop(b *testing.B) {
	b.Run("ergo", func(b *testing.B) {
		q := New[int](1)
		b.ReportAllocs()
		n := 0
		for i := range b.N {
			_ = q.Push(bg, i)
			v, _ := q.Pop(bg)
			n += v
		}
		queueBenchSink = n
	})
	b.Run("go", func(b *testing.B) {
		ch := make(chan int, 1)
		b.ReportAllocs()
		n := 0
		for i := range b.N {
			ch <- i
			n += <-ch
		}
		queueBenchSink = n
	})
}

// queueBenchSink keeps a benchmark's result alive.
var queueBenchSink int

// TestQueueContract walks each rule of the queue contract (runtime.md,
// Queues).
func TestQueueContract(t *testing.T) {
	t.Run("FIFO", func(t *testing.T) {
		q := New[int](3)
		push(t, q, 1)
		push(t, q, 2)
		push(t, q, 3)
		for want := 1; want <= 3; want++ {
			if got := pop(t, q); got != want {
				t.Fatalf("Pop() = %d; want %d", got, want)
			}
		}
	})
	t.Run("backpressure", func(t *testing.T) {
		q := New[int](1)
		push(t, q, 1)
		done := make(chan error, 1)
		go func() { done <- q.Push(bg, 2) }()
		parked(t, q, 1) // Push into a full queue waits
		if got := pop(t, q); got != 1 {
			t.Fatalf("Pop() = %d; want 1", got)
		}
		if err := result(t, done); err != nil {
			t.Fatalf("the waiting Push failed with %v", err)
		}
		if got := pop(t, q); got != 2 {
			t.Fatalf("Pop() = %d; want 2", got)
		}
	})
	t.Run("capacity 0 is a rendezvous", func(t *testing.T) {
		q := New[string](0)
		done := make(chan error, 1)
		go func() { done <- q.Push(bg, "v") }()
		parked(t, q, 1) // the Push waits for the Pop that takes its value
		if got := pop(t, q); got != "v" {
			t.Fatalf("Pop() = %q; want v", got)
		}
		if err := result(t, done); err != nil {
			t.Fatalf("the Push failed with %v", err)
		}
	})
	t.Run("negative capacity", func(t *testing.T) {
		p := recovered(func() { New[int](-1) })
		if s, _ := p.(string); s != "queue.New: negative capacity -1" {
			t.Fatalf("New(-1) raised %#v; want the defect", p)
		}
	})
	t.Run("Push after Close", func(t *testing.T) {
		q := New[int](2)
		q.End()
		if !closed(t, q.Push(bg, 1)) {
			t.Fatal("Push on a closed queue did not fail with queue.Closed")
		}
		if _, err := q.Pop(bg); !closed(t, err) {
			t.Fatal("the rejected value entered the queue")
		}
	})
	t.Run("Pop drains, then fails", func(t *testing.T) {
		q := New[int](2)
		push(t, q, 1)
		push(t, q, 2)
		q.End()
		q.End() // idempotent
		if a, b := pop(t, q), pop(t, q); a != 1 || b != 2 {
			t.Fatalf("Pop, Pop = %d, %d; want 1, 2", a, b)
		}
		if _, err := q.Pop(bg); !closed(t, err) {
			t.Fatal("Pop on a closed, empty queue did not fail with queue.Closed")
		}
		if (Closed{}).Error() != "queue.Closed" {
			t.Fatalf("the failure's text is %q", Closed{}.Error())
		}
	})
	t.Run("a waiting Push fails at Close", func(t *testing.T) {
		q := New[int](1)
		push(t, q, 1)
		done := make(chan error, 1)
		go func() { done <- q.Push(bg, 2) }()
		parked(t, q, 1)
		q.End()
		if err := result(t, done); err != (Closed{}) {
			t.Fatalf("the waiting Push gave %v; want queue.Closed", err)
		}
		var got []int
		for v := range q.Values(bg) {
			got = append(got, v)
		}
		if fmt.Sprint(got) != "[1]" {
			t.Fatalf("drained %v; want [1]: the failed Push's value never enters", got)
		}
	})
	t.Run("a waiting Pop fails at Close", func(t *testing.T) {
		q := New[int](1)
		done := make(chan error, 1)
		go func() {
			_, err := q.Pop(bg)
			done <- err
		}()
		parked(t, q, 1)
		q.End()
		if err := result(t, done); err != (Closed{}) {
			t.Fatalf("the waiting Pop gave %v; want queue.Closed", err)
		}
	})
	t.Run("Values drains and ends at Close", func(t *testing.T) {
		q := New[int](4)
		producer := make(chan struct{})
		defer func() { <-producer }()
		go func() {
			defer close(producer)
			for i := range 100 {
				_ = q.Push(bg, i)
			}
			q.End()
		}()
		n := 0
		for v := range q.Values(bg) {
			if v != n {
				t.Fatalf("value %d; want %d", v, n)
			}
			n++
		}
		if n != 100 {
			t.Fatalf("%d values; want 100", n)
		}
	})
	t.Run("Values stops early", func(t *testing.T) {
		q := New[int](4)
		for i := 1; i <= 4; i++ {
			push(t, q, i)
		}
		var got []int
		for v := range q.Values(bg) {
			got = append(got, v)
			if len(got) == 2 {
				break
			}
		}
		if fmt.Sprint(got) != "[1 2]" || pop(t, q) != 3 {
			t.Fatalf("took %v; want [1 2], then 3 still queued", got)
		}
	})
	t.Run("Close never waits", func(t *testing.T) {
		q := New[int](0)
		done := make(chan error, 1)
		go func() { done <- q.Push(bg, 1) }()
		parked(t, q, 1)
		q.End() // returns while a Push waits: it never waits itself
		if err := result(t, done); err != (Closed{}) {
			t.Fatalf("the waiting Push gave %v; want queue.Closed", err)
		}
	})
	t.Run("a closed queue is a typed failure, not an interruption", func(t *testing.T) {
		// The review's case (stage 3a round 1, m1): a child that pops a
		// closed, empty queue fails with queue.Closed; its owner receives
		// it from Wait as a typed failure, and main exits 1, not 130.
		q := New[int](1)
		q.End()
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				c := Go(s, "consumer", func(ct *rt.Task) (int, error) { return q.Pop(ct) })
				_, err := c.Wait()
				return err
			})
		})
		var out strings.Builder
		if code := report(&out, err); err != (Closed{}) || code != 1 || out.String() != "error: queue.Closed\n" {
			t.Errorf("exit %v, status %d, report %q; want queue.Closed, 1", err, code, out.String())
		}
	})
}

// parked waits until n operations wait in q. It reads the count under q's
// mutex, so each of them has released the lock and holds the wake channel
// that the next change closes: the change it waits for then reaches the
// wait, not the fast path. The deadline only fails the setup.
func parked[T any](t *testing.T, q *Queue[T], n int) {
	t.Helper()
	k := 0
	if !testwait.Until(t, func() bool {
		q.mu.Lock()
		k = q.parked
		q.mu.Unlock()
		return k >= n
	}) {
		t.Fatalf("setup: %d operations wait in the queue at the test's deadline; want %d", k, n)
	}
}

// result receives an operation's result; the deadline fails a wait that
// nothing woke.
func result(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-testwait.After(t):
		t.Fatal("the operation did not return by the test's deadline: nothing woke it")
		return nil
	}
}

// TestQueueModel compares the queue with a model on random sequences of
// operations from one goroutine: Push, Pop, Poll, Close, and Len before each
// step. The model is a FIFO list, a capacity and a closed flag; at the end
// the queue is emptied by Values after End, or by Drain (the release of a
// queue of resources, #197 L8 B3), which must also end admission. An operation
// that would wait is skipped: the interleavings test covers waits. Poll is
// never skipped, since it never waits.
func TestQueueModel(t *testing.T) {
	for seed := uint64(1); seed <= 300; seed++ {
		rng := rand.New(rand.NewPCG(seed, 1))
		capacity := rng.IntN(4)
		q := New[int](capacity)
		var model []int
		isClosed := false
		next := 0
		for step := range 40 {
			if n := q.Len(); n != len(model) || n > capacity {
				t.Fatalf("seed %d step %d: Len() = %d; want %d (capacity %d)", seed, step, n, len(model), capacity)
			}
			switch rng.IntN(7) {
			case 5, 6:
				// Poll takes the front or is none, open or closed, and
				// never waits.
				v, ok := poll(t, q)
				switch {
				case len(model) > 0 && (!ok || v != model[0]):
					t.Fatalf("seed %d step %d: Poll() = %d, %v; want %d, true", seed, step, v, ok, model[0])
				case len(model) == 0 && ok:
					t.Fatalf("seed %d step %d: Poll() on an empty queue = %d, true", seed, step, v)
				}
				if len(model) > 0 {
					model = model[1:]
				}
			case 0, 1:
				if isClosed {
					if !closed(t, q.Push(bg, next)) {
						t.Fatalf("seed %d step %d: Push on a closed queue did not fail with queue.Closed", seed, step)
					}
				} else if len(model) < capacity {
					push(t, q, next)
					model = append(model, next)
				}
				next++
			case 2, 3:
				switch {
				case len(model) > 0:
					if got := pop(t, q); got != model[0] {
						t.Fatalf("seed %d step %d: Pop() = %d; want %d", seed, step, got, model[0])
					}
					model = model[1:]
				case isClosed:
					if _, err := q.Pop(bg); !closed(t, err) {
						t.Fatalf("seed %d step %d: Pop on a closed, empty queue did not fail with queue.Closed", seed, step)
					}
				}
			case 4:
				if rng.IntN(4) == 0 {
					q.End()
					isClosed = true
				}
			}
		}
		var rest []int
		if seed%2 == 0 {
			// The release of a queue of resources (#197 L8 B3, Q7): Drain
			// ends admission and gives every value left, oldest first.
			rest = q.Drain()
			if !closed(t, q.Push(bg, next)) {
				t.Fatalf("seed %d: Push after Drain did not fail with queue.Closed: the drain left admission open", seed)
			}
		} else {
			q.End()
			for v := range q.Values(bg) {
				rest = append(rest, v)
			}
		}
		if fmt.Sprint(rest) != fmt.Sprint(append([]int{}, model...)) && !(len(rest) == 0 && len(model) == 0) {
			t.Fatalf("seed %d: drained %v; want %v", seed, rest, model)
		}
	}
}

// item is a value pushed by one producer: its producer and its number.
type item struct{ p, i int }

// TestQueueInterleavings is the generated interleaving test: for each seed,
// random capacity, producers, consumers (by Pop, by Values or by Poll, some
// stopping early) and a Close at a random point run concurrently. Every
// value whose Push returned is taken exactly once; no value whose Push failed is
// ever taken; each consumer sees each producer's values in push order (FIFO),
// and the values left after every consumer stopped come after all of them.
func TestQueueInterleavings(t *testing.T) {
	base := runtime.NumGoroutine()
	seeds := uint64(300)
	if testing.Short() {
		seeds = 50
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		done := make(chan struct{})
		go func() {
			defer close(done)
			interleave(t, seed)
		}()
		select {
		case <-done:
		case <-testwait.After(t):
			buf := make([]byte, 1<<16)
			t.Fatalf("seed %d did not end:\n%s", seed, buf[:runtime.Stack(buf, true)])
		}
		if t.Failed() {
			return
		}
	}
	waitForGoroutines(t, base)
}

// TestQueueStress is the stage 3a round 1 review's stress test: more
// producers and consumers and more values than the interleavings test, with
// a Close at a random moment. Every value whose Push returned nil is taken
// exactly once, by a consumer or the final drain; no value whose Push failed
// is taken; each consumer sees each producer's values in order.
func TestQueueStress(t *testing.T) {
	base := runtime.NumGoroutine()
	seeds := uint64(60)
	if testing.Short() {
		seeds = 10
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewPCG(seed, 9))
		capacity := []int{0, 1, 2, 8}[rng.IntN(4)]
		producers, consumers, per := 2+rng.IntN(5), 2+rng.IntN(5), 50+rng.IntN(300)
		q := New[item](capacity)
		var mu sync.Mutex
		accepted, rejected := map[item]bool{}, map[item]bool{}
		taken := make([][]item, consumers)
		var wg sync.WaitGroup
		for p := range producers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range per {
					v := item{p, i}
					if closedQuiet(q.Push(bg, v)) {
						mu.Lock()
						rejected[v] = true
						mu.Unlock()
						return
					}
					mu.Lock()
					accepted[v] = true
					mu.Unlock()
				}
			}()
		}
		for c := range consumers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if c%2 == 0 {
					for v := range q.Values(bg) {
						taken[c] = append(taken[c], v)
					}
					return
				}
				for {
					v, err := q.Pop(bg)
					if closedQuiet(err) {
						return
					}
					taken[c] = append(taken[c], v)
				}
			}()
		}
		time.Sleep(time.Duration(rng.IntN(3000)) * time.Microsecond)
		q.End()
		wg.Wait()
		seen := map[item]int{}
		for c, list := range taken {
			last := map[int]int{}
			for _, v := range list {
				seen[v]++
				if l, ok := last[v.p]; ok && v.i <= l {
					t.Fatalf("seed %d: consumer %d took %v after %d (FIFO)", seed, c, v, l)
				}
				last[v.p] = v.i
			}
		}
		for v := range q.Values(bg) {
			seen[v]++
		}
		for v, n := range seen {
			switch {
			case n != 1:
				t.Fatalf("seed %d: %v taken %d times", seed, v, n)
			case rejected[v]:
				t.Fatalf("seed %d: %v was taken, but its Push failed", seed, v)
			case !accepted[v]:
				t.Fatalf("seed %d: %v was taken, but no Push of it returned", seed, v)
			}
		}
		for v := range accepted {
			if seen[v] != 1 {
				t.Fatalf("seed %d: delivered %v was taken %d times", seed, v, seen[v])
			}
		}
	}
	waitForGoroutines(t, base)
}

func interleave(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 2))
	capacity := rng.IntN(4)
	producers, consumers := 1+rng.IntN(3), 1+rng.IntN(3)
	perProducer := rng.IntN(25)
	closeAfter := int64(rng.IntN(producers*perProducer + 1))
	q := New[item](capacity)

	var (
		mu            sync.Mutex
		accepted      = map[item]bool{}
		rejected      = map[item]bool{}
		taken         = make([][]item, consumers)
		pops          atomic.Int64
		producersDone atomic.Int64
		consumersDone atomic.Int64
		wg            sync.WaitGroup
		yieldProducer = make([]bool, producers)
		consumerKind  = make([]int, consumers) // 0 Pop, 1 Values, 2 Poll
		lenErr        atomic.Value
		consumerLimit = make([]int, consumers)
	)
	for p := range yieldProducer {
		yieldProducer[p] = rng.IntN(2) == 0
	}
	for c := range consumers {
		consumerKind[c] = rng.IntN(3)
		consumerLimit[c] = -1
		if rng.IntN(3) == 0 {
			consumerLimit[c] = rng.IntN(perProducer + 1)
		}
	}

	for p := range producers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer producersDone.Add(1)
			for i := range perProducer {
				it := item{p, i}
				if closedQuiet(q.Push(bg, it)) {
					mu.Lock()
					rejected[it] = true
					mu.Unlock()
					return
				}
				mu.Lock()
				accepted[it] = true
				mu.Unlock()
				if yieldProducer[p] {
					runtime.Gosched()
				}
			}
		}()
	}
	for c := range consumers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer consumersDone.Add(1)
			keep := func(it item) bool {
				taken[c] = append(taken[c], it)
				pops.Add(1)
				return consumerLimit[c] < 0 || len(taken[c]) < consumerLimit[c]
			}
			if consumerLimit[c] == 0 {
				return
			}
			switch consumerKind[c] {
			case 1:
				for it := range q.Values(bg) {
					if !keep(it) {
						return
					}
				}
				return
			case 2:
				// A poller never waits in the queue: it polls until the
				// queue is closed and empty, and checks Len between.
				for {
					if n := q.Len(); n < 0 || n > capacity {
						lenErr.CompareAndSwap(nil, fmt.Sprintf("Len() = %d with capacity %d", n, capacity))
					}
					it, ok := q.Poll()
					if ok {
						if !keep(it) {
							return
						}
						continue
					}
					q.mu.Lock()
					end := q.closed && len(q.buf) == 0 && len(q.senders) == 0
					q.mu.Unlock()
					if end {
						return
					}
					runtime.Gosched()
				}
			}
			for {
				it, err := q.Pop(bg)
				if closedQuiet(err) {
					return
				}
				if !keep(it) {
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for pops.Load() < closeAfter && producersDone.Load() < int64(producers) && consumersDone.Load() < int64(consumers) {
			runtime.Gosched()
		}
		q.End()
	}()
	wg.Wait()
	if e := lenErr.Load(); e != nil {
		t.Errorf("seed %d: %s", seed, e)
	}

	var rest []item
	for it := range q.Values(bg) {
		rest = append(rest, it)
	}

	seen := map[item]int{}
	for c := range taken {
		last := map[int]int{}
		for _, it := range taken[c] {
			seen[it]++
			if l, ok := last[it.p]; ok && it.i <= l {
				t.Errorf("seed %d: consumer %d took %v after %d of the same producer (FIFO)", seed, c, it, l)
			}
			last[it.p] = it.i
		}
	}
	maxTaken := map[int]int{}
	for it := range seen {
		if m, ok := maxTaken[it.p]; !ok || it.i > m {
			maxTaken[it.p] = it.i
		}
	}
	lastRest := map[int]int{}
	for _, it := range rest {
		seen[it]++
		if m, ok := maxTaken[it.p]; ok && it.i <= m {
			t.Errorf("seed %d: %v was left in the queue behind a later value of its producer (FIFO)", seed, it)
		}
		if l, ok := lastRest[it.p]; ok && it.i <= l {
			t.Errorf("seed %d: the rest has %v after %d (FIFO)", seed, it, l)
		}
		lastRest[it.p] = it.i
	}
	for it, n := range seen {
		switch {
		case n > 1:
			t.Errorf("seed %d: %v was taken %d times", seed, it, n)
		case rejected[it]:
			t.Errorf("seed %d: %v was taken, but its Push failed with queue.Closed", seed, it)
		case !accepted[it]:
			t.Errorf("seed %d: %v was taken, but no Push of it returned", seed, it)
		}
	}
	var lost []string
	for it := range accepted {
		if seen[it] == 0 {
			lost = append(lost, fmt.Sprint(it))
		}
	}
	if len(lost) > 0 {
		sort.Strings(lost)
		t.Errorf("seed %d (capacity %d, %d producers, %d consumers): delivered but never taken: %s", seed, capacity, producers, consumers, strings.Join(lost, " "))
	}
}

// closedQuiet is closed for another goroutine, where t.Fatalf may not run:
// any error other than queue.Closed panics.
func closedQuiet(err error) bool {
	if err != nil && err != (Closed{}) {
		panic(fmt.Sprintf("queue failed with %#v; want queue.Closed or nothing", err))
	}
	return err != nil
}
