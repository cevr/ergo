package check_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestFunctionRewriteShapes is the scope of a top-level rewrite law over
// functions (types.md, Laws 14; LAWFFI r2 §3.2, owner q195), seeded: each
// seed generates laws `law L(params) { lhs } == { rhs }` over a fixture of
// pure Ergo functions (Twice, Half, Join), a pure Ergo function that fails
// (Check), an impure Ergo function (Apply, which calls a function value),
// a pure Go function (strings.ToUpper, whose package is another one) and
// an impure one (os.Getenv). The checker admits a law (Law.FuncRewrite)
// exactly when a reference model of the rules does, and a refused law's
// S108 names the first rule the model finds broken. A law over Ergo
// bodies only is admitted (q195).
//
// Mutants: lf6-impure-side-admitted, lf6-same-size-admitted,
// lf6-orphan-admitted, lf6-ergo-only-law-refused (q195: the checker asks
// for a Go function), lf6-wider-failure-row-admitted,
// lf6-guarded-law-admitted, lf6-impure-row-text-stale (the refusal names the
// row the call charges: Ambient for os.Getenv, interop.md Purity and
// authority).
//
// ERGO_FUNCREWRITE_SEED=N replays one seed.
func TestFunctionRewriteShapes(t *testing.T) {
	seeds := []uint64{}
	if s := os.Getenv("ERGO_FUNCREWRITE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(24) {
			seeds = append(seeds, i+1)
		}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x667277))
		var laws []frLaw
		var b strings.Builder
		b.WriteString(frFixture)
		for i := range 16 {
			l := genFRLaw(r, fmt.Sprintf("L%d", i))
			laws = append(laws, l)
			b.WriteString(l.src + "\n\n")
		}
		src := b.String()
		pkg, diags := check.CheckSource(src)
		if pkg == nil {
			t.Fatalf("seed %d: no package\n%s", seed, src)
		}
		byName := map[string]*check.Law{}
		for _, law := range pkg.Laws {
			byName[law.Name] = law
		}
		for _, l := range laws {
			want := l.model()
			reached[want.rule] = true
			law := byName[l.name]
			var got []string
			for _, d := range diags {
				if d.Code == check.CodeType && strings.HasPrefix(d.Message, "law "+l.name+" is no rewrite: ") {
					got = append(got, d.Message)
				}
			}
			fail := func(why string) {
				t.Errorf("seed %d, %s: %s\nreplay: ERGO_FUNCREWRITE_SEED=%d go test -run '^TestFunctionRewriteShapes$' ./internal/sketch/check\nlaw: %s\nmodel: %s %q\nchecker: %v %q\n--- source\n%s\n--- diagnostics\n%s", seed, l.name, why, seed, l.src, want.rule, want.text, law != nil && law.FuncRewrite != nil, got, src, describe(diags))
			}
			switch {
			case law == nil:
				fail("no law")
			case strings.HasPrefix(want.rule, "admitted"):
				if law.FuncRewrite == nil || len(got) > 0 {
					fail("the model admits the law, the checker does not")
				}
			case law.FuncRewrite != nil || len(got) != 1:
				fail("the model refuses the law, the checker gives another verdict")
			case !strings.Contains(got[0], want.text):
				fail("the checker names another rule than the model's first")
			}
		}
	}
	if len(seeds) > 1 {
		for _, rule := range []string{"admitted-ergo-only", "admitted-go", "shape", "fewer", "guard", "pure", "failures", "orphan"} {
			if !reached[rule] {
				t.Errorf("no seed reaches the model's %s", rule)
			}
		}
	}
}

