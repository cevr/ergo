package check_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Stage D2 (check-v1): one `for` over a slice, a Map, a Range and a Seq
// (D-D4), `a..b` as a Range value, the single-name binder rule for loop
// binders, the Map read refusal and write (D2 design ruling), and `len` as
// the only built-in function (D-D5). Each case lists exactly its
// diagnostics, so a cascade fails it. Lowering runs the admitted forms
// (internal/sketch/lower stage_d2_test.go).

// D-D4: the four domains and their bindings.
func TestForDomains(t *testing.T) {
	runD1(t, []d1Case{
		{"slice-element", "func f(xs []int64) int64 {\n\tlet mut n int64 = 0\n\tfor x in xs {\n\t\tn += x\n\t}\n\tn\n}\n", nil},
		{"slice-index-element", "func f(xs []int64) int64 {\n\tlet mut n int64 = 0\n\tfor i, x in xs {\n\t\tn += int64(i) * x\n\t}\n\tn\n}\n", nil},
		{"map-value", "func f(m Map[string, int]) int {\n\tlet mut n = 0\n\tfor v in m {\n\t\tn += v\n\t}\n\tn\n}\n", nil},
		{"map-key-value", "func f(m Map[string, int]) int {\n\tlet mut n = 0\n\tfor k, v in m {\n\t\tn += len(k) + v\n\t}\n\tn\n}\n", nil},
		// The Map rule: one binding is the value, so k is an int here.
		{"map-one-binding-is-value", "func f(m Map[string, int]) string {\n\tlet mut s = \"\"\n\tfor k in m {\n\t\ts = s + k\n\t}\n\ts\n}\n", []string{"S108:4"}},
		{"range-literal", "func f(n int) int {\n\tlet mut t = 0\n\tfor i in 0..n {\n\t\tt += i\n\t}\n\tt\n}\n", nil},
		{"range-value", "func f() int64 {\n\tlet r Range[int64] = 0..3\n\tlet mut t int64 = 0\n\tfor i in r {\n\t\tt += i\n\t}\n\tt\n}\n", nil},
		{"range-record", "func f() int {\n\tlet mut t = 0\n\tfor i in (Range[int]{Start: 1, End: 4}) {\n\t\tt += i\n\t}\n\tt\n}\n", nil},
		{"seq", "func count(n int) Seq[int] {\n\tSeq[int](func(yield func(v int) bool) {\n\t\tfor i in 0..n {\n\t\t\tif !yield(i) {\n\t\t\t\tbreak\n\t\t\t}\n\t\t}\n\t})\n}\nfunc f() int {\n\tlet mut t = 0\n\tfor v in count(3) {\n\t\tt += v\n\t}\n\tt\n}\n", nil},
		// A written function type's parameter names have Defs entries,
		// inside a function literal too (the Seq shape needs it).
		{"function-type-parameter-names", "func g(y func(v int) bool) bool {\n\ty(1)\n}\n", nil},
		{"function-type-parameter-names-in-literal", "func g() {\n\tlet h = func(y func(v int) bool) {\n\t\t_ = y(1)\n\t}\n\th(func(v int) bool { v > 0 })\n}\n", nil},
		{"for-condition", "func f() int {\n\tlet mut n = 1\n\tfor n < 10 {\n\t\tn *= 2\n\t}\n\tfor {\n\t\tbreak\n\t}\n\tn\n}\n", nil},
		{"element-pattern", "func f(ps [](int, int)) int {\n\tlet mut t = 0\n\tfor (a, b) in ps {\n\t\tt += a * b\n\t}\n\tt\n}\n", nil},
		{"blank-binding", "func f(xs []int) int {\n\tlet mut n = 0\n\tfor _ in xs {\n\t\tn += 1\n\t}\n\tfor i, _ in xs {\n\t\tn += i\n\t}\n\tn\n}\n", nil},
	})
}

