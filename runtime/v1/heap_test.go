package v1

import (
	"runtime"
	"runtime/metrics"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestHeapFloorRaisesTheGoal: a program whose environment chooses no
// collector policy gets the heap floor, and after a collection the
// collector's own goal is at least twice the floor; a program that sets
// GOGC or GOMEMLIMIT gets none (heap.go). It reads the goal from
// runtime/metrics: a count, not a time.
//
// Mutants it kills (testdata/mutants.json): gx7-main-no-floor (a
// program's main runs with Go's 4 MiB minimum), gx7-floor-dropped (the
// ballast is not kept live, so the goal falls back after a collection),
// gx7-floor-ignores-gogc and gx7-floor-ignores-memlimit (a program's own
// policy is overridden).
func TestHeapFloorRaisesTheGoal(t *testing.T) {
	env := func(k, v string) func(string) string {
		return func(name string) string {
			if name == k {
				return v
			}
			return ""
		}
	}
	saved := ballast
	defer func() { ballast = saved }()
	for _, c := range []struct{ k, v string }{{"GOGC", "100"}, {"GOGC", "off"}, {"GOMEMLIMIT", "64MiB"}} {
		ballast = nil
		if heapFloor(env(c.k, c.v)) || ballast != nil {
			t.Fatalf("%s=%s: the heap floor overrides the program's own policy", c.k, c.v)
		}
	}
	// A program's main installs it from the program's environment.
	t.Setenv("GOGC", "")
	t.Setenv("GOMEMLIMIT", "")
	ballast = nil
	if err := runMain(func(*rt.Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if ballast == nil {
		t.Fatal("no policy in the environment: main installs no heap floor")
	}
	runtime.GC()
	goal := []metrics.Sample{{Name: "/gc/heap/goal:bytes"}}
	metrics.Read(goal)
	if got := goal[0].Value.Uint64(); got < 2*heapFloorBytes {
		t.Fatalf("heap goal after a collection: %d bytes, want at least %d", got, 2*heapFloorBytes)
	}
}
