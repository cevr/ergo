package check

import (
	"strconv"
	"strings"
	"testing"
)

// Stage E2, C16 and C17 (escaping.go): a non-escaping function value is
// called, passed to a non-escaping parameter or bound to a local, and
// captured only by a non-escaping frame.
//
// The domain is every position of the syntax where a local's name can
// stand: every field of type Expr, []Expr, *Ident or []*Ident of every
// node type, read from the syntax package's source
// (TestEscapePositionsCoverTheSyntax). Each position has programs that put
// f, a non-escaping parameter, there, with their verdicts, or a reason no
// checked program puts a use of a local there. A node type or field the
// decided parser never builds (node_inventory_test.go) is outside the
// domain by that table's reason.

const escHead = `import task

type Holder { Run escaping func(int) int }
type Box[T] { V T }
type F func(int) int
type Runner {
	Run(g func(int) int) int,
	Keep(g escaping func(int) int) int,
}
type Svc { Get() int }

func take(g escaping func(int) int) int { 0 }
func pass(g func(int) int) int { g(1) }
func gen[T](x T) int { 0 }
func later(body escaping func() int) int { 0 }
func now(body func() int) int { body() }
func give(g func(int) int) func(int) int { func(x int) int { x } }
func each(body func(k func(int) int) int) int { body(func(x int) int { x }) }

`

// escCase puts f, a non-escaping parameter of type func(int) int, at the
// position marked $. ok is the verdict: true when the checker admits the
// program; false when it is refused, by one non-escaping S108 at the
// line holding $ when want is empty, or with want as the first code when
// the position refuses f for another reason first.
type escCase struct {
	src  string
	ok   bool
	want string
}

