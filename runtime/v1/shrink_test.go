package v1

import (
	"flag"
	"fmt"
	"math/bits"
	"math/rand/v2"
	"testing"
)

var shrinkSeed = flag.Uint64("shrink.seed", 0, "run TestShrinkTapeKeepsTheFailure on this seed alone")

// TestShrinkTapeKeepsTheFailure is the shrinker's seeded property (L10):
// for random tapes and a random failure predicate that the tape meets,
// ShrinkTape returns a tape that still fails, no larger in shortlex
// order, within its budget of replays; and, unless the budget ran out, the
// result is a local minimum: deleting any one draw, or lowering any one
// draw by one, no longer fails. A failing seed prints the tape, the
// predicate and both results; replay with
// go test -run '^TestShrinkTapeKeepsTheFailure$' ./runtime/v1 -args -shrink.seed=N
// A single monotone magnitude predicate must reach its exact boundary in
// at most twice the draw's bit length plus pass overhead (the work guard).
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
		// A monotone predicate on one integer magnitude must reach its
		// exact boundary in logarithmic work, not a linear walk from a
		// 64-bit draw. tries counts all replays, including confirmation.
		start := 1 + r.Uint64N(^uint64(0))
		boundary := 1 + r.Uint64N(start)
		atLeast := func(c []uint64) bool { return len(c) > 0 && c[0] >= boundary }
		best, tries := ShrinkTape([]uint64{start}, atLeast, 1000)
		if len(best) != 1 || best[0] != boundary || tries > 2*bits.Len64(start)+12 {
			t.Fatalf("seed %d: magnitude >= %d from %d: best %v, tries %d; want [%d], work <= %d\nreplay: go test -run '^TestShrinkTapeKeepsTheFailure$' ./runtime/v1 -args -shrink.seed=%d", seed, boundary, start, best, tries, boundary, 2*bits.Len64(start)+12, seed)
		}
		if !p.fails(tape) {
			continue
		}
		budget := 10000
		if r.IntN(8) == 0 {
			budget = r.IntN(20)
			reached["budget"] = true
		}
		best, tries = ShrinkTape(tape, p.fails, budget)
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

// shrinkPredicate is a failure predicate over the draws and the
// tape's length (a draw past the end reads 0): "draw k is at least v", "at
// least n draws", or "the sum of the first m draws (saturating) is at least
// s", or "draw 0 >= draw 1 >= b" (lowering draw 1 unlocks draw 0, so the
// components must be revisited to a fixpoint).
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
	switch r.IntN(4) {
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
	case 3:
		b := at(tape, 1)
		if b > 0 {
			b = 1 + r.Uint64N(b)
		}
		return predicate{fmt.Sprintf("draw 0 >= draw 1 >= %d", b), func(c []uint64) bool { return at(c, 0) >= at(c, 1) && at(c, 1) >= b }}
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

// shortlexLess reports whether a comes before b: shorter, or as long and
// lexicographically smaller.
func shortlexLess(a, b []uint64) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
