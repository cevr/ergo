package shadow

import (
	"math"
	"os"
	"slices"
	"strings"
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
	holds("NaN", func() { Shadow(nan, nan, "p.L", "m.ergo:1:1", "F(x)", "x", true, []any{1.0}) })
	holds("record", func() {
		Shadow(rec{nan, []float64{nan, 1}}, rec{nan, []float64{nan, 1}}, "p.L", "m.ergo:1:1", "F(x)", "x", true, []any{1.0})
	})
	holds("map", func() {
		Shadow(map[string]int{"a": 1}, map[string]int{"a": 1}, "p.L", "m.ergo:1:1", "F(x)", "x", true, []any{1.0})
	})
	if got := Shadow("a", "a", "p.L", "m.ergo:1:1", "F(x)", "x", true, []any{1.0}); got != "a" {
		t.Errorf("Shadow gave %q; want the left value", got)
	}
	var lines strings.Builder
	stderr = &lines
	defer func() { stderr = os.Stderr }()
	refutes("-0", "law p.L is false at m.ergo:2:3: F(x) gave -0, and the right side gives 0, for x = -0", func() {
		Shadow(math.Copysign(0, -1), 0.0, "p.L", "m.ergo:2:3", "F(x)", "x", true, []any{math.Copysign(0, -1)})
	})
	refutes("string", `law p.L is false at m.ergo:2:3: F(x) gave "a\x80", and the right side gives "a\ufffd", for s = "a\x80", n = 2`, func() {
		Shadow("a\x80", "a\ufffd", "p.L", "m.ergo:2:3", "F(x)", "s,n", true, []any{"a\x80", int64(2)})
	})
	refutes("slice", "law p.L is false at m.ergo:2:3: F(x) gave [1 2], and the right side gives [1], for xs = [1 2]", func() {
		Shadow([]int{1, 2}, []int{1}, "p.L", "m.ergo:2:3", "F(x)", "xs", true, []any{[]int64{1, 2}})
	})
	refutes("listable", "law p.L is false at m.ergo:2:3: F(x) gave 1, and the right side gives 2, for b = true", func() {
		Shadow(1, 2, "p.L", "m.ergo:2:3", "F(x)", "b", false, []any{true})
	})
	// Each refutation's line, the case in ergo.cases's text; a listable
	// parameter (enc false) gives none.
	want := "ergo-shadow: law p.L refuted (-0x0p+00)\n" +
		"ergo-shadow: law p.L refuted (\"a\\x80\", 2)\n" +
		"ergo-shadow: law p.L refuted ([1, 2])\n" +
		"ergo-shadow: law p.L refuted\n"
	if lines.String() != want {
		t.Errorf("refutation lines:\n%s\nwant:\n%s", lines.String(), want)
	}
}

// TestCanaryChecksEveryNthCall is the canary (LAWFFI r2 §3.7): Tick is
// true on each N-th call at a site, counted per site; Canary raises a
// defect that names the law and the site and no value, when key equality
// tells the sides apart, and nothing when it holds (a NaN equal to a NaN).
//
// Mutants: lf6d-tick-shared (every site counts on one counter),
// lf6d-canary-names-value (the defect prints a value).
func TestCanaryChecksEveryNthCall(t *testing.T) {
	var a, b []int
	for i := 1; i <= 7; i++ {
		if Tick(7001, 3) {
			a = append(a, i)
		}
		if i%2 == 0 && Tick(7002, 3) {
			b = append(b, i)
		}
	}
	if !slices.Equal(a, []int{3, 6}) || !slices.Equal(b, []int{6}) {
		t.Errorf("ticks at %v and %v; want [3 6] at one site and [6] at the other (its third call)", a, b)
	}
	Canary(math.NaN(), math.NaN(), "p.L", "m.ergo:1:1")
	func() {
		defer func() {
			if d, ok := recover().(*CanaryDefect); !ok || d.At != "m.ergo:3:4" {
				t.Errorf("CanaryFail: %v; want the defect at m.ergo:3:4", d)
			}
		}()
		CanaryFail("p.L", "m.ergo:3:4")
	}()
	defer func() {
		d, ok := recover().(*CanaryDefect)
		want := "law p.L is false at m.ergo:4:5: the canary evaluated both sides of the rewrite, and key equality tells them apart"
		if !ok || d.String() != want {
			t.Errorf("%v; want the defect %q", d, want)
		}
	}()
	Canary("secret", "other", "p.L", "m.ergo:4:5")
}
