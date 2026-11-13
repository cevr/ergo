package main

import (
	"strings"
	"testing"
)

// With -jobs 2 two programs run at once, and the output is the
// one-at-a-time run's: each program's lines together, in the programs'
// order (gate-speed GS7). The first program's test leg waits for a signal
// that only the second program's test leg makes, so the first can pass only
// when both run at once. Mutants it catches, which no other test catches
// (scripts/examples/testdata/mutants.json): gs7-one-at-a-time, runAll
// running one program at a time (the first program's leg fails when its
// wait ends), and gs7-shared-output, programs writing straight to the run's
// output rather than to their own buffers (the second program's check line
// comes before the first program's test line).
func TestProgramsRunAtOnceAndPrintInOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	basic := map[string]map[string]string{"basic": {"expected.out": "hi\n"}}
	f.program("a-waits", "#!/bin/sh\necho hi\n# TESTAWAIT b\n", "default\n", basic, nil)
	f.program("b-signals", "#!/bin/sh\necho hi\n# TESTSIGNAL b\n", "default\n", basic, nil)
	code, out := f.run("-jobs", "2")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	lastA, firstB := -1, -1
	for i, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "a-waits") {
			lastA = i
		}
		if strings.Contains(line, "b-signals") && firstB < 0 {
			firstB = i
		}
	}
	if lastA < 0 || firstB < 0 || lastA > firstB {
		t.Fatalf("want every line of a-waits before the first of b-signals (last a-waits line %d, first b-signals line %d)\n%s", lastA, firstB, out)
	}
	if !strings.Contains(out, "examples: passed; cases: 2\n") {
		t.Fatalf("want both programs' cases counted\n%s", out)
	}
}
