package v1

import (
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
)

// moreInts is a slice of up to 8 small ints, sometimes with room after its
// elements, so a body that appends into its argument's array shows.
func moreInts(r *rand.Rand) []int {
	n := r.IntN(9)
	xs := make([]int, n, n+r.IntN(3)*4)
	for i := range xs {
		xs[i] = r.IntN(7) - 3
	}
	return xs
}

// TestStdSlicesMoreMatchGo (std-slices-maps): Clone, Concat, Repeat and
// Slice give Go's slices.Clone, slices.Concat, slices.Repeat and the full
// slice expression, and MapsClone gives Go's maps.Clone, on 10,000 seeded
// cases each; each result shares no array (or map) with its argument
// where Go's promise says so: an append to an argument with room never
// changes a Concat, a write to a Clone never reaches its argument, and an
// append to a Slice never writes past its end.
func TestStdSlicesMoreMatchGo(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for i := range 10000 {
		a, b := moreInts(r), moreInts(r)

		c := SlicesConcat(a, b)
		if want := slices.Concat(a, b); !slices.Equal(c, want) {
			t.Fatalf("case %d: Concat(%v, %v) = %v; want %v", i, a, b, c, want)
		}
		snapshot := slices.Clone(c)
		_ = append(a, 99)
		_ = append(b, 99)
		if !slices.Equal(c, snapshot) {
			t.Fatalf("case %d: an append to an argument of Concat changed it: %v, was %v", i, c, snapshot)
		}

		cl := SlicesClone(a)
		if !slices.Equal(cl, a) {
			t.Fatalf("case %d: Clone(%v) = %v", i, a, cl)
		}
		if len(cl) > 0 {
			before := a[0]
			cl[0] += 100
			if a[0] != before {
				t.Fatalf("case %d: a write to Clone reached its argument", i)
			}
		}

		n := r.IntN(5)
		if got, want := SlicesRepeat(a, n), slices.Repeat(a, n); !slices.Equal(got, want) || len(got) != len(a)*n {
			t.Fatalf("case %d: Repeat(%v, %d) = %v; want %v", i, a, n, got, want)
		}

		if len(a) > 0 {
			lo := r.IntN(len(a) + 1)
			hi := lo + r.IntN(len(a)-lo+1)
			s := SlicesSlice(a, lo, hi)
			if !slices.Equal(s, a[lo:hi]) || cap(s) != hi-lo {
				t.Fatalf("case %d: Slice(%v, %d, %d) = %v (cap %d)", i, a, lo, hi, s, cap(s))
			}
			whole := slices.Clone(a[:cap(a)])
			_ = append(s, 99)
			if !slices.Equal(a[:cap(a)], whole) {
				t.Fatalf("case %d: an append to Slice(%v, %d, %d) wrote its argument's array", i, a, lo, hi)
			}
		}

		m := map[string]int{}
		for j := range r.IntN(6) {
			m[strconv.Itoa(r.IntN(8))] = j
		}
		mc := MapsClone(m)
		if !maps.Equal(mc, maps.Clone(m)) {
			t.Fatalf("case %d: MapsClone(%v) = %v", i, m, mc)
		}
		mc["new"] = 1
		if _, ok := m["new"]; ok {
			t.Fatalf("case %d: a write to MapsClone reached its argument", i)
		}
	}
}

// BenchmarkSlicesConcat: the body of slices.Concat on two slices of 1,000
// ints, against the hand loop a program writes without it (an Append per
// element into an empty slice) and Go's one-allocation form.
func BenchmarkSlicesConcat(b *testing.B) {
	x := make([]int, 1000)
	y := make([]int, 1000)
	b.Run("Concat", func(b *testing.B) {
		for b.Loop() {
			_ = SlicesConcat(x, y)
		}
	})
	b.Run("AppendLoop", func(b *testing.B) {
		for b.Loop() {
			var out []int
			for _, v := range x {
				out = append(out, v)
			}
			for _, v := range y {
				out = append(out, v)
			}
			_ = out
		}
	})
	b.Run("GoGrowAppend", func(b *testing.B) {
		for b.Loop() {
			out := make([]int, 0, len(x)+len(y))
			out = append(out, x...)
			_ = append(out, y...)
		}
	})
}

// TestStdSlicesMoreDefects: Repeat with a negative count and Slice with a
// bound out of range panic, as Go's do: Ergo defects.
func TestStdSlicesMoreDefects(t *testing.T) {
	panics := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}
	xs := []int{1, 2, 3}
	panics("Repeat(xs, -1)", func() { SlicesRepeat(xs, -1) })
	panics("Slice(xs, 2, 1)", func() { SlicesSlice(xs, 2, 1) })
	panics("Slice(xs, 0, 4)", func() { SlicesSlice(xs, 0, 4) })
	panics("Slice(xs, -1, 1)", func() { SlicesSlice(xs, -1, 1) })
}
