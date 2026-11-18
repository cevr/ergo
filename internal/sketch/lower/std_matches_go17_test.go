package lower

import (
	"math"
	"math/rand/v2"
	"strconv"
	"time"
)

// np4f17Pair is two instants, each Unix seconds and nanoseconds, as
// time.Unix takes them (nanoseconds outside a second included).
type np4f17Pair struct{ a, an, b, bn int64 }

// stdGo17Cases are NP4f-17's moved time Sub (its wall half; Go's
// Time.Sub of two times with no monotonic reading) against Go's own:
// time.Unix(a, an).Sub(time.Unix(b, bn)). Edges are the ends of years 1
// and 9999, the ends of int64 and the seconds where Go's internal seconds
// (Unix plus 62135596800) wrap, both saturations, equal instants, and
// nanoseconds at 0, 999999999 and outside a second; seeded pairs mix near
// and far instants. Mutants (std ledger): np4f17-sub-no-internal-offset,
// np4f17-sub-saturates-up-below, np4f17-sub-carry-short and
// np4f17-sub-addsec-wraps.
func stdGo17Cases(r *rand.Rand, n int) []bodyCase {
	const first, last = -62135596800, 253402300799
	const k = 62135596800
	secs := []int64{0, 1, -1, first, last, math.MaxInt64, math.MinInt64, math.MaxInt64 - k, math.MaxInt64 - k + 1, math.MinInt64 + 1,
		9223372036, -9223372036, 9223372037, -9223372037, 1 << 40, -(1 << 40)}
	nsecs := []int64{0, 999_999_999, 1, 500_000_000, -1, 1_000_000_000, -1_500_000_001}
	var edges []np4f17Pair
	for i, a := range secs {
		for j, b := range secs {
			edges = append(edges, np4f17Pair{a, nsecs[(i+j)%len(nsecs)], b, nsecs[(i*3+j)%len(nsecs)]})
		}
	}
	edges = append(edges, np4f17Pair{5, 999_999_999, 5, 999_999_999}, np4f17Pair{9223372036, 854_775_807, 0, 0}, np4f17Pair{9223372036, 854_775_808, 0, 0},
		np4f17Pair{0, 0, 9223372036, 854_775_808}, np4f17Pair{0, 0, 9223372036, 854_775_809}, np4f17Pair{-9223372036, -854_775_808, 0, 0})
	gen := func(r *rand.Rand) np4f17Pair {
		a := first + r.Int64N(last-first+1)
		switch r.IntN(3) {
		case 0:
			return np4f17Pair{a, r.Int64N(1_000_000_000), first + r.Int64N(last-first+1), r.Int64N(1_000_000_000)}
		case 1:
			return np4f17Pair{a, r.Int64N(1_000_000_000), a + r.Int64N(20_000_000_000) - 10_000_000_000, r.Int64N(1_000_000_000)}
		}
		return np4f17Pair{int64(r.Uint64()), r.Int64N(3_000_000_000) - 1_000_000_000, int64(r.Uint64()), r.Int64N(1_000_000_000)}
	}
	i := strconv.FormatInt
	lit := func(p np4f17Pair) string {
		return "int64(" + i(p.a, 10) + "), int64(" + i(p.an, 10) + "), int64(" + i(p.b, 10) + "), int64(" + i(p.bn, 10) + ")"
	}
	ref := func(p np4f17Pair) string {
		return i(int64(time.Unix(p.a, p.an).Sub(time.Unix(p.b, p.bn))), 10)
	}
	c := bodyRows("time.Sub", []string{"time"}, "a int64, an int64, b int64, bn int64", `"\(int64(time.Unix(a, an).Sub(time.Unix(b, bn))))"`,
		edges, n, r, gen, lit, ref)
	return []bodyCase{c}
}
