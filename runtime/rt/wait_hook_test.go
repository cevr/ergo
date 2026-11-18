package rt

import "testing"

// TestWaitHooksRunOnceAndClear holds WhenWaiting's rules over its two
// stores (FU-timer-task-reuse): the slot, which holds one hook with no
// allocation, and the map, which holds the rest. Each task's hook runs
// once, on its first wait, whichever store holds it; a cleared hook does
// not run; and once every hook ran or was cleared the count is zero, so a
// wait pays one load. A hook stored and cleared alone allocates nothing.
// Mutants wait-hook-slot-stays (a slot hook is not removed when it runs),
// wait-hook-slot-shadows-map (a task whose hook is in the map is taken
// from the slot) and wait-hook-slot-unused (every hook goes to the map)
// are killed by no other rt test.
func TestWaitHooksRunOnceAndClear(t *testing.T) {
	a, b, c := Root("a"), Root("b"), Root("c")
	runs := map[string]int{}
	WhenWaitingHook(a, func() { runs["a"]++ }) // the slot
	WhenWaitingHook(b, func() { runs["b"]++ }) // the map
	WhenWaitingHook(c, func() { runs["c"]++ }) // the map
	ClearWaitHook(c)
	for round := range 2 {
		b.Waits()
		a.Waits()
		c.Waits()
		if runs["a"] != 1 || runs["b"] != 1 || runs["c"] != 0 {
			t.Fatalf("after %d waits of each task, hook runs %v; want a 1, b 1, c 0", round+1, runs)
		}
	}
	if n := waitHooks.n.Load(); n != 0 {
		t.Fatalf("%d hooks counted after every hook ran or was cleared; want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		WhenWaitingHook(a, func() {})
		ClearWaitHook(a)
	}); n != 0 {
		t.Fatalf("a hook stored and cleared alone allocates %v times; want 0", n)
	}
}