// TestFunctionRewriteRows pins the texts and positions of the diagnostics
// of a top-level rewrite law that TestFunctionRewriteShapes's fixture does
// not reach: the value class (a Go type), a guard (when) and a side that is
// not one expression.
//
// Mutants: lf6-value-class-skipped, lf6-top-when-accepted.
func TestFunctionRewriteRows(t *testing.T) {
	for _, tc := range []struct {
		src       string
		code      string
		line, col int
		msg       string
	}{
		{"import go time\n\nfunc Month(n int) time.Month { time.Month(n) }\n\nfunc Norm(m time.Month) time.Month { m }\n\nlaw V(n int) { Norm(Month(n)) } == { Month(n) }\n", check.CodeType, 7, 16,
			"law V is no rewrite: its sides are values of time.Month, which is a Go type; a rewrite is of values with key equality; write { lhs == rhs } for a property"},
		{"func Twice(s string) string { s + s }\n\nlaw W(s string) when s == \"\" { Twice(s) } == { s }\n", "S001", 3, 17,
			"a top-level rewrite law takes no when: a law over functions has no guard in v1, and a law true only under a guard stays a property"},
		{"func Twice(s string) string { s + s }\n\nlaw W(s string) { let t = Twice(s); t } == { s }\n", "S001", 3, 17,
			"a top-level rewrite law has one expression on each side: law L(x T) { f(g(x)) } == { x }"},
	} {
		_, diags := check.CheckSource(tc.src)
		if len(diags) != 1 || diags[0].Code != tc.code || diags[0].Message != tc.msg || diags[0].Line != tc.line || diags[0].Column != tc.col {
			t.Errorf("%s\nwant one diagnostic %s %d:%d %q, got\n\t%s", tc.src, tc.code, tc.line, tc.col, tc.msg, describe(diags))
		}
	}
}

const frFixture = `import go strings
import go os

type Bad {}

func Twice(s string) string { s + s }

func Half(s string) string { s[:len(s) / 2] }

func Join(a string, b string) string { a + b }

func Check(s string) string fails Bad {
	if s == "" {
		return Bad
	}
	s
}

func Apply(s string) string {
	let f = Twice
	f(s)
}

`

// frFunc is a fixture function: its call text, arity, whether it is
// pure, an Ergo body, fails Bad, and its package is this one.
type frFunc struct {
	name              string
	arity             int
	pure, ergo, fails bool
	local             bool
	impureText        string
}

var frFuncs = []frFunc{
	{name: "Twice", arity: 1, pure: true, ergo: true, local: true},
	{name: "Half", arity: 1, pure: true, ergo: true, local: true},
	{name: "Join", arity: 2, pure: true, ergo: true, local: true},
	{name: "Check", arity: 1, pure: true, ergo: true, fails: true, local: true},
	{name: "Apply", arity: 1, ergo: true, local: true, impureText: "function Apply calls"},
	{name: "strings.ToUpper", arity: 1, pure: true},
	{name: "os.Getenv", arity: 1, impureText: "Getenv needs {Ambient}"},
}

// frExpr is a generated side: a parameter, a constant, a call of a
// fixture function or (on the right) a string concatenation.
type frExpr struct {
	param string
	konst string
	fn    *frFunc
	op    bool
	args  []*frExpr
}

func (e *frExpr) text() string {
	switch {
	case e.param != "":
		return e.param
	case e.konst != "":
		return strconv.Quote(e.konst)
	case e.op:
		return e.args[0].text() + " + " + e.args[1].text()
	}
	var parts []string
	for _, a := range e.args {
		parts = append(parts, a.text())
	}
	call := e.fn.name + "(" + strings.Join(parts, ", ") + ")"
	if e.fn.fails {
		call = "try " + call
	}
	return call
}

// walk visits e and its parts in pre-order.
func (e *frExpr) walk(f func(*frExpr)) {
	f(e)
	for _, a := range e.args {
		a.walk(f)
	}
}

type frLaw struct {
	name     string
	params   []string
	where    bool
	lhs, rhs *frExpr
	src      string
}

func genFRExpr(r *rand.Rand, params []string, depth int, ops bool) *frExpr {
	if depth == 0 || r.IntN(4) == 0 {
		if r.IntN(6) == 0 {
			return &frExpr{konst: "k"}
		}
		return &frExpr{param: params[r.IntN(len(params))]}
	}
	if ops && r.IntN(5) == 0 {
		return &frExpr{op: true, args: []*frExpr{genFRExpr(r, params, depth-1, ops), genFRExpr(r, params, depth-1, ops)}}
	}
	// The pure Ergo functions are drawn most, so the admitted laws are
	// many; each other kind breaks one rule.
	var f *frFunc
	switch k := r.IntN(14); {
	case k < 9:
		f = &frFuncs[k%3]
	default:
		f = &frFuncs[3+(k-9)%4]
	}
	e := &frExpr{fn: f}
	for range f.arity {
		e.args = append(e.args, genFRExpr(r, params, depth-1, ops))
	}
	return e
}

