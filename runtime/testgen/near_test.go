package testgen

import (
	"flag"
	"math"
	"strconv"
	"strings"
	"testing"
)

var nearSeed = flag.Uint64("near.seed", 0, "replay TestNearVariantsReachTies at this seed alone")

// TestNearVariantsReachTies is the search's property (runtime.md, Tests 6;
// CONSTRAINTS C5): for generated equivalences with a known distinct equal
// value one near-variant step away, Search finds a distinct admitted
// value; for the identity it finds none, which a law run reports as a
// skip. Each equivalence is a reference model written in the test. A
// failing seed prints its value and the replay flag.
func TestNearVariantsReachTies(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	if *nearSeed != 0 {
		seeds = []uint64{*nearSeed}
	}
	for _, seed := range seeds {
		r := New(seed, "near")
		replay := " (replay: go test ./runtime/testgen -run TestNearVariantsReachTies -near.seed " + strconv.FormatUint(seed, 10) + ")"
		for i := 0; i < 50; i++ {
			r.Start(i)
			// Strings equal up to ASCII letter case: a string with a
			// letter has a distinct equal variant (bit 5 of the letter).
			s := "q" + r.Str()
			fold := func(x string) bool { return strings.EqualFold(x, s) }
			if v, ok, d := Search(r, s, true, func() string { return r.Str() }, fold); !ok || !d || v == s {
				t.Fatalf("seed %d: no distinct case-folded variant of %q (got %q, admitted %v, distinct %v)%s", seed, s, v, ok, d, replay)
			}
			// Integers equal modulo 2: flipping bit 1 keeps the parity.
			n := r.I64()
			if v, ok, d := Search(r, n, true, func() int64 { return r.I64() }, func(x int64) bool { return (x-n)%2 == 0 }); !ok || !d || v == n {
				t.Fatalf("seed %d: no distinct variant of %d of its parity (got %d)%s", seed, n, v, replay)
			}
			// The identity: a fresh draw may equal n, but no variant does,
			// so the value is n itself, not distinct.
			if v, ok, d := Search(r, n, true, func() int64 { return n + 1 }, func(x int64) bool { return x == n }); !ok || d || v != n {
				t.Fatalf("seed %d: the identity found %d distinct from %d%s", seed, v, n, replay)
			}
		}
		// == on floats: 0.0 and -0.0 are equal and not identical.
		v, ok, d := Search(r, 0.0, true, func() float64 { return 1 }, func(x float64) bool { return x == 0 })
		if !ok || !d || !math.Signbit(v) {
			t.Fatalf("seed %d: no -0.0 for 0.0 (got %v)%s", seed, v, replay)
		}
	}
}