// D-D4: every other domain is S108 with the Ergo form to write, and a
// refused domain or second binding still binds, so nothing cascades.
func TestForRefusals(t *testing.T) {
	runD1(t, []d1Case{
		{"string", "func f(s string) int {\n\tlet mut n = 0\n\tfor c in s {\n\t\tn += c\n\t}\n\tn\n}\n", []string{"S108:3"}},
		{"int", "func f() int {\n\tlet mut n = 0\n\tfor x in 5 {\n\t\tn += x\n\t}\n\tn\n}\n", []string{"S108:3"}},
		{"pointer", "func f(p *[]int) int {\n\tlet mut n = 0\n\tfor x in p {\n\t\tn += x\n\t}\n\tn\n}\n", []string{"S108:3"}},
		{"bool", "func f() int {\n\tlet mut n = 0\n\tfor x in true {\n\t\tn += x\n\t}\n\tn\n}\n", []string{"S108:3"}},
		{"record", "type R { N int }\nfunc f(r R) int {\n\tlet mut n = 0\n\tfor x in r {\n\t\tn += x\n\t}\n\tn\n}\n", []string{"S108:4"}},
		{"range-two-bindings", "func f() int {\n\tlet mut n = 0\n\tfor i, x in 0..3 {\n\t\tn += i + x\n\t}\n\tn\n}\n", []string{"S108:3"}},
		{"seq-two-bindings", "func f(s Seq[int]) int {\n\tlet mut n = 0\n\tfor i, x in s {\n\t\tn += i + x\n\t}\n\tn\n}\n", []string{"S108:3"}},
		{"range-of-strings", "func f() int {\n\tlet mut n = 0\n\tfor x in (Range[string]{Start: \"a\", End: \"b\"}) {\n\t\tn += len(x)\n\t}\n\tn\n}\n", []string{"S108:3"}},
		{"condition-not-bool", "func f() {\n\tfor 1 {\n\t\tbreak\n\t}\n}\n", []string{"S108:2"}},
		{"binding-immutable", "func f(xs []int) {\n\tfor y in xs {\n\t\ty = y + 1\n\t}\n}\n", []string{"S108:3"}},
		{"binding-unread", "func f(xs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tn += 1\n\t}\n\tn\n}\n", []string{"S110:3"}},
		{"position-unread", "func f(xs []int) int {\n\tlet mut n = 0\n\tfor i, x in xs {\n\t\tn += x\n\t}\n\tn\n}\n", []string{"S110:3"}},
	})
}

// D-D4: each refused domain's one diagnostic names the Ergo form to
// write, not Go's wording; a second binding over a refused domain adds
// nothing (it binds with no type); and a binder refused for its name gives
// the one D-D3 diagnostic, never S110 as well.
func TestForRefusalMessages(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"string", "func f(s string) int {\n\tlet mut n = 0\n\tfor c in s {\n\t\tn += c\n\t}\n\tn\n}\n", "S108:3:a for does not iterate a string in v1: count its bytes with for i in 0..len(s)"},
		{"int", "func f() int {\n\tlet mut n = 0\n\tfor x in 5 {\n\t\tn += x\n\t}\n\tn\n}\n", "S108:3:int is not iterable: count with for i in 0..n"},
		{"pointer", "func f(p *[]int) int {\n\tlet mut n = 0\n\tfor x in p {\n\t\tn += x\n\t}\n\tn\n}\n", "S108:3:a for does not iterate through a pointer: write for x in *p"},
		{"two-bindings-over-string", "func f(s string) int {\n\tlet mut n = 0\n\tfor i, b in s {\n\t\tn += i + b\n\t}\n\tn\n}\n", "S108:3:a for does not iterate a string in v1: count its bytes with for i in 0..len(s)"},
		{"two-bindings-over-int", "func f() int {\n\tlet mut n = 0\n\tfor i, b in 5 {\n\t\tn += i + b\n\t}\n\tn\n}\n", "S108:3:int is not iterable: count with for i in 0..n"},
		{"prelude-value-binder-unread", "func f(xs []int) {\n\tfor Some in xs {\n\t}\n}\n", "S108:2:Some is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)"},
		{"prelude-function-binder-unread", "func f(xs []int) {\n\tfor len in xs {\n\t}\n}\n", "S108:2:len is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)"},
		{"prelude-let-unread", "func f() {\n\tlet Some = 1\n}\n", "S108:2:Some is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			if len(d) != 1 {
				t.Fatalf("want one diagnostic %q; got %s", tc.want, describe(d))
			}
			if got := fmt.Sprintf("%s:%d:%s", d[0].Code, d[0].Line, d[0].Message); got != tc.want {
				t.Fatalf("want %q\ngot  %q", tc.want, got)
			}
		})
	}
}

