package stdlib_test

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestAllocsLaws runs the law of testing.Allocs (MX-P3 F6) against the Go
// body AllocCounter.PerRun lowers to, v1.AllocsPerRun, with a callback of
// the shape a lowered func() {} takes: a call that does nothing counts 0.
// It is the ledger's runner for the law. A runs that starts lazily (it
// allocates on its first call only) counts 0 too, since the warm-up call
// pays it (mutant f6-allocs-no-warmup), and a call that allocates one object
// counts 1 per run, not the total (f6-allocs-total-not-mean).
func TestAllocsLaws(t *testing.T) {
	task := rt.Root("allocs")
	laws := map[string]lawledger.Law{
		"testing Allocs.EmptyCallIsZero": {Statement: `EmptyCallIsZero() { PerRun(10, func() {}) == 0 }`, Check: func(t *testing.T) {
			if n := v1.AllocsPerRun(task, 10, func(*rt.Task) {}); n != 0 {
				t.Errorf("EmptyCallIsZero: PerRun(10, func() {}) is %d", n)
			}
		}},
	}
	lawledger.Run(t, laws)
	var lazy []int
	if n := v1.AllocsPerRun(task, 1, func(*rt.Task) {
		if lazy == nil {
			lazy = make([]int, 64)
		}
	}); n != 0 {
		t.Errorf("a call that allocates on its first run only counts %d after the warm-up, want 0", n)
	}
	var keep []*[64]int
	if n := v1.AllocsPerRun(task, 10, func(*rt.Task) { keep = append(keep[:0], new([64]int)) }); n != 1 {
		t.Errorf("a call that allocates one object counts %d per run, want 1", n)
	}
}
