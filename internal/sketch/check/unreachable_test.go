package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// K5 (owner q29, 2026-11-08; synthesis M22, repro G-ex-a-3): the prelude
// function Unreachable(reason) has the bottom type. It checks in every
// position that expects a value, joins any type as the last statement of a
// branch, and raises a defect, so it adds nothing to a row.

// unreachablePositions are sources that use Unreachable in one position
// each; every one checks clean.
var unreachablePositions = []struct{ name, src string }{
	{"statement", "func f(n int) int {\n\tif n < 0 {\n\t\tUnreachable(\"negative\")\n\t}\n\tn\n}\n"},
	{"statement-before-more", "func f(n int) int {\n\tUnreachable(\"never\")\n\tn\n}\n"},
	{"typed-let", "func f() int {\n\tlet x int = Unreachable(\"let\")\n\tx\n}\n"},
	{"let-of-a-value-if", "func f(c bool) int {\n\tlet x = if c { 1 } else { Unreachable(\"else\") }\n\tx\n}\n"},
	{"let-of-a-value-if-first", "func f(c bool) string {\n\tlet x = if c { Unreachable(\"then\") } else { \"b\" }\n\tx\n}\n"},
	{"match-arm", "func f(o Option[int]) int {\n\tmatch o {\n\tcase Some[int] as s:\n\t\ts.Value\n\tcase None:\n\t\tUnreachable(\"none\")\n\t}\n}\n"},
	{"match-arm-first", "func f(n int) string {\n\tlet s = match n {\n\tcase 0:\n\t\tUnreachable(\"zero\")\n\tcase _:\n\t\t\"n\"\n\t}\n\ts\n}\n"},
	{"return", "func f(n int) int {\n\tif n > 0 {\n\t\treturn n\n\t}\n\treturn Unreachable(\"not positive\")\n}\n"},
	{"tail", "func f() []string { Unreachable(\"tail\") }\n"},
	{"argument", "func g(s string) int { len(s) }\nfunc f() int { g(Unreachable(\"arg\")) }\n"},
	{"argument-of-a-record", "type R { N int }\nfunc f() R { R{N: Unreachable(\"field\")} }\n"},
	{"operand", "func f(n int) int { n + Unreachable(\"operand\") }\n"},
	{"generic-tail", "func first[T](xs []T) T {\n\tif len(xs) > 0 {\n\t\treturn xs[0]\n\t}\n\tUnreachable(\"empty\")\n}\n"},
	{"generic-let", "func zero[T]() T {\n\tlet v T = Unreachable(\"no zero\")\n\tv\n}\n"},
	{"catch-arm", "type Missing {}\nfunc g(n int) int fails Missing {\n\tif n < 0 {\n\t\treturn Missing\n\t}\n\tn\n}\nfunc f() int {\n\tg(1) catch {\n\tcase Missing:\n\t\tUnreachable(\"g(1) never fails\")\n\t}\n}\n"},
	{"computed-reason", "func f(k string) int { Unreachable(\"no key \\(k)\") }\n"},
	{"exported-no-row", "func F(n int) int {\n\tif n < 0 {\n\t\tUnreachable(\"negative\")\n\t}\n\tn\n}\n"},
}

func TestUnreachableChecksInEachPosition(t *testing.T) {
	for _, tc := range unreachablePositions {
		if _, d := check.CheckSource(tc.src); len(d) > 0 {
			t.Errorf("%s: %s\n%s", tc.name, describe(d), tc.src)
		}
	}
}

// TestUnreachableTakesTheExpectedType: the call's recorded type is the
// type its position expects, which the lowering writes as Never[T].
func TestUnreachableTakesTheExpectedType(t *testing.T) {
	pkg, d := check.CheckSource("func f() int64 {\n\tlet x int64 = Unreachable(\"x\")\n\tx\n}\n")
	if len(d) > 0 {
		t.Fatal(describe(d))
	}
	found := false
	for x, c := range pkg.Info.Calls {
		if !check.IsUnreachable(c) {
			continue
		}
		found = true
		if got := pkg.Info.Types[x]; got == nil || got.String() != "int64" {
			t.Errorf("Unreachable has type %v; want int64", got)
		}
		if !c.Fails.Empty() {
			t.Errorf("Unreachable has the row %v; want none", c.Fails)
		}
	}
	if !found {
		t.Fatal("no call of Unreachable recorded")
	}
}

// TestUnreachableRefusals: a position with no expected type asks for one;
// a catch of it is a catch of an expression that cannot fail; a program
// declaration or local named Unreachable is the prelude rule's (D-D3),
// with K4's message naming the prelude function.
func TestUnreachableRefusals(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"untyped-let", "func f() int {\n\tlet x = Unreachable(\"x\")\n\tx\n}\n",
			"Unreachable(…) takes the type its position expects, and nothing here expects one; write the type"},
		{"catch", "func f() {\n\tUnreachable(\"x\") catch {\n\tcase _:\n\t\treturn\n\t}\n}\n",
			"cannot fail"},
		{"local", "func f() int {\n\tlet Unreachable = 1\n\tUnreachable\n}\n",
			"Unreachable is a prelude name (the prelude function Unreachable), which cannot be redeclared"},
		{"parameter", "func f(Unreachable int) int { Unreachable }\n",
			"Unreachable is a prelude name (the prelude function Unreachable), which cannot be redeclared"},
		{"function", "func Unreachable(reason string) int { 0 }\n",
			"Unreachable is a prelude name (the prelude function Unreachable), which cannot be redeclared"},
		{"reason-type", "func f() int { Unreachable(1) }\n",
			"int"},
		{"go-panic", "func f() { panic(\"x\") }\n",
			"for a path that cannot run, write Unreachable(\"why\")"},
		{"not-a-value", "func f() int {\n\tlet g = Unreachable\n\t0\n}\n",
			"Unreachable"},
	} {
		_, d := check.CheckSource(tc.src)
		found := false
		for _, x := range d {
			found = found || strings.Contains(x.Message, tc.want)
		}
		if !found {
			t.Errorf("%s: want a message holding %q, got %s\n%s", tc.name, tc.want, describe(d), tc.src)
		}
	}
	if !check.IsPredeclared("Unreachable") {
		t.Error("Unreachable is not predeclared")
	}
}
