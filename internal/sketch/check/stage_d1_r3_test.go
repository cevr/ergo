package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Stage D1 round 3: the round 2 review's findings (stage-D1-round-2-
// findings.md), each as the program that showed it. runD1 compares the
// exact diagnostics, as CODE:line, so a cascade or a lost diagnostic fails.

// pinnedDecls declares a, a class G member refused for its pinned rows
// (line 3), two callees that take a callback and never run it (keep,
// store), and one whose error row is its callback's (run).
const pinnedDecls = "type Bang {}\nfunc mark() fails Bang { return Bang }\n" +
	"func a(n int) int { let m = Map[int, func(int) int]{1: a}; _ = m; try mark(); n }\n" +
	"func keep(cb func() int) int { _ = cb; 1 }\n" +
	"func store(cb func() int) { _ = cb }\n" +
	"func run[E](f func() int fails E) int fails E { try f() }\n"

// M1: creating a function literal or a trailing block runs nothing, so a
// call of the refused member in its body does not pin the row of the call
// that receives it: the absence check on that call is independent and
// stays (the six shapes of the review).
func TestCallbackBodyKeepsTheOuterDiagnostic(t *testing.T) {
	cases := []struct{ name, body, msg string }{
		{"try-literal", "func F() int { try keep(func() int { a(1) }) }\n", "try on an expression that cannot fail"},
		{"catch-literal", "func F() int { keep(func() int { a(1) }) catch { case Bang: 2 } }\n", "catch on an expression that cannot fail"},
		{"return-literal", "func F() { return store(func() int { a(1) }) }\n", "function F returns no value"},
		{"try-trailing", "func F() int { try keep() { a(1) } }\n", "try on an expression that cannot fail"},
		{"catch-trailing", "func F() int { keep() { a(1) } catch { case Bang: 2 } }\n", "catch on an expression that cannot fail"},
		{"return-trailing", "func F() { return store() { a(1) } }\n", "function F returns no value"},
		// The callback's own statements pin only the callback's row.
		{"try-statement-in-literal", "func F() int { try keep(func() int { try a(1) }) }\n", "try on an expression that cannot fail"},
	}
	var d1 []d1Case
	for _, tc := range cases {
		d1 = append(d1, d1Case{tc.name, pinnedDecls + tc.body, []string{"S108:3", "S108:7"}})
	}
	runD1(t, d1)
	for _, tc := range cases {
		_, d := check.CheckSource(pinnedDecls + tc.body)
		found := false
		for _, x := range d {
			found = found || x.Line == 7 && strings.Contains(x.Message, tc.msg)
		}
		if !found {
			t.Errorf("%s: want %q at line 7, got %s", tc.name, tc.msg, describe(d))
		}
	}
}

// M1: a call of the literal runs its body, so the literal keeps the pinned
// row's uncertainty: an immediate call, a try inside the body, a callee
// whose error row is its callback's (run), and a value branch over a call
// add no follow-on to the refused member's S108.
func TestInvokedCallbackKeepsThePinnedRow(t *testing.T) {
	runD1(t, []d1Case{
		{"immediate", pinnedDecls + "func F() int fails Bang { try (func() int { a(1) })() }\n", []string{"S108:3"}},
		{"immediate-double-paren", pinnedDecls + "func F() int fails Bang { try ((func() int { a(1) }))() }\n", []string{"S108:3"}},
		{"immediate-try-in-body", pinnedDecls + "func F() int fails Bang { try (func() int { try a(1) })() }\n", []string{"S108:3"}},
		{"immediate-catch", pinnedDecls + "func F() int { (func() int { a(1) })() catch { case Bang: 2 } }\n", []string{"S108:3"}},
		{"row-parameter-literal", pinnedDecls + "func F() int fails Bang { try run(func() int { a(1) }) }\n", []string{"S108:3"}},
		{"row-parameter-trailing", pinnedDecls + "func F() int fails Bang { try run() { a(1) } }\n", []string{"S108:3"}},
		{"row-parameter-value", pinnedDecls + "func z() int { let m = Map[int, func() int]{1: z}; _ = m; try mark(); 1 }\n" +
			"func F() int fails Bang { try run(z) }\n", []string{"S108:3", "S108:7"}},
		{"nested-under-try", pinnedDecls + "func F() int fails Bang { try keep(func() int { 1 }) + a(1) }\n", []string{"S108:3"}},
		{"value-if", pinnedDecls + "func F(c bool) int fails Bang { try if c { a(1) } else { 2 } }\n", []string{"S108:3"}},
		// Every spelling that names the member keeps the provenance: a
		// dot call and an explicit instantiation.
		{"dot-call", pinnedDecls + "type Q { N int }\nfunc h(q Q) int { let m = Map[int, func(Q) int]{1: h}; _ = m; try mark(); 1 }\n" +
			"func F(q Q) int fails Bang { try q.h() }\n", []string{"S108:3", "S108:8"}},
		{"explicit-instance", pinnedDecls + "func g[T](v T) T { let m = Map[int, func(int) int]{1: g[int]}; _ = m; try mark(); v }\n" +
			"func F() int fails Bang { try g[int](1) }\n", []string{"S108:3", "S108:7"}},
	})
}

