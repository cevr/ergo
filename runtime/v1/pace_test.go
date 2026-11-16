package v1

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestMainPacesTheHeap checks that the program entry starts the heap
// pacing (rt.Pace, IR-G PACE1) once, before it runs main's body, and still
// returns the body's exit status. The pacer itself is runtime/rt's
// (pace_test.go); here it is replaced, so the test process keeps its own
// collector settings.
//
// Mutant: pace-not-started (runtime/rt/testdata/mutants.json).
func TestMainPacesTheHeap(t *testing.T) {
	defer func(f func()) { startPace = f }(startPace)
	started := 0
	startPace = func() { started++ }
	ran := false
	status := mainStatus(func(*rt.Task) error {
		if started != 1 {
			t.Errorf("main's body ran with the pacer started %d times, want 1", started)
		}
		ran = true
		return nil
	})
	if !ran || status != 0 || started != 1 {
		t.Fatalf("body ran %v, status %d, pacer started %d times; want true, 0, 1", ran, status, started)
	}
}
