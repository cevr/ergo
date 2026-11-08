package check_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Stage D1 (check-v1): `let` and `let mut` (D-D1, Q2, Q3), no shadowing
// inside a function (D-D2) and prelude names (D-D3), for every binder
// kind. Each case lists exactly the diagnostics it expects, as CODE:line,
// so a cascade fails it (one mistake, one diagnostic). Lowering runs the
// admitted forms (internal/sketch/lower stage_d1_test.go).

type d1Case struct {
	name, src string
	want      []string
}

func runD1(t *testing.T, cases []d1Case) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.TrimPrefix(tc.src, "\n")
			_, d := check.CheckSource(src)
			got := []string{}
			for _, x := range d {
				got = append(got, fmt.Sprintf("%s:%d", x.Code, x.Line))
			}
			want := append([]string{}, tc.want...)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("want %v, got %v\n%s\n%s", want, got, describe(d), src)
			}
			if len(d) == 0 {
				if gaps := check.InfoGaps(src); len(gaps) > 0 {
					t.Fatalf("Info of an accepted program: %v", gaps)
				}
			}
		})
	}
}

// D-D1: only a `let mut` local is reassigned, directly, with op=, or
// through fields and indexes (Q2); every other binder kind is immutable.
func TestLetMutReassignment(t *testing.T) {
	const decls = "type E { N int }\ntype F {}\ntype U = E | F\ntype R { N int }\n" +
		"func g() int fails E { 1 }\nfunc use(n int) {}\n"
	runD1(t, []d1Case{
		{"let", decls + "func f() int {\n\tlet x = 1\n\tx = 2\n\tx\n}\n", []string{"S108:9"}},
		{"let-op-assign", decls + "func f() int {\n\tlet x = 1\n\tx += 2\n\tx\n}\n", []string{"S108:9"}},
		{"parameter", decls + "func f(n int) int {\n\tn = 2\n\tn\n}\n", []string{"S108:8"}},
		{"literal-parameter", decls + "func f() {\n\tlet h = func(k int) { k = 1 }\n\th(1)\n}\n", []string{"S108:8"}},
		{"pattern", decls + "func f() int {\n\tlet (a, b) = (1, 2)\n\ta = 3\n\ta + b\n}\n", []string{"S108:9"}},
		{"record-pattern", decls + "func f(r R) int {\n\tlet R{N: n} = r\n\tn = 3\n\tn\n}\n", []string{"S108:9"}},
		{"as", decls + "func f(u U) int {\n\tmatch u {\n\tcase E as e:\n\t\te = E{N: 2}\n\t\te.N\n\tcase F:\n\t\t0\n\t}\n}\n", []string{"S108:10"}},
		{"match-binder", decls + "func f(n int) int {\n\tmatch n {\n\tcase k:\n\t\tk = 1\n\t\tk\n\t}\n}\n", []string{"S108:10"}},
		{"catch-as", decls + "func f() int {\n\tg() catch {\n\tcase E as e:\n\t\te = E{N: 2}\n\t\te.N\n\t}\n}\n", []string{"S108:10"}},
		{"loop", decls + "func f(xs []int) {\n\tfor x in xs {\n\t\tx = 1\n\t\tuse(x)\n\t}\n}\n", []string{"S108:9"}},
		{"package-let", decls + "let Limit = 1\nfunc f() { Limit = 2 }\n", []string{"S108:8"}},
		// A refused package-level let mut is the one diagnostic: its
		// assignments add none (D3).
		{"package-let-mut", decls + "let mut Counter = 0\nfunc f() { Counter = 2 }\n", []string{"S108:7"}},
		{"let-mut-pattern", decls + "func f() int {\n\tlet mut (a, b) = (1, 2)\n\ta + b\n}\n", []string{"S108:8"}},
		{"let-mut", decls + "func f() int {\n\tlet mut x = 1\n\tx = 2\n\tx += 3\n\tx\n}\n", nil},
		{"let-mut-names", decls + "func f() int {\n\tlet mut a, b = (1, 2)\n\ta = b\n\tb = a\n\ta + b\n}\n", nil},
		// Q2: fields and elements of a let or a parameter are immutable;
		// those of a let mut are not.
		{"let-field", decls + "func f(r R) int {\n\tlet c = r\n\tc.N = 2\n\tc.N\n}\n", []string{"S108:9"}},
		{"parameter-field", decls + "func f(r R) int {\n\tr.N = 2\n\tr.N\n}\n", []string{"S108:8"}},
		{"parameter-element", decls + "func f(xs []int) int {\n\txs[0] = 2\n\txs[0]\n}\n", []string{"S108:8"}},
		{"parameter-map-entry", decls + "func f(m Map[string, int]) int {\n\tm[\"a\"] = 2\n\tlen(m)\n}\n", []string{"S108:8"}},
		{"loop-field", decls + "func f(rs []R) {\n\tfor r in rs {\n\t\tr.N = 1\n\t\tuse(r.N)\n\t}\n}\n", []string{"S108:9"}},
		{"let-mut-field", decls + "func f(r R) int {\n\tlet mut c = r\n\tc.N = 2\n\tc.N\n}\n", nil},
		{"let-mut-element", decls + "func f(xs []int) int {\n\tlet mut ys = xs\n\tys[0] += 2\n\tys[0]\n}\n", nil},
		// A non-addressable target keeps its own diagnostic, alone.
		{"map-entry-field", decls + "func f(m Map[int, R]) {\n\tlet mut n = m\n\tn[0].N = 1\n}\n", []string{"S108:9"}},
	})
}

