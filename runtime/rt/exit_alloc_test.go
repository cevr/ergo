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
// gx5-live-timer-fresh (a new host timer for each live sleep).
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
}
