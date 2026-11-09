package check_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// preludeMessage is D-D3's message, which names the prelude declaration
// when the name alone does not say it (K4): "Task is a prelude name (the
// prelude type Task[T, E]), which cannot be redeclared".
var preludeMessage = regexp.MustCompile(`is a prelude name( \(the [^)]*\))?, which cannot be redeclared`)

// runD1Prelude is runD1, and every S108 must be D-D3's message: another
// S108 at the same line (a type-pattern or "compare with if" message) is
// the finding this round fixes.
func runD1Prelude(t *testing.T, cases []d1Case) {
	t.Helper()
	runD1(t, cases)
	for _, tc := range cases {
		_, d := check.CheckSource(strings.TrimPrefix(tc.src, "\n"))
		for _, x := range d {
			if x.Code == check.CodeType && !preludeMessage.MatchString(x.Message) {
				t.Errorf("%s: want D-D3's message, got %s", tc.name, describe(d))
			}
		}
	}
}

// Stage D1 round 2: the round 1 review's findings (stage-D1-round-1-
// findings.md), each as the program that showed it. runD1 compares the
// exact diagnostics, as CODE:line, so a cascade or a lost diagnostic fails.

// M1: a single-name let, a let or for pattern name and a parameter named
// after a universe or prelude type, a constant, a variant or an operation
// type are D-D3's S108 at the name; the binder binds with the value's type,
// so its uses add nothing.
func TestPreludeTypeAndConstantNamesAreBinders(t *testing.T) {
	names := []string{
		"bool", "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64",
		"byte", "rune", "duration", "comparable", "ordered", "number", "Map",
		"true", "false", "len",
		"Option", "Some", "None", "Console", "Scheduler", "Random", "Task",
	}
	var cases []d1Case
	for _, n := range names {
		cases = append(cases,
			d1Case{"let-" + n, fmt.Sprintf("func f() int {\n\tlet %s = 1\n\t%s + 1\n}\n", n, n), []string{"S108:2"}},
			d1Case{"let-mut-" + n, fmt.Sprintf("func f() int {\n\tlet mut %s = 1\n\t%s = 2\n\t%s\n}\n", n, n, n), []string{"S108:2"}},
			d1Case{"tuple-" + n, fmt.Sprintf("func f() int {\n\tlet (x, %s) = (1, 2)\n\tx + %s\n}\n", n, n), []string{"S108:2"}},
			d1Case{"for-" + n, fmt.Sprintf("func f(xs []int) int {\n\tlet mut s = 0\n\tfor %s in xs {\n\t\ts += %s\n\t}\n\ts\n}\n", n, n), []string{"S108:3"}},
		)
	}
	// A type of the package is a name a local may take (types.md, Scope 1;
	// L5, replacing stage C answers m3): the let binds R, which the body
	// reads; R in a type position is still the type.
	cases = append(cases,
		d1Case{"package-type-name", "type R { N int }\nfunc f(r R) int {\n\tlet R = r\n\tlet s R = R\n\ts.N\n}\n", nil},
	)
	runD1Prelude(t, cases)
}

// m1, m2: a catch rest name and a match-pattern binder named after a
// prelude function get D-D3's one S108, with no cascade.
func TestPreludeNamesInCatchAndMatchArms(t *testing.T) {
	const decls = "type E { N int }\nfunc g() int fails E { 1 }\n"
	runD1Prelude(t, []d1Case{
		{"catch-rest-len", decls + "func f() int {\n\tg() catch {\n\tcase len:\n\t\t_ = len\n\t\t0\n\t}\n}\n", []string{"S108:5"}},
		{"match-record-len", "func f(o Option[int]) int {\n\tmatch o {\n\tcase Some{Value: len}:\n\t\tlen\n\tcase None:\n\t\t0\n\t}\n}\n", []string{"S108:3"}},
		// Sleep is a prelude operation, in no scope (prelude-scope S1): the
		// arm binds it with no diagnostic.
		{"match-bare-Sleep", "func f(n int) int {\n\tmatch n {\n\tcase Sleep:\n\t\tSleep\n\t}\n}\n", nil},
		{"catch-record-len", decls + "func f() int {\n\tg() catch {\n\tcase E{N: len}:\n\t\tlen\n\t}\n}\n", []string{"S108:5"}},
	})
}

// M2: an assignment target rooted at a call's result or a literal is S108;
// one through a pointer is not.
func TestAssignmentRoots(t *testing.T) {
	const decls = "type R { N int }\nfunc xs() []int { []int{1, 2} }\nfunc r() R { R{N: 1} }\n"
	runD1(t, []d1Case{
		{"call-element", decls + "func f() {\n\txs()[0] = 5\n}\n", []string{"S108:5"}},
		{"literal-element", decls + "func f() {\n\t[]int{1, 2}[0] = 3\n}\n", []string{"S108:5"}},
		{"call-op-assign", decls + "func f() {\n\txs()[0] += 1\n}\n", []string{"S108:5"}},
		// Not addressable: typing's one diagnostic.
		{"call-field", decls + "func f() {\n\tr().N = 5\n}\n", []string{"S108:5"}},
	})
}
