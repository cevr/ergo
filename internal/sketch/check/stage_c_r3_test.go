package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Stage C round 2 review witnesses m4, O1, O2 and O3, at check.Check.

// m4: one discard rule (D-C1), made one statement-position rule by the
// stage C round 3 review m2 (rows.md, Totality 3). A with, try or catch
// whose value is discarded is a body with no result. Its call tails are
// discarded whatever their types, as in a match statement or a void
// function's tail; each pure value tail is S110 "this value is not used",
// in every body kind.
func TestDiscardedBodiesDropTheirTails(t *testing.T) {
	const decls = "let W = context {}\ntype E {}\nfunc g(b bool) int fails E { 1 }\nfunc i() int { 1 }\nfunc s() string { \"s\" }\n"
	// A and B are the two tails; C is a catch arm's tail.
	for _, tc := range []struct{ name, src string }{
		{"with-statement", "func f(b bool) { with W { if b { A } else { B } } }"},
		{"with-statement-in-body", "func f(b bool) int { with W { if b { A } else { B } }; 0 }"},
		{"with-statement-match", "func f(n int) { with W { match n { case 0: A; case _: B } } }"},
		{"with-trailing-void", "func each(f func()) { f() }\nfunc f(b bool) { each() { with W { if b { A } else { B } } } }"},
		{"with-caught-statement", "func f(b bool) { with W { if b { g(b) } else { B } } catch { case E: C } }"},
		{"catch-void-tail", "func f(b bool) { g(b) catch { case E: if b { A } else { B } } }"},
		{"with-under-catch-arm", "func f(b bool) { g(b) catch { case E: with W { if b { A } else { B } } } }"},
		{"catch-middle-statement", "func f(b bool) int { g(b) catch { case E: C }; 0 }"},
		{"trailing-block-tail", "func each(f func()) { f() }\nfunc f() { each() { A } }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := strings.NewReplacer("A", "i()", "B", "s()", "C", "i()").Replace(tc.src)
			src := decls + calls + "\n"
			if got, d := codeLines(src); got != "" {
				t.Fatalf("call tails: want no diagnostic, got %s", describe(d))
			}
			if gaps := check.InfoGaps(src); len(gaps) != 0 {
				t.Fatalf("Info gaps: %v", gaps)
			}
			values := strings.NewReplacer("A", "1", "B", "\"s\"", "C", "0").Replace(tc.src)
			want := strings.Count(tc.src, "A") + strings.Count(tc.src, "B") + strings.Count(tc.src, "C")
			_, d := codeLines(decls + values + "\n")
			n := 0
			for _, x := range d {
				if x.Code == "S110" && strings.Contains(x.Message, "this value is not used") {
					n++
				}
			}
			if n != want || len(d) != want {
				t.Fatalf("value tails: want %d S110 diagnostics, got %s", want, describe(d))
			}
		})
	}
	// A with whose value is used still has one type (D-C2).
	src := decls + "func f(b bool) int { 1 + with W { if b { 1 } else { \"s\" } } }\n"
	if got, d := codeLines(src); got != "S108:6" {
		t.Fatalf("a used with keeps one type: want [S108:6], got %s", describe(d))
	}
}

// O1: a with whose value is used reports a path without a value at that
// path, S107, like every other body kind; it is not "this expression has
// no value" at the with.
func TestUsedWithReportsItsPath(t *testing.T) {
	const decls = "let W = context {}\n"
	for _, tc := range []struct{ name, src, want string }{
		{"let", "func f(b bool) int {\n\tlet x = with W {\n\t\tif b { 1 }\n\t}\n\tx\n}", "S107:5"},
		{"operand", "func f(b bool) int {\n\t1 + with W {\n\t\tif b { 2 }\n\t}\n}", "S107:5"},
		{"argument", "func id(n int) int { n }\nfunc f(b bool) int {\n\tid(with W {\n\t\tif b { 2 }\n\t})\n}", "S107:6"},
		{"empty", "func f() int {\n\tlet x = with W {\n\t}\n\tx\n}", "S107:4"},
		{"statement-path", "func f(b bool) int {\n\tlet x = with W {\n\t\tif b { 2 } else {\n\t\t\tlet y = 3\n\t\t\t_ = y\n\t\t}\n\t}\n\tx\n}", "S107:7"},
		{"statement-without-else", "func f(b bool) {\n\twith W {\n\t\tif b { i() }\n\t}\n}\nfunc i() int { 1 }", ""},
		{"statement-value-without-else", "func f(b bool) {\n\twith W {\n\t\tif b { 1 }\n\t}\n}", "S110:4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			if got, d := codeLines(src); got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
		})
	}
}