var escPositions = map[string][]escCase{
	"CallExpr.Fun": {
		{src: "func u(f func(int) int) int {\n\t$f(1)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$f(1) + pass(f)\n}\n", ok: true},
	},
	"CallExpr.Args": {
		{src: "func u(f func(int) int) int {\n\t$pass(f)\n}\n", ok: true},
		{src: "func u(f func(int) int, k func(g func(int) int) int) int {\n\t$k(f)\n}\n", ok: true},
		{src: "func u(f func(int) int, r Runner) int {\n\t$r.Run(f)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$take(f)\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$gen(f)\n}\n"},
		{src: "func u(f func(int) int, r Runner) int {\n\t$r.Keep(f)\n}\n"},
		{src: "func u(f func(int) int, k func(g escaping func(int) int) int) int {\n\t$k(f)\n}\n"},
		{src: "func u(f func(int) int) F {\n\t$F(f)\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$len(f)\n}\n", want: "S108"},
	},
	"ParenExpr.X": {
		{src: "func u(f func(int) int) int {\n\t$(f)(1)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$take((f))\n}\n"},
	},
	"CompositeLit.Elts": {
		{src: "func u(f func(int) int) int {\n\t$let xs = []func(int) int{f}\n\txs[0](1)\n}\n"},
	},
	"CompositeLit.Base": {
		{src: "func u(f func(int) int) int {\n\t$Box{..Box{V: pass(f)}, V: 2}.V\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$Box{..Box{V: f}, V: give(f)}.V(1)\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$Box{..f, V: 2}.V\n}\n", want: "S108"},
	},
	"KeyValueExpr.Value": {
		{src: "func u(f func(int) int) Holder {\n\t$Holder{Run: f}\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$Box{V: f}.V(1)\n}\n"},
	},
	"ReturnStmt.Results": {
		{src: "func u(f func(int) int) func(int) int {\n\t$return f\n}\n"},
	},
	"ExprStmt.X": {
		{src: "func u(f func(int) int) func(int) int {\n\t$f\n}\n"},
		{src: "func u(c bool, f func(int) int) func(int) int {\n\tif c {\n\t\treturn give(f)\n\t}\n\t$f\n}\n"},
	},
	"TupleExpr.Elts": {
		{src: "func u(f func(int) int) (func(int) int, int) {\n\t$(f, 1)\n}\n"},
		{src: "func u(f func(int) int) int {\n\t$let (g, n) = (f, 1)\n\tg(n)\n}\n"},
	},
	"AssignStmt.Rhs": {
		{src: "func u(f func(int) int, g func(int) int) int {\n\tlet mut h = g\n\t$h = f\n\th(1)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\tlet mut h = func(x int) int { x }\n\t$h = f\n\th(1)\n}\n"},
		// The target outlives the value: k lives only while each runs.
		{src: "func u(f func(int) int) int {\n\tlet mut h = f\n\teach(func(k func(int) int) int {\n$\t\th = k\n\t\t0\n\t})\n\th(1)\n}\n", want: ""},
		// A discard keeps nothing.
		{src: "func u(f func(int) int) int {\n\t$_ = f\n\t0\n}\n", ok: true},
	},
	"AssignStmt.Lhs": {
		{src: "func u(f func(int) int, g func(int) int) int {\n\tlet mut h = g\n\t$h = f\n\th(1)\n}\n", ok: true},
		{src: "func u(f func(int) int, g func(int) int) int {\n\t$f = g\n\tf(1)\n}\n", want: "S108"},
	},
	"VarSpec.Values": {
		{src: "func u(f func(int) int) int {\n\t$let g = f\n\tpass(g)\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$let _ = f\n\t0\n}\n", ok: true},
		{src: "func u(f func(int) int) int {\n\t$let g func(int) int = f\n\tg(1)\n}\n"},
		{src: "func u(f func(int) int) int {\n\tlet g = f\n\t$take(g)\n}\n"},
	},
	// A dot call's receiver has the named type its function is declared
	// on (196m), so f.pass() is no call of pass.
	"SelectorExpr.X": {
		{src: "func u(f func(int) int) int {\n\t$f.pass()\n}\n", want: "S108"},
		{src: "func u(f func(int) int) int {\n\t$f.V\n}\n", want: "S108"},
	},
	"IndexExpr.X":       {{src: "func u(f func(int) int) int {\n\t$f[0]\n}\n", want: "S108"}},
	"IndexExpr.Indices": {{src: "func u(f func(int) int, xs []int) int {\n\t$xs[f]\n}\n", want: "S108"}},
	"SliceExpr.X":       {{src: "func u(f func(int) int) int {\n\t$len(f[0:1])\n}\n", want: "S108"}},
	"SliceExpr.Low":     {{src: "func u(f func(int) int, xs []int) int {\n\t$len(xs[f:])\n}\n", want: "S108"}},
	"SliceExpr.High":    {{src: "func u(f func(int) int, xs []int) int {\n\t$len(xs[:f])\n}\n", want: "S108"}},
	"TypeGuard.X": {
		{src: "func u(f func(int) int) int {\n\t$match f {\n\tcase _:\n\t\t1\n\t}\n}\n"},
	},
	"StarExpr.X":  {{src: "func u(f func(int) int) int {\n\t$*f\n}\n", want: "S108"}},
	"UnaryExpr.X": {{src: "func u(f func(int) int) bool {\n\t$!f\n}\n", want: "S108"}},
	"BinaryExpr.X": {
		{src: "func u(f func(int) int) bool {\n\t$f == f\n}\n", want: "S108"},
	},
	"BinaryExpr.Y": {
		{src: "func u(f func(int) int) int {\n\t$1 + f\n}\n", want: "S108"},
	},
	"TryExpr.X": {{src: "func u(f func(int) int) int {\n\t$try f\n}\n", want: "S108"}},
	"CatchExpr.X": {
		{src: "func u(f func(int) int) int {\n\t$f catch {\n\tcase _:\n\t\t0\n\t}\n}\n", want: "S108"},
	},
	"IfStmt.Cond":  {{src: "func u(f func(int) int) int {\n\t$if f {\n\t\t1\n\t} else {\n\t\t2\n\t}\n}\n", want: "S108"}},
	"ForStmt.Cond": {{src: "func u(f func(int) int) int {\n\t$for f {\n\t}\n\t0\n}\n", want: "S108"}},
	"RangeStmt.X":  {{src: "func u(f func(int) int) int {\n\t$for x in f {\n\t}\n\t0\n}\n", want: "S108"}},
	"CaseClause.List": {
		{src: "func u(f func(int) int, n int) int {\n\tmatch n {\n\t$case f:\n\t\t1\n\tcase _:\n\t\t2\n\t}\n}\n", want: "S108"},
	},
	"AsPattern.X": {
		{src: "func u(f func(int) int, n int) int {\n\tmatch n {\n\t$case f as m:\n\t\tm\n\tcase _:\n\t\t2\n\t}\n}\n", want: "S108"},
	},
	"AssertStmt.X": {
		// assert stands in a test body (stage E3a); a function value is
		// not a bool there, whatever its escaping verdict.
		{src: "func g(n int) int {\n\tn\n}\n\ntest T {\n\t$assert g\n}\n", want: "S108"},
	},
	"InterpolatedString.Exprs": {
		// A function part has no text form (E6a): the text-form S108, and
		// no second, non-escaping S108 for the same part.
		{src: "func u(f func(int) int) string {\n\t$\"\\(f)\"\n}\n", want: "S108"},
	},
	"KeyValueExpr.Key": {
		{src: "func u(f func(int) int) int {\n\t$len(Map[func(int) int, int]{f: 1})\n}\n", want: "S108"},
	},
	"ContextEntry.Value": {
		{src: "func u(f func(int) int) int {\n\t$with context { Svc: f } {\n\t\tSvc.Get()\n\t}\n}\n", want: "S106"},
	},
}

func escLine(head, src string) int {
	return strings.Count(head, "\n") + 1 + strings.Count(src[:strings.IndexByte(src, '$')], "\n")
}

func escCheck(t *testing.T, name, src string, ok bool, want string) {
	t.Helper()
	head := escHead
	if !strings.Contains(src, "task.") {
		head = strings.Replace(head, "import task\n", "", 1)
	}
	line := escLine(head, src)
	full := head + strings.Replace(src, "$", "", 1)
	_, diags := checkFor(full)
	switch {
	case ok && len(diags) == 0:
		return
	case !ok && want == "" && len(diags) == 1 && diags[0].Code == CodeType && diags[0].Line == line &&
		strings.Contains(diags[0].Message, "non-escaping function value"):
		return
	case !ok && want != "" && len(diags) >= 1 && diags[0].Code == want && diags[0].Line == line:
		return
	}
	verdict := "a clean check"
	switch {
	case !ok && want == "":
		verdict = "one non-escaping S108 at line " + strconv.Itoa(line)
	case !ok:
		verdict = want + " at line " + strconv.Itoa(line)
	}
	t.Errorf("%s: want %s; got %v\n%s", name, verdict, diags, full)
}

func TestEscapingPositions(t *testing.T) {
	for pos, cases := range escPositions {
		for i, c := range cases {
			escCheck(t, pos+"#"+strconv.Itoa(i), c.src, c.ok, c.want)
		}
	}
}
