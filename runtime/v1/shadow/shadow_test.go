package shadow

import (
	"math"
	"testing"
)

// TestShadowComparesByKeyEquality is Shadow's comparison (LAWFFI r2 §3.6:
// key equality, floats by their bits): a NaN equals a NaN, also inside a
// record's unexported field and a slice; -0 is not +0; a slice and a map
// compare by their elements. A difference raises a LawDefect that names
// the law, the site and both values; agreement returns the left value.
//
// Mutants: lf6c-shadow-float-by-go-equality (floats by Go's ==, so a NaN
// side is a defect), lf6c-shadow-zero-sign (floats by value, so -0 is +0).
func TestShadowComparesByKeyEquality(t *testing.T) {
	type rec struct {
		f  float64
		xs []float64
	}
	nan := math.NaN()
	holds := func(name string, f func()) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: a defect %v; want the sides equal", name, r)
			}
		}()
		f()
	}
	refutes := func(name, want string, f func()) {
		t.Helper()
		defer func() {
			d, ok := recover().(*LawDefect)
			if !ok || d.String() != want {
				t.Errorf("%s: %v; want the defect %q", name, d, want)
			}
		}()
		f()
	}
	holds("NaN", func() { Shadow(nan, nan, "p.L", "m.ergo:1:1", "F(x)") })
	holds("record", func() {
		Shadow(rec{nan, []float64{nan, 1}}, rec{nan, []float64{nan, 1}}, "p.L", "m.ergo:1:1", "F(x)")
	})
	holds("map", func() { Shadow(map[string]int{"a": 1}, map[string]int{"a": 1}, "p.L", "m.ergo:1:1", "F(x)") })
	if got := Shadow("a", "a", "p.L", "m.ergo:1:1", "F(x)"); got != "a" {
		t.Errorf("Shadow gave %q; want the left value", got)
	}
	refutes("-0", "law p.L is false at m.ergo:2:3: F(x) gave -0, and the right side gives 0", func() {
		Shadow(math.Copysign(0, -1), 0.0, "p.L", "m.ergo:2:3", "F(x)")
	})
	refutes("string", `law p.L is false at m.ergo:2:3: F(x) gave "a\x80", and the right side gives "a\ufffd"`, func() {
		Shadow("a\x80", "a\ufffd", "p.L", "m.ergo:2:3", "F(x)")
	})
	refutes("slice", "law p.L is false at m.ergo:2:3: F(x) gave [1 2], and the right side gives [1]", func() {
		Shadow([]int{1, 2}, []int{1}, "p.L", "m.ergo:2:3", "F(x)")
	})
}