// D-D1 and pointers (#222 decides them): a write through a pointer changes
// the pointee, not the value its root names, so it needs no let mut; a
// borrow, written `&x` or implicit in a call to a function whose first
// parameter is *T, could change x, so x must be a let mut, which the
// borrow counts as reassigned (Q3).
func TestLetMutPointers(t *testing.T) {
	const decls = "type R { N int }\nfunc bump(r *R) { r.N = r.N + 1 }\n"
	runD1(t, []d1Case{
		{"write-through-parameter", decls + "func f(p *R) { p.N = 2 }\n", nil},
		{"write-through-deref", decls + "func f(p *R) { (*p).N = 2 }\n", nil},
		{"borrow-let", decls + "func f() int {\n\tlet r = R{N: 1}\n\tlet p = &r\n\tp.bump()\n\tr.N\n}\n", []string{"S108:5"}},
		{"borrow-let-mut", decls + "func f() int {\n\tlet mut r = R{N: 1}\n\tlet p = &r\n\tp.bump()\n\tr.N\n}\n", nil},
		{"borrow-parameter", decls + "func take(p *R) {}\nfunc f(r R) { take(&r) }\n", []string{"S108:4"}},
		{"implicit-borrow-let", decls + "func f() int {\n\tlet r = R{N: 1}\n\tr.bump()\n\tr.N\n}\n", []string{"S108:5"}},
		{"implicit-borrow-let-mut", decls + "func f() int {\n\tlet mut r = R{N: 1}\n\tr.bump()\n\tr.N\n}\n", nil},
		{"reassign-pointer", decls + "func f(a *R, b *R) {\n\tlet p = a\n\tp = b\n\tp.N = 1\n}\n", []string{"S108:5"}},
	})
}

// Q3: a let mut that is never reassigned is S110 at its name; one that is
// read nowhere keeps only its unused S110; a reassignment from a function
// literal, through a field or by a borrow counts.
func TestLetMutNeverReassigned(t *testing.T) {
	const decls = "type R { N int }\nfunc use(n int) {}\n"
	runD1(t, []d1Case{
		{"never", decls + "func f() int {\n\tlet mut n = 1\n\tn\n}\n", []string{"S110:4"}},
		{"unread", decls + "func f() {\n\tlet mut n = 1\n}\n", []string{"S110:4"}},
		{"in-literal", decls + "func f() int {\n\tlet mut n = 1\n\tlet inc = func() { n += 1 }\n\tinc()\n\tn\n}\n", nil},
		{"through-field", decls + "func f(r R) int {\n\tlet mut c = r\n\tc.N = 1\n\tc.N\n}\n", nil},
		{"in-loop-literal", decls + "func f() int {\n\tlet mut n = 0\n\tlet run = func() {\n\t\tlet mut i = 0\n\t\tfor i < 3 {\n\t\t\ti += 1\n\t\t}\n\t\tn = i\n\t}\n\trun()\n\tn\n}\n", nil},
		{"literal-local-never", decls + "func f() {\n\tlet h = func() int {\n\t\tlet mut k = 1\n\t\tk\n\t}\n\tuse(h())\n}\n", []string{"S110:5"}},
	})
}

// D-D1: `let x` with neither a type nor a value is S108; x binds with the
// invalid type, so its uses add nothing (D3). `let x T` is its zero value.
func TestUntypedLet(t *testing.T) {
	runD1(t, []d1Case{
		{"local", "func f() int {\n\tlet x\n\tx + 1\n}\n", []string{"S108:2"}},
		{"package", "let A\nfunc f() int { A + 1 }\n", []string{"S108:1"}},
		{"local-typed", "func f() int {\n\tlet x int\n\tx + 1\n}\n", nil},
		{"package-typed", "let A int\nfunc f() int { A + 1 }\n", nil},
	})
}

