package v1

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestUnreachedPointStopsTheRun: a point armed and never reached does not
// leave its test waiting (RT-HOOK). A task arms a point on a Manual
// clock's domain and waits for a task that waits for the release the
// point's function would push. The test, outside the domain, waits for
// the domain to settle (every member parked, the event an Advance waits
// for), has the stall watch report once more (rt.FlushStalls), and then
// holds that the run's stop named the point. Here the stop only records
// the message and ends the queue, so the tasks end. Mutant
// (hook-unreached-unwatched): HookAt starts no stall watch, so no stop
// came, and the test fails on that, with no wait.
func TestUnreachedPointStopsTheRun(t *testing.T) {
	q := New[int](1)
	got := make(chan string, 1)
	old := hookDefect
	hookDefect = func(msg string) {
		select {
		case got <- msg:
		default:
		}
		q.End()
	}
	defer func() { hookDefect = old }()
	root := rt.Root("stall")
	sc := NewScope(root)
	lines := make(chan *Timeline, 1)
	arming := ownedGo(&sc, Site("arming"), func(ct *rt.Task) (bool, error) {
		line := NewTimeline(ct, 0, 0) // a Manual whose domain is this task and its children
		inner := NewScope(ct)
		HookAt(ct, "rt-hook.never", func(pt *rt.Task) { _ = q.Push(pt, 1) })
		waiter := ownedGo(&inner, Site("waiter"), func(wt *rt.Task) (bool, error) {
			_, err := q.Pop(wt)
			return err == nil, nil
		})
		lines <- line
		released, err := waiter.Wait(ct)
		inner.End(nil)
		return released, err
	})
	line := <-lines
	line.Advance(root, 0) // returns once both tasks of the domain are parked
	watched := rt.FlushStalls()
	var msg string
	select {
	case msg = <-got:
	default:
	}
	q.End()
	if released, err := arming.Wait(root); err != nil || released {
		t.Errorf("the waiter ended with %v, %v; want the queue ended", released, err)
	}
	sc.End(nil)
	HookPoint(root, "rt-hook.never") // disarm it for the tests after
	if want := `testing.At: point "rt-hook.never" was never reached: every task waits, and none can reach it`; !watched || msg != want {
		t.Fatalf("after the domain stalled: watch running %v, stop %q; want the stop %q", watched, msg, want)
	}
}
