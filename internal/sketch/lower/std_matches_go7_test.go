package lower

import (
	"math"
	"math/big"
	"math/rand/v2"
	"strconv"
)

// stdGo7Cases are NP4f-7's moved functions: time.Times, an Ergo body on
// int64, against an independent reference, the exact product clamped to a
// duration's range (math/big), which is Go's d * time.Duration(n) where it
// does not wrap. Edges are 0, ±1, the ends of the range, and the products
// that land on, just past and far past each end.
func stdGo7Cases(r *rand.Rand, n int) []bodyCase {
	type mul struct {
		d int64
		n int
	}
	edges := []mul{{0, 0}, {0, math.MinInt}, {5, 0}, {1, math.MaxInt}, {-1, math.MaxInt}, {-1, math.MinInt},
		{math.MinInt64, -1}, {math.MinInt64, 1}, {math.MaxInt64, -1}, {math.MaxInt64, 2}, {math.MinInt64, 2},
		{1 << 62, 2}, {1 << 62, 4}, {-(1 << 62), 2}, {1 << 31, 1 << 32}, {-(1 << 31), 1 << 32}, {3, -(1 << 62)}, {1000000007, 9223372036}}
	gen := func(r *rand.Rand) mul {
		return mul{int64(r.Uint64()) >> r.UintN(64), int(int64(r.Uint64()) >> r.UintN(64))}
	}
	lit := func(v mul) string { return strconv.FormatInt(v.d, 10) + ", " + strconv.Itoa(v.n) }
	ref := func(v mul) string {
		p := new(big.Int).Mul(big.NewInt(v.d), big.NewInt(int64(v.n)))
		switch {
		case p.Cmp(big.NewInt(math.MaxInt64)) > 0:
			return strconv.FormatInt(math.MaxInt64, 10)
		case p.Cmp(big.NewInt(math.MinInt64)) < 0:
			return strconv.FormatInt(math.MinInt64, 10)
		}
		return p.String()
	}
	return []bodyCase{
		bodyRows("time.Times", []string{"time"}, "d int64, n int", `"\(int64(time.Times(duration(d), n)))"`, edges, n, r, gen, lit, ref),
	}
}
