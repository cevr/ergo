package rt

import (
	"runtime"
	"testing"
)

// TestParkTwiceFailsLoudly parks one goroutine on a parker of each body
// and then parks a second on it: the second panics with parkTwice, and the
// first still sleeps, so the wake that follows reaches it. Dropping either
// goroutine would leave it asleep for good (two goroutines in waits of one
// task on its own word, WAITOR-WORD). Mutants (testdata/mutants.json):
// park-commit-drops-parker, stock-park-drops-parker.
func TestParkTwiceFailsLoudly(t *testing.T) {
	for name, mk := range parkBodies {
		t.Run(name, func(t *testing.T) {
			p := mk()
			first := make(chan struct{})
			go func() {
				defer close(first)
				p.park()
			}()
			for !p.parkedNow() {
				runtime.Gosched()
			}
			func() {
				defer func() {
					if r := recover(); r != parkTwice {
						t.Errorf("a second park on a parker one goroutine sleeps on recovered %v; want the parkTwice panic", r)
					}
				}()
				p.park()
			}()
			if !p.parkedNow() {
				t.Fatalf("the first goroutine no longer sleeps on the parker: the second park dropped it")
			}
			p.unpark()
			<-first
		})
	}
}

// TestWaiterStopReachesSharedWaits parks two goroutines of one task, each
// on its own Waiter, as Go code that runs two goroutines on one task makes
// two queue waits (runtime/v1 Queue.waitOr): one Waiter holds the task's
// slot, the other is in moreWaits. A stop of the task ends both waits.
// Mutants (testdata/mutants.json): waiter-stop-skips-slot,
// waiter-stop-skips-more.
func TestWaiterStopReachesSharedWaits(t *testing.T) {
	task := Root("shared")
	var ws [2]Waiter
	done := make(chan int, len(ws))
	for i := range ws {
		go func() {
			for !task.Stopped() {
				ws[i].Park(task, true)
			}
			done <- i
		}()
	}
	for !ws[0].p.parkedNow() || !ws[1].p.parkedNow() {
		runtime.Gosched()
	}
	task.Interrupt()
	for range ws {
		<-done
	}
	moreWaits.mu.Lock()
	left := len(moreWaits.m)
	moreWaits.mu.Unlock()
	if left != 0 || task.waiting.Load() != nil {
		t.Fatalf("ended waits left %d tasks in moreWaits and slot %p; want none", left, task.waiting.Load())
	}
}