// O2: an if or match branch's fallible tail passes its failure to the
// expression, as a with block's does, so an enclosing catch or try
// handles it; the two shapes agree.
func TestBranchTailsPassFailures(t *testing.T) {
	const decls = "let W = context {}\ntype E {}\ntype F {}\nfunc g(b bool) int fails E { 1 }\nfunc h(n int) int fails F { n }\n"
	for _, tc := range []struct{ name, src, want string }{
		{"if-catch", "func f(b bool, c bool) int { (if c { g(b) } else { 2 }) catch { case E: 3 } }", ""},
		{"with-catch", "func f(b bool, c bool) int { with W { if c { g(b) } else { 2 } } catch { case E: 3 } }", ""},
		{"match-catch", "func f(b bool, n int) int { (match n { case 0: g(b); case _: h(n) }) catch { case E: 4; case F: 5 } }", ""},
		{"match-catch-partial-total", "func f(b bool, n int) int { (match n { case 0: g(b); case _: h(n) }) catch { case E: 4 } }", "S103:6"},
		{"if-try", "func f(b bool, c bool) int fails E { try 1 + (if c { g(b) } else { 2 }) }", ""},
		{"if-return", "func f(b bool, c bool) int fails E { (if c { g(b) } else { 2 }) }", ""},
		{"if-unhandled", "func f(b bool, c bool) int { let x = if c { g(b) } else { 2 }; x }", "S105:6"},
		{"with-unhandled", "func f(b bool, c bool) int { let x = with W { if c { g(b) } else { 2 } }; x }", "S105:6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			if got, d := codeLines(src); got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
			if tc.want == "" {
				if gaps := check.InfoGaps(src); len(gaps) != 0 {
					t.Fatalf("Info gaps: %v", gaps)
				}
			}
		})
	}
}

// O3: try over a catch that handles the whole row, with nothing inside it
// that propagates, is a redundant try (S108), as a try on an expression
// that cannot fail is.
func TestRedundantTryOverTotalCatch(t *testing.T) {
	const decls = "type E {}\ntype F {}\nfunc g() int fails E { 1 }\nfunc k() int fails F { 2 }\nfunc h(n int) int fails E { n }\nfunc two() int fails E | F { 3 }\n"
	for _, tc := range []struct{ name, src, want string }{
		{"whole-row", "func f() int { try g() catch { case E: 1 } }", "S108:7"},
		{"whole-row-statement", "func f() { try g() catch { case E: } }", "S108:7"},
		{"whole-row-rest", "func f() int { try two() catch { case E: 1; case _: 2 } }", "S108:7"},
		{"rest-escapes", "func f() int fails F { try two() catch { case E: 1 } }", ""},
		{"inner-propagates", "func f() int fails F { try h(k()) catch { case E: 1 } }", ""},
		{"partial-record", "type R { B bool }\nfunc r() int fails R { 1 }\nfunc f() int fails R { try r() catch { case R{B: true}: 1 } }", ""},
		{"invalid-catch-no-cascade", "func f() int { try g() catch { case F: 1 } }", "S108:7"},
		{"beside-unreachable-arm", "func f() int { try g() catch { case E: 1; case E: 2 } }", "S108:7 S108:7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			if got, d := codeLines(src); got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
		})
	}
}

// m1 (contract): Info.InitOrder lists every package-level initializer in
// Go's initialization order: a let after every let its initializer
// reaches, through functions and dot calls too, otherwise in declaration
// order; a let that binds several names is one initializer.
func TestInitOrderFollowsReferences(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"let total = sum()\nfunc sum() int { base + extra }\nlet extra = twice(base)\nfunc twice(n int) int { n * 2 }\nlet base = 5\nlet (q, r) = (total / 3, total % 3)\nfunc main() {}\n", "base extra total q,r"},
		{"let a = 1\nlet b = 2\nlet c = 3\nfunc main() {}\n", "a b c"},
		{"let late = early.Get()\ntype Box { N int }\nfunc Get(b Box) int { b.N + seed }\nlet early = Box{N: 1}\nlet seed = 2\nfunc main() {}\n", "early seed late"},
	} {
		pkg, d := check.CheckSource(tc.src)
		if len(d) != 0 {
			t.Fatalf("%q: %s", tc.src, describe(d))
		}
		var got []string
		for _, in := range pkg.Info.InitOrder {
			var names []string
			for _, v := range in.Vars {
				names = append(names, v.Name)
			}
			got = append(got, strings.Join(names, ","))
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%q: InitOrder %v, want %s", tc.src, got, tc.want)
		}
	}
}