// D-D4: `a..b` is a Range[T] for one integer type T, outside a for too.
func TestRangeValues(t *testing.T) {
	runD1(t, []d1Case{
		{"literals-take-int", "func f() int {\n\tlet r = 0..3\n\tr.End - r.Start\n}\n", nil},
		{"literal-takes-other-bound", "func f(n int64) int64 {\n\tlet r = 0..n\n\tr.End\n}\n", nil},
		{"expected-type", "func f() int64 {\n\tlet r Range[int64] = 1..3\n\tr.Start\n}\n", nil},
		{"literals-are-int-not-int64", "func f() int64 {\n\tlet r = 0..3\n\tr.End\n}\n", []string{"S108:3"}},
		{"mismatched-bounds", "func f(a int, b int64) int {\n\tlet r = a..b\n\tr.End\n}\n", []string{"S108:2"}},
		{"float-bounds", "func f() int {\n\tlet r = 1.0..2.0\n\t0\n}\n", []string{"S108:2"}},
		{"string-bounds", "func f() int {\n\tlet r = \"a\"..\"b\"\n\t0\n}\n", []string{"S108:2"}},
		{"argument", "func sum(r Range[int]) int {\n\tlet mut t = 0\n\tfor i in r {\n\t\tt += i\n\t}\n\tt\n}\nfunc f() int { sum(2..5) }\n", nil},
	})
}

// The carry "single-name for binders" (stage B round 2 review, item 4): a
// single-name loop binder follows the one binder rule of `let`, with the
// same message for each kind.
func TestForSingleNameBinders(t *testing.T) {
	const decls = "let K = 1\ntype T {}\nfunc h() {}\n"
	same := []struct{ name, let, loop string }{
		// K is a package let; the only Const objects, true and false, are
		// prelude names (the next case).
		{"package-let", "func f() {\n\tlet K = 2\n}\n", "func f(xs []int) {\n\tfor K in xs {\n\t}\n}\n"},
		{"prelude-value", "func f() {\n\tlet true = false\n\t_ = true\n}\n", "func f(xs []bool) {\n\tfor true in xs {\n\t\t_ = true\n\t}\n}\n"},
		{"prelude-type", "func f() {\n\tlet Console = 1\n\t_ = Console\n}\n", "func f(xs []int) {\n\tfor Console in xs {\n\t\t_ = Console\n\t}\n}\n"},
	}
	for _, tc := range same {
		t.Run(tc.name, func(t *testing.T) {
			_, ld := check.CheckSource(decls + tc.let)
			_, fd := check.CheckSource(decls + tc.loop)
			if len(ld) != 1 || len(fd) != 1 {
				t.Fatalf("want one diagnostic each:\nlet: %s\nfor: %s", describe(ld), describe(fd))
			}
			if ld[0].Code != fd[0].Code || ld[0].Message != fd[0].Message {
				t.Fatalf("one binder rule, one message:\nlet: %s\nfor: %s", describe(ld), describe(fd))
			}
		})
	}
	runD1(t, []d1Case{
		// A package type's name binds, as in Go (types.md, Scope 1; L5):
		// the loop variable is read, and an unread one is S110.
		{"type-name", decls + "func f(ts []T) int {\n\tlet mut n = 0\n\tfor T in ts {\n\t\tlet _ = T\n\t\tn += 1\n\t}\n\tn\n}\n", nil},
		{"type-name-unread", decls + "func f(ts []T) int {\n\tlet mut n = 0\n\tfor T in ts {\n\t\tn += 1\n\t}\n\tn\n}\n", []string{"S110:6"}},
		// A package function's name binds (D-D2's package-name exception).
		{"function-name", decls + "func f(xs []int) int {\n\tlet mut n = 0\n\tfor h in xs {\n\t\tn += h\n\t}\n\tn\n}\n", nil},
	})
}

