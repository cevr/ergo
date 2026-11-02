package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// variableUnion writes the reviewer's round-8 M3 shape: V[T], k structs
// Ai, U[T0, …, T(k-1)] listing V[T0] | … | V[T(k-1)], one subject
// U[A0, …, A(k-1)] (or U[A, …, A] with collapse, so every variant is
// V[A0]), and a match with one case V[A(k-1)] or a case per variant, and
// case _. Each variant is a variable edge of its own parameter.
func variableUnion(k int, oneCase, collapse bool) string {
	return check.Decided(variableUnionText(k, oneCase, collapse))
}

// variableUnionText is variableUnion before it is formatted, which
// collapsedCases extends.
func variableUnionText(k int, oneCase, collapse bool) string {
	var b strings.Builder
	b.WriteString("type V[T] {}\n")
	for i := range k {
		fmt.Fprintf(&b, "type A%d {}\n", i)
	}
	b.WriteString("type U[")
	for i := range k {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "T%d", i)
	}
	b.WriteString("] = ")
	for i := range k {
		if i > 0 {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "V[T%d]", i)
	}
	b.WriteString("\nfunc F(x U[")
	for i := range k {
		if i > 0 {
			b.WriteString(", ")
		}
		if collapse {
			b.WriteString("A0")
		} else {
			fmt.Fprintf(&b, "A%d", i)
		}
	}
	b.WriteString("]) int {\n\tmatch x {\n")
	switch {
	case collapse:
		b.WriteString("\tcase V[A0]:\n\t\treturn 1\n")
	case oneCase:
		fmt.Fprintf(&b, "\tcase V[A%d]:\n\t\treturn 1\n", k-1)
	default:
		for i := range k - 1 { // Leave a variant for the default arm.
			fmt.Fprintf(&b, "\tcase V[A%d]:\n\t\treturn %d\n", i, i)
		}
	}
	b.WriteString("\tcase _:\n\t\treturn 0\n\t}\n}\n")
	return b.String()
}

// signatureUnion writes the reviewer's round-8 wildcard shape: U[E]
// listing V[func(Ai) fails E], a subject U[Fault], and one case or a case
// per variant, with case _. Every variant's signature has a row that
// mentions E.
func signatureUnion(n int, oneCase bool) string {
	var b strings.Builder
	b.WriteString("type V[T] {}\n")
	for i := range n {
		fmt.Fprintf(&b, "type A%d {}\n", i)
	}
	b.WriteString("type U[E] = ")
	for i := range n {
		if i > 0 {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "V[func(A%d) fails E]", i)
	}
	b.WriteString("\ntype Fault {}\nfunc F(x U[Fault]) int {\n\tmatch x {\n")
	if oneCase {
		fmt.Fprintf(&b, "\tcase V[func(A%d) fails Fault]:\n\t\treturn 1\n", n-1)
	} else {
		for i := range n - 1 { // Leave a variant for the default arm.
			fmt.Fprintf(&b, "\tcase V[func(A%d) fails Fault]:\n\t\treturn %d\n", i, i)
		}
	}
	b.WriteString("\tcase _:\n\t\treturn 0\n\t}\n}\n")
	return check.Decided(b.String())
}

// manyParams writes the reviewer's round-8 M4 shape: Many[T0, …, T(n-1)]
// called with n integer constants. No constraint mentions a parameter.
func manyParams(n int) string {
	var b strings.Builder
	b.WriteString("func Many[")
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "T%d", i)
	}
	b.WriteString("](")
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "x%d T%d", i, i)
	}
	b.WriteString(") {}\nfunc F() {\n\tMany(")
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("1")
	}
	b.WriteString(")\n}\n")
	return check.Decided(b.String())
}

