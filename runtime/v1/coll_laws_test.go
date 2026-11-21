package v1

import (
	"math/rand/v2"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// TestCollLaws runs the laws of the slice and Map operations (prelude.ergo;
// the operations are the language's, check/collop.go) over their Go
// reference bodies: Get, Put, Go's delete, MapTake, Append and MapsClone,
// each on 2,000 seeded cases whose keys hit and miss, and SlicesTake and
// SlicesSlice at every index and window of 300 seeded slices. A Map the law reads again
// after an operation that would write it is passed as a copy, as the
// lowering's value rule passes it.
func TestCollLaws(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	type mcase struct {
		m    map[int]int
		k, v int
	}
	var ms []mcase
	for range 2000 {
		m := map[int]int{}
		for range r.IntN(6) {
			m[r.IntN(8)] = r.IntN(100) - 50
		}
		ms = append(ms, mcase{m, r.IntN(10), r.IntN(100) - 50})
	}
	var xss [][]int
	for range 300 {
		xs := make([]int, r.IntN(7))
		for i := range xs {
			xs[i] = r.IntN(100) - 50
		}
		xss = append(xss, xs)
	}
	clone := func(m map[int]int) map[int]int {
		c := make(map[int]int, len(m))
		for k, v := range m {
			c[k] = v
		}
		return c
	}
	del := func(m map[int]int, k int) map[int]int { delete(m, k); return m }
	type entry struct {
		v  int
		ok bool
	}
	get := func(m map[int]int, k int) entry { v, ok := Get(m, k); return entry{v, ok} }
	lawledger.Run(t, map[string]lawledger.Law{
		"prelude GetAfterPut": {Statement: `GetAfterPut(m Map[int, int], k int, v int) { match m.Put(k, v).Get(k) { case Some[int] as x: x.Value == v case None: false } }`, Check: func(t *testing.T) {
			for i, c := range ms {
				if got := get(Put(clone(c.m), c.k, c.v), c.k); got != (entry{c.v, true}) {
					t.Fatalf("case %d: Get(Put(%v, %d, %d), %d) = %v", i, c.m, c.k, c.v, c.k, got)
				}
			}
		}},
		"prelude GetAfterDelete": {Statement: `GetAfterDelete(m Map[int, int], k int) { match m.Delete(k).Get(k) { case Some[int]: false case None: true } }`, Check: func(t *testing.T) {
			for i, c := range ms {
				if got := get(del(clone(c.m), c.k), c.k); got.ok {
					t.Fatalf("case %d: Get(Delete(%v, %d), %d) = %v", i, c.m, c.k, c.k, got)
				}
			}
		}},
		"prelude TakeIsGetAndDelete": {Statement: `TakeIsGetAndDelete(m Map[int, int], k int) { let v, rest = m.Take(k) sameEntry(v, m.Get(k)) && sameEntry(rest.Get(k), None) && len(rest) == len(m.Delete(k)) }`, Check: func(t *testing.T) {
			for i, c := range ms {
				m := clone(c.m)
				v, found, rest := MapTake(m, c.k)
				if (entry{v, found}) != get(m, c.k) || get(rest, c.k).ok || len(rest) != len(del(m, c.k)) {
					t.Fatalf("case %d: MapTake(%v, %d) = %d, %v, %v", i, c.m, c.k, v, found, rest)
				}
			}
		}},
		"prelude TakeAtIsIndexAndRest": {Statement: `TakeAtIsIndexAndRest(xs []int, i int) { if i < 0 || i >= len(xs) { return true } let x, rest = xs.TakeAt(i) let mut ok = x == xs[i] && len(rest) == len(xs)-1 for k, y in rest { ok = ok && (k < i && y == xs[k] || k >= i && y == xs[k+1]) } ok }`, Check: func(t *testing.T) {
			for n, xs := range xss {
				for i := range xs {
					x, rest := SlicesTake(xs, i)
					ok := x == xs[i] && len(rest) == len(xs)-1
					for k, y := range rest {
						ok = ok && (k < i && y == xs[k] || k >= i && y == xs[k+1])
					}
					if !ok {
						t.Fatalf("case %d: SlicesTake(%v, %d) = %d, %v", n, xs, i, x, rest)
					}
				}
			}
		}},
		"prelude SliceIsWindow": {Statement: `SliceIsWindow(xs []int, i int, j int) { if i < 0 || j > len(xs) || i > j { return true } let w = xs.Slice(i, j) let mut ok = len(w) == j-i for k, y in w { ok = ok && y == xs[i+k] } ok }`, Check: func(t *testing.T) {
			for n, xs := range xss {
				for i := 0; i <= len(xs); i++ {
					for j := i; j <= len(xs); j++ {
						w := SlicesSlice(xs, i, j)
						ok := len(w) == j-i
						for k, y := range w {
							ok = ok && y == xs[i+k]
						}
						if !ok {
							t.Fatalf("case %d: SlicesSlice(%v, %d, %d) = %v", n, xs, i, j, w)
						}
					}
				}
			}
		}},
		"prelude CloneIsEqual": {Statement: `CloneIsEqual(m Map[int, int], k int) { let c = m.Clone() len(c) == len(m) && sameEntry(c.Get(k), m.Get(k)) }`, Check: func(t *testing.T) {
			for i, c := range ms {
				cl := MapsClone(c.m)
				if len(cl) != len(c.m) || get(cl, c.k) != get(c.m, c.k) {
					t.Fatalf("case %d: MapsClone(%v) = %v", i, c.m, cl)
				}
			}
		}},
		"prelude AppendIsLast": {Statement: `AppendIsLast(xs []int, x int) { let ys = xs.Append(x) len(ys) == len(xs)+1 && ys[len(xs)] == x }`, Check: func(t *testing.T) {
			for i, c := range ms {
				xs := make([]int, r.IntN(5), 8)
				ys := Append(xs, c.v)
				if len(ys) != len(xs)+1 || ys[len(xs)] != c.v {
					t.Fatalf("case %d: Append(%v, %d) = %v", i, xs, c.v, ys)
				}
			}
		}},
	})
}