func genFRLaw(r *rand.Rand, name string) frLaw {
	l := frLaw{name: name, params: []string{"a"}}
	if r.IntN(2) == 0 {
		l.params = append(l.params, "b")
	}
	l.where = r.IntN(12) == 0
	l.lhs = genFRExpr(r, l.params, 1+r.IntN(3), r.IntN(10) == 0)
	l.rhs = genFRExpr(r, l.params, r.IntN(3), true)
	var ps []string
	for i, p := range l.params {
		if i == 0 && l.where {
			ps = append(ps, p+" string where len("+p+") < 9")
			continue
		}
		ps = append(ps, p+" string")
	}
	l.src = "law " + name + "(" + strings.Join(ps, ", ") + ") { " + l.lhs.text() + " } == { " + l.rhs.text() + " }"
	return l
}

// frVerdict is the model's verdict: admitted (admitted-go when a side
// calls Go, admitted-ergo-only else), or the first rule broken and a text
// the diagnostic holds.
type frVerdict struct{ rule, text string }

// model is §3.2 read for the fixture, rule by rule in the checker's order.
func (l frLaw) model() frVerdict {
	var lhsCalls, rhsCalls []*frExpr
	lhsOp := false
	used := map[string]bool{}
	l.lhs.walk(func(e *frExpr) {
		switch {
		case e.fn != nil:
			lhsCalls = append(lhsCalls, e)
		case e.op:
			lhsOp = true
		case e.param != "":
			used[e.param] = true
		}
	})
	l.rhs.walk(func(e *frExpr) {
		if e.fn != nil {
			rhsCalls = append(rhsCalls, e)
		}
	})
	if lhsOp {
		return frVerdict{"shape", "an operator is not a call"}
	}
	for _, p := range l.params {
		if !used[p] {
			return frVerdict{"shape", "parameter " + p + " does not occur in its left side"}
		}
	}
	if len(lhsCalls) == 0 {
		return frVerdict{"shape", "its left side calls no declared function"}
	}
	// Rule 2 (LAWFFI LF2m): the pair (calls of Ergo bodies, calls),
	// compared in order.
	ergo := func(calls []*frExpr) int {
		n := 0
		for _, e := range calls {
			if e.fn.ergo {
				n++
			}
		}
		return n
	}
	if le, re := ergo(lhsCalls), ergo(rhsCalls); re > le || re == le && len(rhsCalls) >= len(lhsCalls) {
		return frVerdict{"fewer", fmt.Sprintf("its right side makes %d calls, %d of Ergo bodies, and its left side %d, %d of Ergo bodies", len(rhsCalls), re, len(lhsCalls), le)}
	}
	if l.where {
		return frVerdict{"guard", "parameter " + l.params[0] + " takes a where"}
	}
	for _, e := range append(append([]*frExpr(nil), lhsCalls...), rhsCalls...) {
		if !e.fn.pure {
			return frVerdict{"pure", e.fn.impureText}
		}
	}
	fails := func(calls []*frExpr) bool {
		for _, e := range calls {
			if e.fn.fails {
				return true
			}
		}
		return false
	}
	if fails(rhsCalls) && !fails(lhsCalls) {
		return frVerdict{"failures", "its right side fails {Bad} and its left side {}"}
	}
	if !lhsCalls[0].fn.local {
		return frVerdict{"orphan", "its root function ToUpper is declared in package"}
	}
	for _, e := range append(lhsCalls, rhsCalls...) {
		if !e.fn.ergo {
			return frVerdict{"admitted-go", ""}
		}
	}
	return frVerdict{"admitted-ergo-only", ""}
}