// D-D2: no binder reuses a name bound in an enclosing scope of the same
// function, function literals included; the inner binder is S108 and
// still binds, and the outer one counts as read (no S110 beside it).
// Sibling scopes and package names may be reused.
func TestNoShadowing(t *testing.T) {
	const decls = "type E { N int }\ntype F {}\ntype U = E | F\ntype R { N int }\ntype Cap { M() }\n" +
		"func g() int fails E { 1 }\nfunc use(n int) {}\nfunc run(f func(int)) { f(1) }\nfunc helper() int { 1 }\n"
	runD1(t, []d1Case{
		{"if-let-over-parameter", decls + "func f(x int) {\n\tif x > 0 {\n\t\tlet x = 2\n\t\tuse(x)\n\t}\n}\n", []string{"S108:12"}},
		{"if-let-over-let", decls + "func f(c bool) {\n\tlet x = 1\n\tif c {\n\t\tlet x = 2\n\t\tuse(x)\n\t}\n}\n", []string{"S108:13"}},
		{"same-block-over-parameter", decls + "func f(x int) {\n\tlet x = 2\n\tuse(x)\n}\n", []string{"S108:11"}},
		{"loop", decls + "func f(xs []int) {\n\tlet x = 0\n\tuse(x)\n\tfor x in xs {\n\t\tuse(x)\n\t}\n}\n", []string{"S108:13"}},
		{"literal-parameter", decls + "func f(k int) {\n\trun(func(k int) { use(k) })\n\tuse(k)\n}\n", []string{"S108:11"}},
		{"literal-local", decls + "func f() {\n\tlet k = 1\n\trun(func(n int) {\n\t\tlet k = n\n\t\tuse(k)\n\t})\n\tuse(k)\n}\n", []string{"S108:13"}},
		{"trailing-block-local", decls + "func each(f func()) { f() }\nfunc f() {\n\tlet k = 1\n\teach() {\n\t\tlet k = 2\n\t\tuse(k)\n\t}\n\tuse(k)\n}\n", []string{"S108:14"}},
		{"match-binder", decls + "func f(n int) int {\n\tlet k = 1\n\tmatch n {\n\tcase k:\n\t\tk\n\t}\n}\n", []string{"S108:13"}},
		{"record-pattern-binder", decls + "func f(r R) int {\n\tlet n = 1\n\tlet R{N: n} = r\n\tn\n}\n", []string{"S108:12"}},
		{"as-binder", decls + "func f(u U) int {\n\tlet e = 1\n\tmatch u {\n\tcase E as e:\n\t\te.N\n\tcase F:\n\t\te\n\t}\n}\n", []string{"S108:13"}},
		{"case-err", decls + "func f() int fails E {\n\tlet err = 1\n\tuse(err)\n\tg() catch {\n\tcase err:\n\t\treturn err\n\t}\n}\n", []string{"S108:14"}},
		{"capability-row-name", decls + "func f() needs Cap {\n\tCap.M()\n\trun(func(Cap int) { use(Cap) })\n}\n", []string{"S108:12"}},
		{"siblings", decls + "func f(c bool) int {\n\tif c {\n\t\tlet t = 1\n\t\tt\n\t} else {\n\t\tlet t = 2\n\t\tt\n\t}\n}\n", nil},
		{"sibling-arms", decls + "func f(u U) int {\n\tmatch u {\n\tcase E as e:\n\t\te.N\n\tcase F as e:\n\t\tif e == F { 1 } else { 0 }\n\t}\n}\n", nil},
		{"sibling-literals", decls + "func f() {\n\trun(func(k int) { use(k) })\n\trun(func(k int) { use(k) })\n}\n", nil},
		{"package-function", decls + "func f() int {\n\tlet helper = 2\n\thelper\n}\n", nil},
		{"package-function-parameter", decls + "func f(helper int) int { helper }\n", nil},
		{"package-function-loop", decls + "func f(xs []int) {\n\tfor helper in xs {\n\t\tuse(helper)\n\t}\n}\n", nil},
		{"after-scope", decls + "func f(c bool) int {\n\tif c {\n\t\tlet t = 1\n\t\tuse(t)\n\t}\n\tlet t = 2\n\tt\n}\n", nil},
	})
}

