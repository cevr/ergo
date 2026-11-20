package rt

import (
	"testing"
	"time"
)

// TestQuietExitsAllocateNothing counts the allocations of the exits that a
// server pays on every keep-alive request (GX5): a stopped task whose body
// ended with nothing or with the interruption, and a live sleep that ran
// its length. Each is in canonical form with no Cause, and a live sleep
// reuses the host timer of an ended one. It counts work, never time.
//
// Mutants it kills (testdata/mutants.json): gx5-exit-builds-cause (the
// interruption-only exit through the general Cause path) and
// gx5-live-timer-fresh (a new host timer for each live sleep), and
// c5-poll-hook-allocates (the hook bound with WithValue, not in its
// PollHook).
func TestQuietExitsAllocateNothing(t *testing.T) {
	stopped := Root("stopped")
	stopped.Interrupt()
	quit := func(*Task) (int, error) { return 0, Interrupted }
	done := func(*Task) (int, error) { return 0, nil }
	var errs [2]error
	exits := testing.AllocsPerRun(100, func() {
		_, errs[0] = Run(stopped, quit)
		_, errs[1] = Run(stopped, done)
	})
	if errs[0] != Interrupted || errs[1] != Interrupted {
		t.Fatalf("a stopped task's exits: %v, %v; want Interrupted twice", errs[0], errs[1])
	}
	if exits != 0 {
		t.Fatalf("a stopped task's quiet exits allocate %v times, want 0", exits)
	}
	running := Root("running")
	var slept error
	sleeps := testing.AllocsPerRun(100, func() { slept = Sleep(running, Live{}, time.Nanosecond) })
	if slept != nil {
		t.Fatalf("a live sleep that ran its length: %v", slept)
	}
	if sleeps != 0 {
		t.Fatalf("a live sleep allocates %v times, want 0", sleeps)
	}
	// The HTTP engine arms a poll hook for every request and ends it
	// (HTTP-FAST-c5): a hook armed and ended, and one that a Check runs,
	// allocate nothing.
	var h PollHook
	fired := 0
	hook := func() { fired++ }
	polls := testing.AllocsPerRun(100, func() {
		WhenPolled(running, &h, hook).End()
		p := WhenPolled(running, &h, hook)
		running.Check()
		running.Check()
		p.End()
	})
	if fired != 101 { // AllocsPerRun runs the function once more to warm up
		t.Fatalf("the Checked hook ran %d times in 101 runs, want once each", fired)
	}
	if polls != 0 {
		t.Fatalf("arming and ending a poll hook allocates %v times, want 0", polls)
	}
}
