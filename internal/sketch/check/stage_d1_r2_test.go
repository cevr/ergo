package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// runD1Prelude is runD1, and every S108 must be D-D3's message: another
// S108 at the same line (a type-pattern or "compare with if" message) is
// the finding this round fixes.
func runD1Prelude(t *testing.T, cases []d1Case) {
	t.Helper()
	runD1(t, cases)
	for _, tc := range cases {
		_, d := check.CheckSource(strings.TrimPrefix(tc.src, "\n"))
		for _, x := range d {
			if x.Code == check.CodeType && !strings.Contains(x.Message, "is a prelude name, which cannot be redeclared") {
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

// m3: universe names outside D-D3's list are not prelude names: `any` and
// `error` are stage E's, `uintptr` and the complex types are not v1 types.
func TestUniverseNamesOutsideTheDecidedList(t *testing.T) {
	runD1(t, []d1Case{
		{"any", "func f(any int) int { any }\n", nil},
		{"error", "func f(error int) int { error }\n", nil},
		{"uintptr", "func f(uintptr int) int { uintptr }\n", nil},
		{"complex64", "func f(complex64 int) int { complex64 }\n", nil},
		{"complex128", "func f(complex128 int) int { complex128 }\n", nil},
	})
}

// m4: a binder of an invalid value still meets D-D2, D-D3 and the
// duplicate check.
func TestInvalidValueBindersKeepTheBinderRules(t *testing.T) {
	runD1(t, []d1Case{
		{"pattern-over-parameter", "func f(a int) int {\n\tlet (a, b) = unknownFn()\n\tb\n}\n", []string{"S108:2", "S104:2"}},
		{"name-over-parameter", "func f(a int) int {\n\tlet a = unknownFn()\n\t1\n}\n", []string{"S108:2", "S104:2"}},
		{"prelude-name", "func f() int {\n\tlet (len, b) = unknownFn()\n\tb\n}\n", []string{"S108:2", "S104:2"}},
		{"duplicate", "func f() int {\n\tlet (b, b) = unknownFn()\n\t1\n}\n", []string{"S108:2", "S104:2"}},
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

// M3: a let mut whose reassignment typing skipped (a frame of an invalid
// callee or an orphan index, a target that fails typing) is not reported
// "never reassigned" beside the real error.
func TestNeverReassignedBesideAnError(t *testing.T) {
	runD1(t, []d1Case{
		{"literal-of-unknown-callee", "func f(xs []int) int {\n\tlet mut n = 0\n\teachOf(xs, func(x int) { n += x })\n\tn\n}\n", []string{"S104:3"}},
		{"trailing-block-of-unknown-callee", "func f(xs []int) int {\n\tlet mut n = 0\n\teachOf(xs) {\n\t\tn += 1\n\t}\n\tn\n}\n", []string{"S104:3"}},
		{"literal-in-unindexable-index", "func f() int {\n\tlet mut n = 0\n\tlet r = 0[func() int {\n\t\tn = 2\n\t\tn\n\t}]\n\tn\n}\n", []string{"S108:3"}},
		{"literal-in-invalid-index", "func f() int {\n\tlet mut n = 0\n\tlet r = zz[func() int {\n\t\tn = 2\n\t\tn\n\t}]\n\tn\n}\n", []string{"S104:3"}},
		{"literal-in-unknown-literal", "func f() int {\n\tlet mut n = 0\n\tlet r = Unknown{K: func() { n = 2 }}\n\tn\n}\n", []string{"S104:3"}},
		{"borrow-in-skipped-frame", "type R { N int }\nfunc bump(r *R) {}\nfunc f() int {\n\tlet mut r = R{N: 1}\n\teachOf() { bump(&r) }\n\tr.N\n}\n", []string{"S104:5"}},
		{"misspelled-field", "type R { N int }\nfunc f() int {\n\tlet mut c = R{N: 1}\n\tc.Count = 2\n\tc.N\n}\n", []string{"S104:4"}},
		{"bad-index-type", "func f() int {\n\tlet mut xs = []int{1}\n\txs[\"a\"] = 2\n\txs[0]\n}\n", []string{"S108:3"}},
	})
}

// M4: the locals of a package-level let's function literal follow the
// function rules.
func TestPackageLetLiteralLocals(t *testing.T) {
	runD1(t, []d1Case{
		{"never-reassigned", "let g = func() int {\n\tlet mut k = 1\n\tk\n}\n", []string{"S110:2"}},
		{"unused", "let g = func() {\n\tlet z = 1\n}\n", []string{"S110:2"}},
		{"reassigned", "let g = func() int {\n\tlet mut k = 1\n\tk += 1\n\tk\n}\n", nil},
	})
}

// M5: a call, from a later component, of a member class G refuses keeps
// the member's written signature, so an arity or argument mistake is still
// reported (p13); only the row follow-ons are dropped
// (classf/go-statement.ergo, mapkey-*.ergo).
func TestRefusedMemberKeepsItsSignature(t *testing.T) {
	runD1(t, []d1Case{
		{"p13", "type Bang {}\nfunc mark() fails Bang { return Bang }\n" +
			"func a(n int) int { let m = Map[int, func(int) int]{1: a}; _ = m; try mark(); n }\n" +
			"func F() fails Bang {\n\tlet s string = try a(1, 2)\n\tlet t = a(\"x\")\n\t_ = s\n\t_ = t\n}\n" +
			"func G() fails Bang {\n\tlet u = try a(1) + undefinedName\n\t_ = u\n}\n",
			[]string{"S108:3", "S108:5", "S108:6", "S104:11"}},
		// The follow-ons of the pinned row stay dropped: a try that cannot
		// fail, a catch that cannot fail, a return of no value.
		{"follow-ons", "type Bang {}\nfunc mark() fails Bang { return Bang }\n" +
			"func a() { let m = Map[int, func()]{1: a}; _ = m; try mark() }\n" +
			"func F() fails Bang { try a() }\nfunc G() { a() catch { case Bang: } }\nfunc H() fails Bang { return a() }\n",
			[]string{"S108:3"}},
	})
}