// Info.Loops records each admitted for's domain, element and position.
func TestForInfoLoops(t *testing.T) {
	src := "func f(xs []int, m Map[string, bool], s Seq[int64]) int {\n\tlet mut n = 0\n" +
		"\tfor x in xs {\n\t\tn += x\n\t}\n" +
		"\tfor k, b in m {\n\t\tif b {\n\t\t\tn += len(k)\n\t\t}\n\t}\n" +
		"\tfor i in 0..3 {\n\t\tn += i\n\t}\n" +
		"\tfor v in s {\n\t\tn += int(v)\n\t}\n" +
		"\tn\n}\n"
	pkg, d := check.CheckSource(src)
	if len(d) != 0 {
		t.Fatalf("unexpected diagnostics: %s", describe(d))
	}
	got := map[check.LoopDomain]string{}
	for s, l := range pkg.Info.Loops {
		pos := "-"
		if l.Pos != nil {
			pos = l.Pos.String()
		}
		got[l.Domain] = l.Elem.String() + " " + pos + " " + strings.Repeat("b", len(l.Bindings)) + " line " + strconv.Itoa(strings.Count(src[:min(int(s.For), len(src))], "\n")+1)
	}
	want := map[check.LoopDomain]string{
		check.LoopSlice: "int int b line 3",
		check.LoopMap:   "bool string bb line 6",
		check.LoopRange: "int - b line 11",
		check.LoopSeq:   "int64 - b line 14",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("domain %d: want %q, got %q", k, w, got[k])
		}
	}
	if len(got) != len(want) {
		t.Errorf("want %d loops, got %v", len(want), got)
	}
}

// D2 design ruling: a Map read is refused, naming m.Get(k); a write is
// admitted; an op-assignment reads first.
func TestMapReadAndWrite(t *testing.T) {
	runD1(t, []d1Case{
		{"write", "func f(m Map[string, int]) Map[string, int] {\n\tlet mut n = m\n\tn[\"a\"] = 1\n\tn[\"b\"] = len(n)\n\tn\n}\n", nil},
		{"write-parenthesised", "func f(m Map[string, int]) Map[string, int] {\n\tlet mut n = m\n\t(n[\"a\"]) = 1\n\tn\n}\n", nil},
		{"read", "func f(m Map[string, int]) int {\n\tm[\"a\"]\n}\n", []string{"S108:2"}},
		{"read-of-defaultless-value", "func f(m Map[string, Option[int]]) Option[int] {\n\tm[\"a\"]\n}\n", []string{"S108:2"}},
		{"op-assign-reads", "func f(m Map[string, int]) Map[string, int] {\n\tlet mut n = m\n\tn[\"a\"] += 1\n\tn\n}\n", []string{"S108:3"}},
		{"read-inside-write-target", "func f(m Map[string, Map[string, int]]) {\n\tlet mut n = m\n\tn[\"a\"][\"b\"] = 1\n}\n", []string{"S108:3"}},
		{"write-in-value-key", "func f(m Map[string, int], k Map[int, string]) Map[string, int] {\n\tlet mut n = m\n\tn[k[1]] = 1\n\tn\n}\n", []string{"S108:3"}},
	})
	_, d := check.CheckSource("func f(m Map[string, int]) int {\n\tm[\"a\"]\n}\n")
	if len(d) != 1 || !strings.Contains(d[0].Message, "write m.Get(k), which returns an Option") {
		t.Fatalf("the read names m.Get(k): %s", describe(d))
	}
}