// TestIndexScaling: the reviewer's round-8 index shapes resolve their
// cases with work linear in the source (round 9 class C; D16): variable
// edges are hashed by parameter and reached through the instance's bound
// identities, and a row that mentions a parameter is a row-pattern edge
// reached through the bound row's identity. Round 8 took k(k-1)/2
// uncounted insertion comparisons for the variable union (502503 and
// 8010003 at k=1000 and 4000) and 505500 operations for 1000 signature
// cases. Counted, not timed.
func TestIndexScaling(t *testing.T) {
	type shape struct {
		name  string
		sizes []int
		src   func(n int) string
	}
	for _, s := range []shape{
		{"variable one case", []int{1000, 4000}, func(n int) string { return variableUnion(n, true, false) }},
		{"variable every case", []int{1000, 4000}, func(n int) string { return variableUnion(n, false, false) }},
		{"signature one case", []int{1000, 4000}, func(n int) string { return signatureUnion(n, true) }},
		{"signature every case", []int{1000, 4000}, func(n int) string { return signatureUnion(n, false) }},
	} {
		t.Run(s.name, func(t *testing.T) {
			var prevN, prevOps int
			for _, n := range s.sizes {
				src := s.src(n)
				ops, d := check.VariantOps(src)
				// A switch with a case per variant may reach the reachability
				// solver's own budget (S103, counted and deterministic); the
				// count here is the case resolution before it.
				for _, x := range d {
					if x.Code != check.CodeNotTotal {
						t.Fatalf("n=%d: diagnostics %s", n, describe(d))
					}
				}
				t.Logf("n=%d bytes=%d variant ops=%d diagnostics=%s", n, len(src), ops, describe(d))
				if ops > 24*n {
					t.Errorf("n=%d: %d variant operations, more than 24 per variant or case", n, ops)
				}
				if prevN > 0 && ops*prevN*10 > prevOps*n*11 {
					t.Errorf("n=%d→%d: variant operations grow from %d to %d, faster than the source", prevN, n, prevOps, ops)
				}
				prevN, prevOps = n, ops
			}
		})
	}
}

// TestIndexBudget: the safety net. Work the index cannot hash, here a
// collapsed instance U[A0, …, A0] whose k variants are all V[A0], so one
// case reaches k candidates, is charged to the switch's step counter; past
// totalityBudget the match is S103 "split it or add case _", the same
// refusal as the solvers', deterministically. Below the budget it checks
// clean.
func TestIndexBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		over bool
	}{
		{"one case, k=60", variableUnion(60, true, true), false},
		{"one case, k=240", variableUnion(240, true, true), false},
		{"k cases, k=400", collapsedCases(400), true},
		{"k cases, k=1000", collapsedCases(1000), true},
	} {
		ops, d := check.VariantOps(tc.src)
		t.Logf("%s: bytes=%d variant ops=%d diagnostics=%s", tc.name, len(tc.src), ops, describe(d))
		if !tc.over {
			if len(d) > 0 {
				t.Fatalf("%s: want clean, got %s", tc.name, describe(d))
			}
			continue
		}
		src := tc.src
		if len(d) != 1 || d[0].Code != check.CodeNotTotal || !strings.Contains(d[0].Message, "too many cases to prove this match total; split it or add case _") {
			t.Fatalf("want one S103 past the budget, got %s", describe(d))
		}
		again, d2 := check.VariantOps(src)
		if again != ops || describe(d2) != describe(d) {
			t.Fatalf("the refusal is not deterministic: %d %s, then %d %s", ops, describe(d), again, describe(d2))
		}
	}
}

// collapsedCases writes a k-variant collapsed union (all V[A0]) and a
// switch with k cases naming V[A0]: each case reaches k candidates, k × k
// steps in all.
func collapsedCases(k int) string {
	src := variableUnionText(k, true, true)
	var cases strings.Builder
	for i := range k {
		fmt.Fprintf(&cases, "\tcase V[A0]:\n\t\treturn %d\n", i)
	}
	return check.Decided(strings.Replace(src, "\tcase V[A0]:\n\t\treturn 1\n", cases.String(), 1))
}

// TestConstraintGuardLinear: the constraint guard collects the parameters
// the constraints mention once per call, then looks each constant-bound
// parameter up (round-8 M4; round 9 class C). Round 8 visited n × n
// constraints for Many[T0, …, T(n-1)] called with n constants (3600 and
// 57600 at n=60 and 240).
func TestConstraintGuardLinear(t *testing.T) {
	var prevN, prevOps int
	for _, n := range []int{60, 240} {
		ops, d := check.InferOps(manyParams(n))
		if len(d) > 0 {
			t.Fatalf("n=%d: diagnostics %s", n, describe(d))
		}
		t.Logf("n=%d bytes=%d guard ops=%d", n, len(manyParams(n)), ops)
		if ops > 3*n {
			t.Errorf("n=%d: %d guard operations, more than 3 per parameter", n, ops)
		}
		if prevN > 0 && ops*prevN*10 > prevOps*n*11 {
			t.Errorf("n=%d→%d: guard operations grow from %d to %d, faster than the source", prevN, n, prevOps, ops)
		}
		prevN, prevOps = n, ops
	}
}
