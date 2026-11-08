package v1

import (
	"cmp"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// The algebra of std slices and cmp over their Go bodies (std-align S2a;
// docs/spec/stdlib.md, Std laws). TestStdSlicesLaws checks each law that a
// Go body decides, 10,000 seeded cases per law, over []int and []string
// drawn from the whole shape of the domain: empty and one-element slices,
// many duplicates, the extremes of int, and strings with multi-byte UTF-8
// and invalid bytes. The ledger's runner of each law is the Ergo probe
// (lower TestStdSlicesLawsRun); this test is the wide model check under it.

const lawCases = 10_000

// genInts draws a slice: a third of the draws are empty or one element
// long, and the values come from a small range, so duplicates are common,
// with the extremes of int now and then.
func genInts(r *rand.Rand) []int {
	n := r.IntN(24)
	switch r.IntN(6) {
	case 0:
		n = 0
	case 1:
		n = 1
	}
	xs := make([]int, n)
	for i := range xs {
		switch r.IntN(12) {
		case 0:
			xs[i] = math.MinInt
		case 1:
			xs[i] = math.MaxInt
		case 2:
			xs[i] = int(r.Uint64())
		default:
			xs[i] = r.IntN(11) - 5
		}
	}
	return xs
}

var collectionsAlphabet = []string{"", "a", "b", "ab", "é", "日本", "\xff", "\x00", "B", "a\xffb"}

// genStrings draws a slice of strings from a small alphabet that holds
// multi-byte UTF-8 and invalid bytes.
func genStrings(r *rand.Rand) []string {
	xs := make([]string, r.IntN(12))
	for i := range xs {
		xs[i] = collectionsAlphabet[r.IntN(len(collectionsAlphabet))] + collectionsAlphabet[r.IntN(len(collectionsAlphabet))]
	}
	return xs
}

func counts[T comparable](xs []T) map[T]int {
	m := map[T]int{}
	for _, x := range xs {
		m[x]++
	}
	return m
}

func isAscending[T cmp.Ordered](xs []T) bool {
	for i := 1; i < len(xs); i++ {
		if xs[i-1] > xs[i] {
			return false
		}
	}
	return true
}

func sameCounts[T comparable](a, b []T) bool {
	ca, cb := counts(a), counts(b)
	if len(a) != len(b) || len(ca) != len(cb) {
		return false
	}
	for k, v := range ca {
		if cb[k] != v {
			return false
		}
	}
	return true
}

func TestStdSlicesLaws(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := range lawCases {
		xs := genInts(r)
		ss := genStrings(r)
		before, beforeS := slices.Clone(xs), slices.Clone(ss)
		x := r.IntN(13) - 6
		if len(xs) > 0 && r.IntN(2) == 0 {
			x = xs[r.IntN(len(xs))]
		}
		sorted, sortedS := SlicesSort(xs), SlicesSort(ss)
		// SortOrders, SortPermutes, SortIsIdempotent.
		if !isAscending(sorted) || !isAscending(sortedS) {
			t.Fatalf("case %d: SortOrders: Sort(%v) = %v; Sort(%q) = %q", i, xs, sorted, ss, sortedS)
		}
		if !sameCounts(sorted, xs) || !sameCounts(sortedS, ss) {
			t.Fatalf("case %d: SortPermutes: Sort(%v) = %v", i, xs, sorted)
		}
		if !slices.Equal(SlicesSort(sorted), sorted) || !slices.Equal(SlicesSort(sortedS), sortedS) {
			t.Fatalf("case %d: SortIsIdempotent: %v", i, sorted)
		}
		// SortLeavesItsArgument: each body that sorts or reverses returns a
		// new slice: xs is unchanged, and a write to the result does not
		// reach it.
		byDesc := SlicesSortFunc(xs, func(a, b int) int { return cmp.Compare(b, a) })
		stable := SlicesSortStableFunc(xs, func(a, b int) int { return cmp.Compare(a/4, b/4) })
		reversed := SlicesReverse(xs)
		owned := SlicesSortOwned(slices.Clone(xs))
		cloned := SlicesClone(xs)
		if !slices.Equal(cloned, xs) {
			t.Fatalf("case %d: Clone(%v) = %v", i, xs, cloned)
		}
		for _, out := range [][]int{sorted, byDesc, stable, reversed, cloned} {
			if len(out) > 0 {
				out[0]++
			}
		}
		if !slices.Equal(xs, before) || !slices.Equal(ss, beforeS) {
			t.Fatalf("case %d: SortLeavesItsArgument: %v became %v", i, before, xs)
		}
		if !slices.Equal(owned, SlicesSort(xs)) {
			t.Fatalf("case %d: SortOwned(%v) = %v, want Sort's", i, xs, owned)
		}
		// SortStableKeepsTies: the key is x % 3; of equal keys, the first
		// in xs stays first.
		type tag struct{ key, at int }
		tags := make([]tag, len(xs))
		for j, v := range xs {
			tags[j] = tag{v % 3, j}
		}
		st := SlicesSortStableFunc(tags, func(a, b tag) int { return cmp.Compare(a.key, b.key) })
		for j := 1; j < len(st); j++ {
			a, b := st[j-1], st[j]
			if !(a.key < b.key || a.key == b.key && a.at < b.at) {
				t.Fatalf("case %d: SortStableKeepsTies: %v", i, st)
			}
		}
		// ReverseTwiceIsSame.
		if !slices.Equal(SlicesReverse(SlicesReverse(xs)), xs) {
			t.Fatalf("case %d: ReverseTwiceIsSame: %v", i, xs)
		}
		// ContainsIffIndex.
		at := SlicesIndex(xs, x)
		if SlicesContains(xs, x) != (at >= 0) || at >= 0 && xs[at] != x || at >= 0 && slices.Contains(xs[:at], x) {
			t.Fatalf("case %d: ContainsIffIndex: Index(%v, %d) = %d", i, xs, x, at)
		}
		// MaxIsLastSorted (the Ergo body answers an empty slice).
		if len(xs) > 0 {
			if SlicesMax(xs) != SlicesSort(xs)[len(xs)-1] || SlicesMin(xs) != SlicesSort(xs)[0] {
				t.Fatalf("case %d: MaxIsLastSorted: %v", i, xs)
			}
		}
		// BinarySearchFindsTheFirst.
		s := SlicesSort(xs)
		got := SlicesBinarySearch(s, x)
		switch {
		case got < 0 && slices.Contains(s, x):
			t.Fatalf("case %d: BinarySearch(%v, %d) = -1, and x is there", i, s, x)
		case got >= 0 && (s[got] != x || got > 0 && s[got-1] >= x):
			t.Fatalf("case %d: BinarySearch(%v, %d) = %d, not the first x", i, s, x, got)
		}
		// Equal agrees with the element test.
		if SlicesEqual(xs, before) != true || SlicesEqual(xs, reversed) != slices.Equal(xs, reversed) {
			t.Fatalf("case %d: Equal(%v, %v)", i, xs, reversed)
		}
	}
}

// TestStdCmpMatchesGo: CmpCompare and CmpLess restate Go's cmp.Compare and
// cmp.Less, which collections.go does not import (PureImports). Every pair
// of a set of floats that holds both NaN signs, both zeros and both
// infinities, of ints at the extremes, and of strings, gives Go's answer;
// and Compare is antisymmetric and transitive over the floats, NaN
// included (the laws CompareIsAntisymmetric and CompareIsTransitive at a
// type the Ergo probe does not draw).
func TestStdCmpMatchesGo(t *testing.T) {
	floats := []float64{math.NaN(), math.Copysign(math.NaN(), -1), math.Inf(-1), -1.5, math.Copysign(0, -1), 0, 1e-300, 2.5, math.MaxFloat64, math.Inf(1)}
	for _, a := range floats {
		for _, b := range floats {
			if CmpCompare(a, b) != cmp.Compare(a, b) || CmpLess(a, b) != cmp.Less(a, b) {
				t.Errorf("Compare(%v, %v) = %d, Less %v; Go: %d, %v", a, b, CmpCompare(a, b), CmpLess(a, b), cmp.Compare(a, b), cmp.Less(a, b))
			}
			if CmpCompare(a, b) != -CmpCompare(b, a) {
				t.Errorf("Compare(%v, %v) is not antisymmetric", a, b)
			}
			for _, c := range floats {
				if CmpCompare(a, b) <= 0 && CmpCompare(b, c) <= 0 && CmpCompare(a, c) > 0 {
					t.Errorf("Compare is not transitive on %v, %v, %v", a, b, c)
				}
			}
		}
	}
	ints := []int{math.MinInt, -1, 0, 1, math.MaxInt}
	for _, a := range ints {
		for _, b := range ints {
			if CmpCompare(a, b) != cmp.Compare(a, b) || CmpLess(a, b) != cmp.Less(a, b) {
				t.Errorf("Compare(%d, %d) differs from Go's", a, b)
			}
		}
	}
	for _, a := range collectionsAlphabet {
		for _, b := range collectionsAlphabet {
			if CmpCompare(a, b) != cmp.Compare(a, b) || CmpLess(a, b) != cmp.Less(a, b) {
				t.Errorf("Compare(%q, %q) differs from Go's", a, b)
			}
		}
	}
	// A float NaN sorts first, as Go's slices.Sort puts it.
	got := SlicesSort([]float64{2, math.NaN(), -1})
	if !math.IsNaN(got[0]) || got[1] != -1 || got[2] != 2 {
		t.Errorf("Sort with a NaN = %v", got)
	}
}
