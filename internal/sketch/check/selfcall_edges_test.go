package check_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
)

// Typed self-call edges (round 6 review M1): a call x.f(…) of a 196m
// function adds one body-graph edge, to the function typing resolves it
// to, whatever spells the receiver. An edge by name to every function
// named f merged unrelated bodies (false class G and initialization-cycle
// refusals) and made k same-named functions called m times k·m edges.

// edgeWant is one expected diagnostic: its code, a part of its message and
// its line.
type edgeWant struct {
	code, text string
	line       int
}

type edgeCase struct {
	name string
	src  string
	want []edgeWant
}

// typedEdgeCases are the receivers whose type is not written at the call:
// a call's result, a local bound to one, an element and a field. Each case
// is clean on main's method form and must stay clean, or names the one
// diagnostic it must give.
var typedEdgeCases = []edgeCase{
	// The reviewer's probe g1: m(B) does not refer to f, so f is not in a
	// group with m(A), whose _ = f is no value use inside f's group.
	{"a call's result reaches only its own function", `
type Log { Write(s string) }
type A {}
type B {}
func getB() B { return B }
func m(a A) {
	_ = f
}
func m(b B) {}
func f() {
	getB().m()
	Log.Write("x")
}
func main() {}
`, nil},
	// The reviewer's probe g3.
	{"a local bound to a call's result", `
type Log { Write(s string) }
type A {}
type B {}
func getB() B { return B }
func m(a A) {
	_ = f
}
func m(b B) {}
func f() {
	let x = getB()
	x.m()
	Log.Write("x")
}
func main() {}
`, nil},
	// The reviewer's probe g5: v reaches m(B) only, which does not read v.
	{"an initializer through a call's result", `
type A {}
type B {}
func getB() B { return B }
func m(a A) int { return v }
func m(b B) int { return 1 }
let v = getB().m()
func main() {}
`, nil},
	// The reviewer's probe g5-real: the cycle is real, and its path names
	// the function by its first parameter's type, as main named B.m.
	{"a real initialization cycle names the owner", `
type A {}
type B {}
func getB() B { return B }
func m(a A) int { return v }
func m(b B) int { return v }
let v = getB().m()
func main() {}
`, []edgeWant{{"S108", "refers to itself through B.m, v", 7}}},
	// h's call reaches knock through a call's result: knock's inferred row
	// is solved before h's, whatever the declaration order.
	{"inferred rows through a call's result before the callee", `
func H() { try h() }
func h() { try mk().knock() }
func K() fails Bang { try h() }
func mk() Door { return Door{} }
func knock(d Door) { try fail() }
func fail() fails Bang { return Bang }
type Door { open bool }
type Bang {}
func main() {}
`, []edgeWant{{"S101", "Bang", 2}}},
	{"inferred rows through a local, an element and a field", `
func H(ds []Door, w Wrap) { try h(ds, w) }
func h(ds []Door, w Wrap) {
	let d = mk()
	try d.knock()
	try ds[0].knock()
	try w.door.knock()
}
func K(ds []Door, w Wrap) fails Bang { try h(ds, w) }
func mk() Door { return Door{} }
func knock(d Door) { try fail() }
func fail() fails Bang { return Bang }
type Wrap { door Door }
type Door { open bool }
type Bang {}
func main() {}
`, []edgeWant{{"S101", "Bang", 2}}},
	// A field of function type comes first (196m rule 1): the call is no
	// edge to the function named m on another type.
	{"a field comes before a function of that name", `
type Log { Write(s string) }
type T { m func() }
type U {}
func m(u U) {
	_ = f
}
func getT() T { return T{m: func() {}} }
func f() {
	getT().m()
	Log.Write("x")
}
func main() {}
`, nil},
}

func TestSelfCallEdgesFromTyping(t *testing.T) {
	checkEdgeCases(t, typedEdgeCases)
}

// checkEdgeCases checks each source and requires exactly the expected
// diagnostics.
func checkEdgeCases(t *testing.T, cases []edgeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src)
			left := append([]check.Diagnostic(nil), diags...)
			for _, w := range tc.want {
				found := -1
				for i, d := range left {
					if d.Code == w.code && d.Line == w.line && strings.Contains(d.Message, w.text) {
						found = i
						break
					}
				}
				if found < 0 {
					t.Errorf("missing %s at line %d containing %q; got %v", w.code, w.line, w.text, diags)
					continue
				}
				left = append(left[:found], left[found+1:]...)
			}
			for _, d := range left {
				t.Errorf("unexpected diagnostic %v", d)
			}
		})
	}
}

// TestSelfCallEdgesLinear: n types each with a function m, and n callers
// that reach m through a local bound to a call's result (the reviewer's
// gen.sh). Each caller has two edges, to its maker and to its m, so the
// body graph has exactly 2n edges at n = 1000, 2000 and 4000: linear, where
// edges by name grew as n².
func TestSelfCallEdgesLinear(t *testing.T) {
	for _, n := range []int{1000, 2000, 4000} {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "type R%d {}\nfunc mk%d() R%d { return R%d }\nfunc m(_ R%d) {}\nfunc f%d() {\n\tlet x = mk%d()\n\tx.m()\n}\n", i, i, i, i, i, i, i)
		}
		b.WriteString("func main() {}\n")
		start := time.Now()
		edges, diags := check.BodyGraphEdges(b.String())
		if len(diags) != 0 || edges != 2*n {
			t.Fatalf("n = %d: %d body edges (want %d), %v", n, edges, 2*n, diags)
		}
		t.Logf("n = %d: %d edges, %s", n, edges, time.Since(start))
	}
}
