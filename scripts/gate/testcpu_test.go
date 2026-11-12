package main

import (
	"maps"
	"testing"
)

// TestCPUSpansAttributeToTopLevelTests: the CPU between two -test.v lines
// goes to the top-level test running then, a subtest's to its top-level
// test, and a paused test's wait and TestMain's to nobody; the process's
// CPU counts its waited-for children (the go builds a test starts).
// Mutants: a subtest's span kept under its own name; PAUSE not ending a
// span; the children's fields left out of processCPUFrom.
func TestCPUSpansAttributeToTopLevelTests(t *testing.T) {
	spans := newCPUSpans(0)
	for _, step := range []struct {
		line string
		now  float64
	}{
		{"=== RUN   TestA", 1},
		{"=== RUN   TestA/sub", 3},
		{"    --- PASS: TestA/sub (2.00s)", 6},
		{"--- PASS: TestA (5.00s)", 7},
		{"=== RUN   TestP", 8},
		{"=== PAUSE TestP", 9},
		{"output of nobody", 10},
		{"=== RUN   TestB", 20},
		{"--- FAIL: TestB (1.00s)", 22},
		{"=== CONT  TestP", 30},
		{"--- SKIP: TestP (0.00s)", 34},
	} {
		spans.line(step.line, step.now)
	}
	want := map[string]float64{"TestA": 6, "TestP": 5, "TestB": 2}
	if got := spans.rounded(); !maps.Equal(got, want) {
		t.Errorf("spans %v; want %v", got, want)
	}
	// pid (comm with ) and spaces) state, then fields 4 to 13, then utime
	// 100, stime 50, cutime 300, cstime 50 ticks.
	stat := "42 (a) b) S 1 2 3 4 5 6 7 8 9 10 100 50 300 50 20 0 1"
	if got, err := processCPUFrom(stat); err != nil || got != 5 {
		t.Errorf("processCPUFrom = %v, %v; want 5 seconds", got, err)
	}
}
