package v1

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestUnreachedPointStopsTheRun: a point armed and never reached does not
// leave its test waiting (RT-HOOK). The arming task waits for a task that
// waits for the release the point's function would push; once both are
// parked on the clock's domain, the run stops with the defect that names
// the point. Here the defect only ends the queue, so the test sees the
// message and ends. Mutant (hook-unreached-unwatched): HookAt starts no
// stall watch, and the test waits until go test's -timeout names it.
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
	_ = NewTimeline(root, 0, 0) // a Manual: root and its children are its domain
	sc := NewScope(root)
	HookAt(root, "rt-hook.never", func(ct *rt.Task) { _ = q.Push(ct, 1) })
	waiter := ownedGo(&sc, Site("waiter"), func(ct *rt.Task) (bool, error) {
		_, err := q.Pop(ct)
		return err == nil, nil
	})
	if released, err := waiter.Wait(); err != nil || released {
		t.Fatalf("the waiter ended with %v, %v; want the queue ended by the defect", released, err)
	}
	sc.End(nil)
	if msg := <-got; msg != `testing.At: point "rt-hook.never" was never reached: every task waits, and none can reach it` {
		t.Fatalf("defect %q", msg)
	}
	HookPoint(root, "rt-hook.never") // disarm it for the tests after
}
