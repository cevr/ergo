package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestOrPatternMessages pins the text and line of the diagnostics of
// nested alternatives (FK7a; syntax/orpattern.go), which the property
// TestNestedAlternativesMatchTheirExpansion compares by code and line only.
// Mutants (syntax ledger): a top-level | reads as an expression
// (orp-top-bar-text); | is admitted in a let pattern (orp-let-admitted);
// a case past 64 patterns is admitted (orp-cap-missing); an alternative
// covered by its sibling is said to be covered by an earlier case
// (orp-covered-sibling-text).
func TestOrPatternMessages(t *testing.T) {
	const decls = "type A {}\ntype B {}\ntype U = A | B\n"
	cases := []struct {
		name, src string
		line      int
		code, has string
	}{
		{"a top-level |", decls + "func f(u U) int {\n\tmatch u {\n\tcase A | B:\n\t\t1\n\t}\n}\n", 6, "S001",
			"the alternatives of a case are separated by commas: case A, B:"},
		{"| in a let", "func f() int {\n\tlet (a | b, c) = (1, 2)\n\ta + c\n}\n", 2, "S001",
			"alternatives with | stand only in a case's patterns"},
		{"past 64 patterns", "func f(x (int, int, int, int)) int {\n\tmatch x {\n\tcase (1 | 2 | 3, 1 | 2 | 3, 1 | 2 | 3, 1 | 2 | 3):\n\t\t1\n\tcase _:\n\t\t2\n\t}\n}\n", 3, "S001",
			"this case expands to 81 patterns, more than 64: split it into several cases"},
		{"a sibling covers an alternative", decls + "func f(u U) int {\n\tmatch (u, 1) {\n\tcase (A | A, _):\n\t\t1\n\tcase _:\n\t\t2\n\t}\n}\n", 6, "S108",
			"an alternative of `(A | A, _)` is already covered by an earlier alternative of it"},
	}
	for _, c := range cases {
		_, diags := check.CheckSource(c.src + "func main() {}\n")
		ok := false
		for _, d := range diags {
			if d.Code == c.code && d.Line == c.line && strings.Contains(d.Message, c.has) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s: want %s at line %d with %q, got %s\n%s", c.name, c.code, c.line, c.has, describe(diags), c.src)
		}
	}
}
