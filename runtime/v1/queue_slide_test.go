package v1

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// TestQueueSlideIsOneStep is the smallest repro of a slide built from two
// steps (make room with Poll, then Offer; std queue.Slide before
// SELFHOST-MERGE r2): with a Push waiting for room, the Poll that makes
// room hands it to the waiting Push, whose value then enters and is
// evicted as if it were the oldest. Slide evicts the front under the
// queue's lock and enters v in the same step, so the waiting Push keeps
// waiting and fails Closed at End, never delivered and dropped. Mutant
// slide-two-step. Slide gives the evicted front back to its caller, who
// owns it (resources R42): mutant slide-evicted-not-given-back.
func TestQueueSlideIsOneStep(t *testing.T) {
	q := New[int](1)
	push(t, q, 1)
	done := make(chan error, 1)
	go func() { done <- q.Push(bg, 2) }()
	// The Push of 2 waits for room: wait for it to join the line.
	deadline := testwait.After(t)
	for {
		q.mu.Lock()
		n := len(q.senders)
		q.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Push(2) into a full queue returned %v without waiting", err)
		case <-deadline:
			t.Fatal("Push(2) never waited for room")
		default:
			runtime.Gosched()
		}
	}
	old, evicted, err := q.Slide(3)
	if err != nil {
		t.Fatalf("Slide(3) failed with %v", err)
	}
	if !evicted || old != 1 {
		t.Fatalf("Slide(3) gave back %v, %v; want 1, the evicted front, for its caller to own (SLIDE-RELEASE)", old, evicted)
	}
	if v, ok := poll(t, q); !ok || v != 3 {
		t.Fatalf("after Slide(3) Poll gives %v, %v; want 3: the slid value is the newest", v, ok)
	}
	// 2's Push takes the room Poll made: it was never dropped.
	if err := <-done; err != nil {
		t.Fatalf("the waiting Push(2) failed with %v; Slide must not take it in and drop it", err)
	}
	if v, ok := poll(t, q); !ok || v != 2 {
		t.Fatalf("Poll gives %v, %v; want 2, the waiting Push's value", v, ok)
	}
}

// TestQueueSlideLosesNothing is the law of sliding producers: several
// producers slide into one queue at once, and every value slid is either
// still in the queue at the end or was evicted from its front, exactly
// once; each producer's values still in the queue are the newest it slid,
// in order (a value is never lost while a newer one of its producer
// stays). Seeded; a failing seed names itself.
func TestQueueSlideLosesNothing(t *testing.T) {
	seeds := uint64(40)
	if testing.Short() {
		seeds = 8
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewPCG(seed, 11))
		capacity := 1 + rng.IntN(4)
		producers, per := 2+rng.IntN(4), 20+rng.IntN(200)
		q := New[item](capacity)
		var mu sync.Mutex
		count := map[item]int{}
		var wg sync.WaitGroup
		for p := range producers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range per {
					old, dropped, err := q.slide(item{p, i})
					if closedQuiet(err) {
						panic(fmt.Sprintf("seed %d: Slide on an open queue failed Closed", seed))
					}
					if dropped {
						mu.Lock()
						count[old]++
						mu.Unlock()
					}
				}
			}()
		}
		wg.Wait()
		q.End()
		last := map[int]int{}
		for v := range q.Values(bg) {
			count[v]++
			if l, ok := last[v.p]; ok && v.i != l+1 {
				t.Fatalf("seed %d: producer %d's values in the queue skip from %d to %d: one was lost while a newer one stays", seed, v.p, l, v.i)
			}
			last[v.p] = v.i
		}
		for p, l := range last {
			if l != per-1 {
				t.Fatalf("seed %d: producer %d's newest value in the queue is %d, not its last, %d", seed, p, l, per-1)
			}
		}
		for p := range producers {
			for i := range per {
				if n := count[item{p, i}]; n != 1 {
					t.Fatalf("seed %d (ERGO_SEED replay: capacity %d, %d producers, %d each): %v was evicted or kept %d times; want once", seed, capacity, producers, per, item{p, i}, n)
				}
			}
		}
	}
}
