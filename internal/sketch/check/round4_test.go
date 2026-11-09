package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// The round-3 review found its classes again on wider inputs
// (round-3-findings.md). These tests cover the wider domains; the go/types
// differential test (differential_test.go) covers M1 and M4.

// ---------------------------------------------------------------- M2: case matrices as switches

// matrixSwitch writes a switch over len(sizes) subjects whose case rows are
// rows (-1 is _), with the subjects written in the order order: written
// position k holds subject order[k]. Subject j's union has sizes[j]
// variants, of the struct types V0, V1, ….
func matrixSwitch(sizes []int, rows [][]int, order []int) string {
	var b strings.Builder
	maxSize := 0
	for _, s := range sizes {
		maxSize = max(maxSize, s)
	}
	for v := range maxSize {
		fmt.Fprintf(&b, "type V%d {}\n", v)
	}
	for s := 1; s <= maxSize; s++ {
		fmt.Fprintf(&b, "type U%d = ", s)
		for v := range s {
			if v > 0 {
				b.WriteString(" | ")
			}
			fmt.Fprintf(&b, "V%d", v)
		}
		b.WriteString("\n")
	}
	b.WriteString("func F(")
	subjects := make([]string, len(order))
	for k, j := range order {
		if k > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "x%d U%d", j, sizes[j])
		subjects[k] = fmt.Sprintf("x%d", j)
	}
	b.WriteString(") int {\n\t" + matchHead(subjects) + "\n")
	for _, r := range rows {
		cells := make([]string, len(order))
		for k, j := range order {
			cells[k] = "_"
			if r[j] >= 0 {
				cells[k] = fmt.Sprintf("V%d", r[j])
			}
		}
		if len(order) == 1 {
			fmt.Fprintf(&b, "\tcase %s:\n\t\treturn 1\n", cells[0])
		} else {
			fmt.Fprintf(&b, "\tcase (%s):\n\t\treturn 1\n", strings.Join(cells, ", "))
		}
	}
	b.WriteString("\t}\n\treturn 0\n}\n")
	return check.Decided(b.String())
}

// matchHead writes the head of a match on the subjects: `match x {`, or
// `match (x, y) {` on several.
func matchHead(subjects []string) string {
	if len(subjects) == 1 {
		return "match " + subjects[0] + " {"
	}
	return "match (" + strings.Join(subjects, ", ") + ") {"
}

// caseRow writes a case's patterns: one, or a tuple of several.
func caseRow(cells []string) string {
	if len(cells) == 1 {
		return cells[0]
	}
	return "(" + strings.Join(cells, ", ") + ")"
}

// verdict classifies a switch's totality outcome.
func verdict(diags []check.Diagnostic) string {
	for _, d := range diags {
		if d.Code == "S103" {
			switch {
			case strings.Contains(d.Message, "too many cases to prove this match total"):
				return "refused"
			case strings.Contains(d.Message, "reachable"):
				return "refused-reachability"
			}
			return "not total"
		}
	}
	return "total"
}

func identityOrder(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func codes(diags []check.Diagnostic) string {
	var out []string
	for _, d := range diags {
		out = append(out, d.Code)
	}
	return strings.Join(out, " ")
}

// ---------------------------------------------------------------- M5: linear body graph

// TestBodyGraphScale: the body graph has one edge per referenced name,
// never one per same-named method, so its edge count is linear in the
// source; it is counted, not timed (round-3 M5). Initialization cycles
// through methods are still found, one per cycle, at any scale.
func TestBodyGraphScale(t *testing.T) {
	for _, n := range []int{10, 500, 2000} {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "type R%d {}\nfunc M(_ R%d) { R%d.M() }\n", i, i, i)
		}
		edges, diags := check.BodyGraphEdges(check.Decided(b.String()))
		if len(diags) != 0 || edges < 0 || edges > n {
			t.Errorf("%d self-calling methods: %d edges (want at most %d), %s", n, edges, n, describe(diags))
		}
		b.Reset()
		b.WriteString("type C { M() }\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "type R%d {}\nfunc M(_ R%d) { f%d() }\nfunc f%d() { R%d.M(); f%d() }\n", i, i, i, i, i, (i+1)%n)
		}
		edges, diags = check.BodyGraphEdges(check.Decided(b.String()))
		if len(diags) != 0 || edges < 0 || edges > 2*n {
			t.Errorf("%d methods and a ring of functions: %d edges (want at most %d), %s", n, edges, 2*n, describe(diags))
		}
		b.Reset()
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "type R%d {}\nlet v%d = func() { R%d.M() }\nfunc M(_ R%d) { _ = v%d }\n", i, i, i, i, i)
		}
		edges, diags = check.BodyGraphEdges(check.Decided(b.String()))
		cycles := 0
		for _, d := range diags {
			if d.Code == "S108" && strings.Contains(d.Message, "initialization cycle") {
				cycles++
			} else {
				t.Fatalf("%d var-method cycles: unexpected %s", n, d)
			}
		}
		if cycles != n || edges < 0 || edges > 2*n {
			t.Errorf("%d var-method cycles: %d cycles reported, %d edges (want %d and at most %d)", n, cycles, edges, n, 2*n)
		}
	}
}

// ---------------------------------------------------------------- M3: kinds from methods

// TestKindsFromMethods: a bracket parameter's kind follows its positions in
// the declaration's body and in its methods' signatures, which bind the
// receiver's parameters by position (D6; round-3 M3).
func TestKindsFromMethods(t *testing.T) {
	cases := []struct{ name, want, src string }{
		{"method-error-row", "", `type Job[E] {}
func Run[E](_ Job[E]) fails E {}`},
		{"method-capability-row", "", `type Job[R] {}
func Run[R](_ Job[R]) needs R {}`},
		{"method-renamed-receiver", "", `type Job[E] {}
func Run[F](_ Job[F]) fails F {}`},
		{"pointer-receiver", "", `type Job[E] {}
func Run[E](j *Job[E]) fails E {}`},
		{"method-and-body-agree", "", `type Job[R] { f func() needs R }
func Run[R](_ Job[R]) needs R {}`},
		{"recursive-head-through-method", "", `type Job[E] { next []Job[E] }
func Next[E](j Job[E]) Job[E] { return j }
func Run[E](_ Job[E]) fails E {}`},
		{"mutual-heads-through-method", "", `type A[E] { b []B[E] }
type B[E] { a []A[E] }
func Run[E](_ B[E]) fails E {}`},
		{"method-parameter-type", "", `type Box[T] {}
func Put[T](_ Box[T], x T) {}`},
		{"two-row-method", "", `type A[E, F] {}
func Run[E, F](_ A[E, F]) fails E | F {}`},
		{"body-type-method-row", "S108", `type Job[E] { v E }
func Run[E](_ Job[E]) fails E {}`},
		// Ergo has no error constraint (stage E1, owner answer 4).
		{"error-constraint-refused", "S104", `type Job[E error] {}
func Run[E](_ Job[E]) fails E {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src + "\n")
			if got := codes(diags); got != tc.want {
				t.Errorf("want %q, got %s", tc.want, describe(diags))
			}
		})
	}
}

// ---------------------------------------------------------------- N1: invalid union