// D-D3: a prelude name cannot be redeclared at any scope. The refused
// declaration still binds, so its uses add nothing; names that merely
// resemble prelude names are ordinary.
func TestPreludeNamesCannotBeRedeclared(t *testing.T) {
	runD1(t, []d1Case{
		{"type", "type Option { Value int }\nfunc f(o Option) int { o.Value }\n", []string{"S108:1"}},
		{"variant", "type Opt = Some{V int} | Empty{}\n", []string{"S108:1"}},
		{"function", "func len(xs []int) int { 0 }\nfunc f(xs []int) int { len(xs) }\n", []string{"S108:1"}},
		// Sleep is a prelude operation, in no scope (prelude-scope S1):
		// a program declares its own group of that name.
		{"same-named-functions", "type A {}\ntype B {}\nfunc Sleep(a A) {}\nfunc Sleep(b B) {}\n", nil},
		{"package-let", "let Console = 1\n", []string{"S108:1"}},
		{"context", "type Store { Get() int }\ntype Mem {}\nfunc Get(m Mem) int { 1 }\nlet Random = context { Store: Mem }\nfunc f() int { with Random { Store.Get() } }\n", []string{"S108:4"}},
		{"import-alias", "import string timeout\nfunc f() string.Expired { string.Expired }\nfunc g(n string.Expired) {}\n", []string{"S108:1"}},
		{"function-type-parameter", "func g[int](x int) int { x }\n", []string{"S108:1"}},
		{"type-type-parameter", "type Box[bool] { V bool }\n", []string{"S108:1"}},
		{"parameter", "func f(string int) int { string + 1 }\n", []string{"S108:1"}},
		{"parameter-prelude-operation", "func f(Sleep int) int { Sleep + 1 }\n", nil},
		{"parameter-built-in", "func f(len int) int { len + 1 }\n", []string{"S108:1"}},
		{"literal-parameter", "func run(f func(int) int) int { f(1) }\nfunc f() int { run(func(Task int) int { Task }) }\n", []string{"S108:2"}},
		{"local-map", "func f() int {\n\tlet Map = 1\n\tMap + 1\n}\n", []string{"S108:2"}},
		{"local-ordered", "func f() int {\n\tlet ordered = 1\n\tordered\n}\n", []string{"S108:2"}},
		{"local-prelude-operation", "func f() int {\n\tlet Sleep = 1\n\tSleep\n}\n", nil},
		{"as-binder", "type E { N int }\nfunc g() int fails E { 1 }\nfunc f() int {\n\tg() catch {\n\tcase E as Decision:\n\t\tDecision.N\n\t}\n}\n", []string{"S108:5"}},
		{"loop-binder", "func f(xs []int) int {\n\tlet mut s = 0\n\tfor number in xs {\n\t\ts += number\n\t}\n\ts\n}\n", []string{"S108:3"}},
		{"resembling", "type Maybe { Value int }\nfunc f(xs []int, m Maybe) int {\n\tlet length = len(xs)\n\tlength + m.Value\n}\n", nil},
	})
	if d := check.PreludeDiagnostics(); len(d) > 0 {
		t.Fatalf("the prelude's own declarations are refused: %v", d)
	}
}

// Stage C round 7 review T1 and T2 (surviving mutants): a catch arm's rest
// name with `as` binds a placeholder, so a read of the skipped name adds
// no S104 (T1); and a name bound by one pattern and a placeholder of
// another keeps its type, so a misuse is the real type error, never a
// false S110 (T2), in either order.
func TestCatchArmPlaceholders(t *testing.T) {
	const decls = "type E { N int }\ntype F {}\n" +
		"func g() int fails E { 1 }\nfunc gf() int fails E | F { 1 }\nfunc use(n int) {}\n"
	for _, tc := range []struct{ name, src, want string }{
		{"rest-name-read", "func f() { g() catch { case a as e: use(a) } }", "S108:6"},
		{"bound-then-placeholder", "func f() int { gf() catch { case E{N: e}, a as e: e + \"x\" } }", "S108:6"},
		{"placeholder-then-bound", "func f() int { gf() catch { case a as e, E{N: e}: e + \"x\" } }", "S108:6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(decls + tc.src + "\n")
			for _, x := range d {
				if x.Code == check.CodeUnknown || x.Code == check.CodeUnused {
					t.Fatalf("a placeholder cascaded: %s", describe(d))
				}
			}
			if tc.name != "rest-name-read" {
				found := false
				for _, x := range d {
					if strings.Contains(x.Message, "a string literal takes only a string type, not int") {
						found = true
					}
				}
				if !found {
					t.Fatalf("the real type error is gone: %s", describe(d))
				}
			}
			if len(d) == 0 || fmt.Sprintf("%s:%d", d[0].Code, d[0].Line) != tc.want {
				t.Fatalf("want first %s, got %s", tc.want, describe(d))
			}
		})
	}
}