// D-D5: len takes a slice, a Map or a string, never through a pointer, and
// is the only built-in function; each removed one names its Ergo form.
func TestLenOnly(t *testing.T) {
	runD1(t, []d1Case{
		{"len-domains", "func f(xs []int, m Map[int, int], s string) int {\n\tlen(xs) + len(m) + len(s) + len(\"abc\")\n}\n", nil},
		{"len-pointer", "func f(p *[]int) int {\n\tlen(p)\n}\n", []string{"S108:2"}},
		{"len-int", "func f() int {\n\tlen(3)\n}\n", []string{"S108:2"}},
		{"index-pointer", "func f(p *[]int) int {\n\tp[0]\n}\n", []string{"S108:2"}},
	})
	forms := map[string]string{
		"append": "xs.Append(x)", "delete": "m.Delete(k)", "make": "a literal", "new": "a literal T{…}",
		"cap": "use len", "copy": "xs.Append(x)", "close": "no channels", "clear": "an empty literal",
		"print": "Console.Println", "println": "Console.Println", "min": "if a < b { a } else { b }",
		"max": "if a > b { a } else { b }", "panic": "fails", "recover": "catch",
		"complex": "no complex", "real": "no complex", "imag": "no complex",
	}
	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			_, d := check.CheckSource("func f() {\n\t_ = " + name + "\n}\n")
			if len(d) != 1 || d[0].Code != "S104" || !strings.Contains(d[0].Message, form) {
				t.Fatalf("want one S104 naming %q: %s", form, describe(d))
			}
		})
	}
}

// FuzzCheck input d1665805d2fed622 (failing at d985bea6 too): a generic
// record literal whose type arguments fail (none to infer, mixed literal
// defaults) is refused, and its values are fields, which never elide their
// type. The reference walk only visits an elided literal there (reflits.go
// elemOf), so typing neither infers from one nor types it on the refusal
// route (strayValue): no name in it resolves where the walk found no
// reference. A let mut it reassigns still counts, so no false advice joins
// the refusal.
func TestStrayGenericRecordValuesAreNotReferences(t *testing.T) {
	cases := []d1Case{
		// Three unknown A0 (196g: an undeclared parameter name is unknown,
		// also in a signature), the result type A without arguments, and
		// the literal A whose argument cannot be inferred.
		{"fuzz-input", "type A000000[A]{A A0}\ntype A[A]{A A0[A0]}\nfunc e(A0)A{A{{{{e}}}}}", []string{"S104:1", "S104:2", "S104:3", "S108:3", "S108:3"}},
		{"nothing-to-infer", "type B[T]{a T, b func()}\nfunc t() { _ = B{b: {t}} }\n", []string{"S108:2"}},
		{"positional", "type B[T]{b func(), a T}\nfunc t() { _ = B{{t}} }\n", []string{"S108:2"}},
		{"mixed-defaults", "type B[T]{a T, c T, b func()}\nfunc t() { _ = B{a: 1, c: 1.5, b: {t}} }\n", []string{"S108:2"}},
		// Inference succeeds: the field refuses the elided literal (control).
		{"inferred", "type B[T]{a T, b func()}\nfunc t() { _ = B{a: 1, b: {t}} }\n", []string{"S108:2"}},
	}
	runD1(t, cases)
	for _, src := range []string{
		"type B[T]{a T, b func()}\nfunc e() int { let mut n = 0; _ = B{b: {func() { n = 1 }}}; n }",
		"type B[T]{a T, b func()}\nfunc e() int { let mut n = 0; _ = B{a: 1, b: {func() { n = 1 }}}; n }",
	} {
		cases = append(cases, d1Case{name: src, src: src})
	}
	for _, tc := range cases {
		_, ds := check.CheckSource(tc.src)
		if p := check.RefWalkProblems(tc.src); len(p) > 0 {
			t.Errorf("%s: reference walk: %s", tc.name, strings.Join(p, "; "))
		}
		for _, d := range ds {
			if strings.Contains(d.Message, "never reassigned") {
				t.Errorf("%s: false advice from a skipped reassignment: %s", tc.name, describe(ds))
			}
		}
	}
}
