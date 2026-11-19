package v1

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// onExitFrame is a lowered frame that registers one exit action, as the
// std cache's run does on each miss: the frame's scope, its End deferred,
// and OnExit with a block the caller made.
func onExitFrame(t *rt.Task, body func()) (err error) {
	s := NewScope(t)
	defer s.End(&err)
	OnExit(&s, body)
	return nil
}

// TestOnExitAllocatesOnce counts the allocations of one exit action's
// registration and run (ONEXIT-COST): one, the action itself (its entry,
// block and frame together), and none for the frame's scope, which stays
// on the caller's stack. Before, an exit action made five (the frame's
// scope escaping through a closure, the entry, that closure and the
// block's frame), the largest single cost of the std cache's miss path
// (OWN1 cache split H1). The count is of allocations, not time. Mutant
// (onexit-closure-entry): OnExit registers a closure over s again.
func TestOnExitAllocatesOnce(t *testing.T) {
	root := rt.Root("onexit")
	ran := 0
	body := func() { ran++ }
	if got := testing.AllocsPerRun(100, func() {
		if err := onExitFrame(root, body); err != nil {
			t.Fatal(err)
		}
	}); got != 1 {
		t.Errorf("one exit action allocates %v times; want 1", got)
	}
	if ran != 101 {
		t.Errorf("the exit action ran %d times in 101 frames", ran)
	}
}
