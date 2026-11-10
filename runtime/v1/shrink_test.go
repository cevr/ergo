package v1

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"testing"
)

var shrinkSeed = flag.Uint64("shrink.seed", 0, "run TestShrinkTapeKeepsTheFailure on this seed alone")

// TestShrinkTapeKeepsTheFailure is the shrinker's seeded property (L10):
// for random tapes and a random failure predicate that the tape meets,
// ShrinkTape returns a tape that still fails, no larger in shortlex
// order, within its budget of replays; and for a predicate monotone in
// each draw (every predicate here is), unless the budget ran out, the
// result is a local minimum: deleting any one draw, or lowering any one
// draw by one, no longer fails. A failing seed prints the tape, the
// predicate and both results; replay with
// go test -run '^TestShrinkTapeKeepsTheFailure$' ./runtime/v1 -args -shrink.seed=N
func TestShrinkTapeKeepsTheFailure(t *testing.T) {
	first, last := uint64(1), uint64(300)
	if *shrinkSeed != 0 {
		first, last = *shrinkSeed, *shrinkSeed
	}
	reached := map[string]bool{}
	for seed := first; seed <= last; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x5eed))
		tape := make([]uint64, r.IntN(24))
		for i := range tape {
			switch r.IntN(3) {
			case 0:
				tape[i] = r.Uint64()
			case 1:
				tape[i] = uint64(r.IntN(100))
			}
		}
		p := shrinkPredicate(r, tape)
		if !p.fails(tape) {
			continue
		}
		budget := 10000
		if r.IntN(8) == 0 {
			budget = r.IntN(20)
			reached["budget"] = true
		}
		best, tries := ShrinkTape(tape, p.fails, budget)
		fail := func(why string) {
			t.Fatalf("seed %d: %s\npredicate %s\ntape %v\nbest %v (tries %d of %d)\nreplay: go test -run '^TestShrinkTapeKeepsTheFailure$' ./runtime/v1 -args -shrink.seed=%d", seed, why, p.text, tape, best, tries, budget, seed)
		}
		if !p.fails(best) {
			fail("the shrunk tape does not fail")
		}
		if shortlexLess(tape, best) {
			fail("the shrunk tape is larger")
		}
		if tries > budget {
			fail("more replays than the budget")
		}
		if shortlexLess(best, tape) {
			reached["shrunk"] = true
		}
		if tries >= budget {
			continue
		}
		reached["minimal"] = true
		for i := range best {
			c := append(append([]uint64(nil), best[:i]...), best[i+1:]...)
			if p.fails(c) {
				fail(fmt.Sprintf("deleting draw %d still fails", i))
			}
			if best[i] > 0 {
				c := append([]uint64(nil), best...)
				c[i]--
				if p.fails(c) {
					fail(fmt.Sprintf("lowering draw %d still fails", i))
				}
			}
		}
	}
	if *shrinkSeed == 0 {
		for _, k := range []string{"budget", "shrunk", "minimal"} {
			if !reached[k] {
				t.Errorf("no seed reached %q", k)
			}
		}
	}
}

// shrinkPredicate is a failure predicate monotone in each draw and in the
// tape's length (a draw past the end reads 0): "draw k is at least v", "at
// least n draws", or "the sum of the first m draws (saturating) is at least
// s", chosen so the tape meets it.
type predicate struct {
	text  string
	fails func([]uint64) bool
}

func shrinkPredicate(r *rand.Rand, tape []uint64) predicate {
	at := func(c []uint64, k int) uint64 {
		if k < len(c) {
			return c[k]
		}
		return 0
	}
	switch r.IntN(3) {
	case 0:
		k := r.IntN(len(tape) + 1)
		v := at(tape, k)
		if v > 0 {
			v = 1 + r.Uint64N(v)
		}
		return predicate{fmt.Sprintf("draw %d >= %d", k, v), func(c []uint64) bool { return at(c, k) >= v }}
	case 1:
		n := r.IntN(len(tape) + 1)
		return predicate{fmt.Sprintf("len >= %d", n), func(c []uint64) bool { return len(c) >= n }}
	}
	m := r.IntN(len(tape) + 1)
	sum := func(c []uint64) uint64 {
		s := uint64(0)
		for k := 0; k < m; k++ {
			if s += at(c, k); s < at(c, k) {
				return ^uint64(0)
			}
		}
		return s
	}
	s := sum(tape)
	if s > 0 {
		s = 1 + r.Uint64N(s)
	}
	return predicate{fmt.Sprintf("sum of the first %d >= %d", m, s), func(c []uint64) bool { return sum(c) >= s }}
}
