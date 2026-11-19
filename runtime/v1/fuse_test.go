package v1

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestFuseOrRunsTheLeftSideOnADefect: FuseOr is right's value when right
// returns; when right raises a defect, it is left's (the staged form run
// again), and a defect of left is the one that leaves FuseOr: the staged
// form's, never the fused loop's (FUSION; types.md, Laws 14).
//
// Mutants: fusion-fuseor-no-recover (FuseOr returns right() bare, so the
// fused loop's defect leaves), fusion-fuseor-left-always (FuseOr runs
// left even when right returned).
func TestFuseOrRunsTheLeftSideOnADefect(t *testing.T) {
	lefts := 0
	left := func() int { lefts++; return 2 }
	if got := FuseOr(func() int { return 1 }, left); got != 1 || lefts != 0 {
		t.Errorf("FuseOr(1, left) = %d with %d runs of left; want 1 and none", got, lefts)
	}
	if got := FuseOr(func() int { panic("later stage") }, left); got != 2 || lefts != 1 {
		t.Errorf("FuseOr(defect, left) = %d with %d runs of left; want left's 2, run once", got, lefts)
	}
	defer func() {
		if r := recover(); r != "first stage" {
			t.Errorf("FuseOr(defect, defect) raised %v; want the left side's defect, \"first stage\"", r)
		}
	}()
	FuseOr(func() int { panic("later stage") }, func() int { panic("first stage") })
	t.Errorf("FuseOr(defect, defect) returned; want the left side's defect")
}

// TestFuseOrPassesACarriedExit: a panic that carries an interruption or an
// exit is no defect of the fused loop; it leaves FuseOr as it came, and left
// does not run.
//
// Mutant: fusion-fuseor-swallows-exit (FuseOr runs left on any panic, so
// an interruption inside the fused loop ends in left's value).
func TestFuseOrPassesACarriedExit(t *testing.T) {
	for _, p := range []any{rt.Interrupted, &exit{err: rt.Interrupted}} {
		lefts := 0
		func() {
			defer func() {
				if r := recover(); r != p {
					t.Errorf("FuseOr(panic %v, left) raised %v; want the panic as it came", p, r)
				}
			}()
			FuseOr(func() int { panic(p) }, func() int { lefts++; return 2 })
			t.Errorf("FuseOr(panic %v, left) returned; want the panic", p)
		}()
		if lefts != 0 {
			t.Errorf("FuseOr(panic %v, left) ran left %d times; want none", p, lefts)
		}
	}
}
