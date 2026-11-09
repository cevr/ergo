package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// deepBound writes the reviewer's round-9 M1 shape: U[T] listing V[T] and
// V[int], a field F of type U[G[G[… int]]] with the G argument nested d
// levels and written once, and a match on n subjects x.F with one tuple
// case (V[int], …, V[int]) and case _. The
// source is O(n + d); every subject shares one instance whose bound type
// argument is d levels deep.
func deepBound(n, d int) string {
	var b strings.Builder
	b.WriteString("type G[T] {}\ntype V[T] {}\ntype U[T] = V[T] | V[int]\ntype B { F U[")
	b.WriteString(strings.Repeat("G[", d) + "int" + strings.Repeat("]", d))
	subjects, cells := make([]string, n), make([]string, n)
	for i := range n {
		subjects[i], cells[i] = "x.F", "V[int]"
	}
	b.WriteString("] }\nfunc F(x B) int {\n\t" + matchHead(subjects) + "\n\tcase " + caseRow(cells) + ":\n\t\treturn 1\n\tcase _:\n\t\treturn 0\n\t}\n}\n")
	return check.Decided(b.String())
}

// TestDeepBoundLinear: a bound type argument's identity is interned once
// per type and charged per node, and the bound identities are indexed once
// per instance, shared by every subject (round-9 M1). So n subjects of one
// instance with a d-deep bound argument take work linear in n + d, all of
// it on the switch's step counter. Round 9 made n(d+2) uncharged key
// visits here (3720, 58080 and 231360 at n=d=60, 240 and 480) while its
// counter showed 304, 1204 and 2404. Counted, not timed.
func TestDeepBoundLinear(t *testing.T) {
	var prevN, prevOps int
	for _, n := range []int{60, 240, 480} {
		src := deepBound(n, n)
		ops, d := check.VariantOps(src)
		if len(d) > 0 {
			t.Fatalf("n=d=%d: diagnostics %s", n, describe(d))
		}
		t.Logf("n=d=%d bytes=%d variant ops=%d", n, len(src), ops)
		if ops > 12*n {
			t.Errorf("n=d=%d: %d variant operations, more than 12 per subject and level", n, ops)
		}
		if prevN > 0 && ops*prevN*10 > prevOps*n*11 {
			t.Errorf("n=d %d→%d: variant operations grow from %d to %d, faster than the source", prevN, n, prevOps, ops)
		}
		prevN, prevOps = n, ops
	}
}