// M2: a call of a function on a let mut local inside a frame typing skipped
// is an implicit borrow in every callee spelling typing accepts, so Q3 adds
// no "never reassigned" beside the unknown callee's S104. The controls: the
// same spellings in a frame typing checks, and an explicit &r.
func TestImplicitBorrowSpellingsInSkippedFrames(t *testing.T) {
	const decls = "type R { N int }\nfunc Bump(r *R) { r.N = 1 }\nfunc Set[T](r *R, v T) { r.N = 2 }\n" +
		"func each(cb func()) { cb() }\n"
	var cases []d1Case
	for _, call := range []string{"r.Bump()", "(r.Bump)()", "((r.Bump))()", "r.Set[int](1)", "(r.Set[int])(1)", "Bump(&r)"} {
		cases = append(cases,
			d1Case{"skipped " + call, decls + "func f() int {\n\tlet mut r = R{N: 0}\n\tunknown(func() { " + call + " })\n\tr.N\n}\n", []string{"S104:7"}},
			d1Case{"checked " + call, decls + "func f() int {\n\tlet mut r = R{N: 0}\n\teach(func() { " + call + " })\n\tr.N\n}\n", nil})
	}
	runD1(t, cases)
}

// T1: whether same-named functions are a valid group must not read which
// declaration of a duplicated type is kept (an operation type, which owns
// no functions, or a record): both orders give the duplicate's one S108,
// and every declaration order passes the order oracle. A group that is
// invalid whichever declaration is kept (both name the duplicated type, or
// another member's first parameter is not a local type) still reports the
// redeclaration. The review's FuzzCheck input is the corpus seed
// testdata/fuzz/FuzzCheck/704c117a97aa3fef.
func TestOverloadsOnADuplicatedType(t *testing.T) {
	runD1(t, []d1Case{
		{"operation-type-first", "type Y { M() int }\ntype Y { N int }\ntype E { N int }\nfunc f(y Y) {}\nfunc f(e E) {}\n", []string{"S108:2"}},
		{"record-first", "type Y { N int }\ntype Y { M() int }\ntype E { N int }\nfunc f(y Y) {}\nfunc f(e E) {}\n", []string{"S108:2"}},
		{"both-name-it", "type Y { M() int }\ntype Y { N int }\nfunc f(y Y) {}\nfunc f(y *Y) {}\n", []string{"S108:2", "S108:4"}},
		{"not-a-local-type", "type Y { N int }\ntype Y { M() int }\nfunc f(y Y) {}\nfunc f(n int) {}\n", []string{"S108:2", "S108:4"}},
	})
	for _, src := range []string{
		"type Y { M() int }\ntype Y { N int }\ntype E { N int }\nfunc f(y Y) {}\nfunc f(e E) {}\n",
		"type Y { N int }\ntype Y { M() int }\ntype E { N int }\nfunc f(y Y) {}\nfunc f(e E) {}\n",
		"type A{A()A}\ntype A{A()A}\ntype y{A()A}\ntype y{A A}\nfunc l(A y)A{A00}\ntype e{A A}\nfunc l(A e)A{A0000}",
	} {
		if p := check.OrderProblems(src, 16); len(p) > 0 {
			t.Errorf("declaration order: %v", p)
		}
	}
}
