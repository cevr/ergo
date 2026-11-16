package eval

import (
	"runtime/debug"
	"testing"
)

// TestRunRestoresGCPercent: an evaluation raises the collector's target
// only while it runs (gc.go); the process's own target is back once Run
// returns. Mutant: oracle-o32-gc-not-restored.
func TestRunRestoresGCPercent(t *testing.T) {
	pkg := checked(t, `test T {
	assert 1 + 1 == 2
}
`)
	prev := debug.SetGCPercent(77)
	defer debug.SetGCPercent(prev)
	if _, err := Run(pkg, DefaultBudget()); err != nil {
		t.Fatal(err)
	}
	if got := debug.SetGCPercent(77); got != 77 {
		t.Fatalf("the GC percent after Run is %d, want the 77 set before it", got)
	}
}
