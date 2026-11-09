package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// sharedTuple writes the reviewer's round-7 TestR7ReviewGenericTuple
// shape: V[T, Q], n records Ai, a union U[T] of V[T, A0] … V[T, A(n-1)],
// n subjects xi of type U[Ai], and one tuple case (V[A0, A(n-1)], …,
// V[A(n-1), A(n-1)]) with case _. Every variant
// of every subject shares the origin V. With noSwitch, the function only
// returns (the control).
func sharedTuple(n int, noSwitch bool) string {
	var b strings.Builder
	b.WriteString("type V[T, Q] {}\n")
	for i := range n {
		fmt.Fprintf(&b, "type A%d {}\n", i)
	}
	b.WriteString("type U[T] = ")
	for i := range n {
		if i > 0 {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "V[T, A%d]", i)
	}
	b.WriteString("\nfunc F(")
	subjects := make([]string, n)
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "x%d U[A%d]", i, i)
		subjects[i] = fmt.Sprintf("x%d", i)
	}
	b.WriteString(") int {\n")
	if !noSwitch {
		b.WriteString("\t" + matchHead(subjects) + "\n\tcase ")
		if n > 1 {
			b.WriteString("(")
		}
		for i := range n {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "V[A%d, A%d]", i, n-1)
		}
		if n > 1 {
			b.WriteString(")")
		}
		b.WriteString(":\n\t\treturn 1\n\tcase _:\n\t\treturn 0\n\t}\n")
	}
	b.WriteString("\treturn 0\n}\n")
	return check.Decided(b.String())
}

// sharedCases writes one subject whose n variants share one origin, and a
// match with a case for each variant and case _. The variants are
// V[Ai] of a plain union (the reviewer's TestR7ReviewSharedOrigin), V[B,
// Ai] of an instance U[B] of a generic union, or V[func() Ai], whose type
// arguments are signatures.
func sharedCases(n int, shape string, noSwitch bool) string {
	var b strings.Builder
	variant := func(i int) string { return fmt.Sprintf("V[A%d]", i) }
	param := "U"
	switch shape {
	case "plain":
		b.WriteString("type V[T] {}\n")
	case "generic":
		b.WriteString("type V[T, Q] {}\ntype B {}\n")
		variant = func(i int) string { return fmt.Sprintf("V[T, A%d]", i) }
		param = "U[B]"
	case "signature":
		b.WriteString("type V[T] {}\n")
		variant = func(i int) string { return fmt.Sprintf("V[func() A%d]", i) }
	}
	for i := range n {
		fmt.Fprintf(&b, "type A%d {}\n", i)
	}
	if shape == "generic" {
		b.WriteString("type U[T] = ")
	} else {
		b.WriteString("type U = ")
	}
	for i := range n {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(variant(i))
	}
	fmt.Fprintf(&b, "\nfunc F(x %s) int {\n", param)
	if !noSwitch {
		b.WriteString("\tmatch x {\n")
		for i := range n - 1 { // Leave a variant for the default arm.
			c := variant(i)
			if shape == "generic" {
				c = fmt.Sprintf("V[B, A%d]", i)
			}
			fmt.Fprintf(&b, "\tcase %s:\n\t\treturn %d\n", c, i)
		}
		b.WriteString("\tcase _:\n\t\treturn -1\n\t}\n")
	}
	b.WriteString("\treturn 0\n}\n")
	return check.Decided(b.String())
}

// variantWork checks src and returns its variant operations and generic
// instances.
func variantWork(t *testing.T, src string) (int, int) {
	t.Helper()
	ops, d := check.VariantOps(src)
	for _, x := range d {
		if x.Code != check.CodeNotTotal {
			t.Fatalf("diagnostics %s", describe(d))
		}
	}
	inst, _ := check.InstancesMade(src)
	return ops, inst
}

// TestSharedOriginLinear: a case resolves its variant by instance
// identity, so the variant operations and the generic instances a switch
// adds grow linearly with the source, for variants that share one origin
// across subjects, cases and type arguments (round-7 M4; D16 as amended
// in round 8: nothing before the solvers' counters is superlinear). Round
// 7 took n × k operations and instances here (3720 and 58080 at n=k=60 and
// 240 for the tuple; 502500 and 8010000 operations for the plain union at
// n=1000 and 4000). Counted, not timed: each count is at most a constant
// times the source size, and the switch's instances at most a constant per
// subject or case over the no-switch control's.
func TestSharedOriginLinear(t *testing.T) {
	type shape struct {
		name  string
		sizes []int
		src   func(n int, noSwitch bool) string
	}
	for _, s := range []shape{
		{"tuple", []int{60, 240}, sharedTuple},
		{"plain", []int{1000, 4000}, func(n int, ns bool) string { return sharedCases(n, "plain", ns) }},
		{"generic", []int{1000, 4000}, func(n int, ns bool) string { return sharedCases(n, "generic", ns) }},
		{"signature", []int{1000, 4000}, func(n int, ns bool) string { return sharedCases(n, "signature", ns) }},
	} {
		t.Run(s.name, func(t *testing.T) {
			var prevN, prevOps int
			for _, n := range s.sizes {
				src := s.src(n, false)
				ops, inst := variantWork(t, src)
				_, base := variantWork(t, s.src(n, true))
				t.Logf("n=%d bytes=%d variant ops=%d instances=%d control instances=%d", n, len(src), ops, inst, base)
				if ops > 16*n {
					t.Errorf("n=%d: %d variant operations, more than 16 per subject or case: superlinear", n, ops)
				}
				if inst-base > 3*n {
					t.Errorf("n=%d: the switch adds %d instances, more than 3 per subject or case: superlinear", n, inst-base)
				}
				// Four times the size, at most about four times the work.
				if prevN > 0 && ops*prevN*10 > prevOps*n*11 {
					t.Errorf("n=%d→%d: variant operations grow from %d to %d, faster than the source", prevN, n, prevOps, ops)
				}
				prevN, prevOps = n, ops
			}
		})
	}
}
