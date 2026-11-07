package lower

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"
	"testing"
)

// TestMatchTreeShape (M1, red acceptance check A): a pattern match lowers
// to a decision tree. For the function that holds each fixed match, the
// emitted Go
//  1. calls no ergoIs helper;
//  2. asserts no type: only `.(type)` switches narrow;
//  3. tests each column at most once on every path (a switch operand, an
//     ergoIs argument or an == operand, after resolving a type-switch
//     variable to the value it narrows);
//  4. holds each arm's literal exactly once: an arm body is never copied.
//
// A pathological match whose tree grows exponentially (Jacobs 2021 §5,
// scaled) keeps the flat lowering under a fixed size bound.

// treeCommonTypes are the unions of full36 and mixed.
const treeCommonTypes = `type A0 {}
type A1 {}
type A2 { N int }
type A3 {}
type A4 {}
type A5 {}
type A = A0 | A1 | A2 | A3 | A4 | A5
type B0 {}
type B1 {}
type B2 { S string }
type B3 {}
type B4 {}
type B5 {}
type B = B0 | B1 | B2 | B3 | B4 | B5
`

// treeBreakerDecls is the Breaker step of
// check/testdata/fixtures/positive/machine-step-total.ergo without its
// Probe row, so it is pure, and its machine plan.
const treeBreakerDecls = `type Closed { Failures int64 }
type Open { Endpoint string }
type State = Closed | Open
type Failure { Endpoint string }
type Retry {}
type Signal = Failure | Retry

func step(s State, sig Signal) Decision[State, string] {
	match (s, sig) {
	case (Closed as st, Failure as e):
		if st.Failures >= 2 {
			return Enter{Open{e.Endpoint}}
		}
		return Stay{Closed{st.Failures + 1}}
	case (Open as st, Retry):
		return Stay{st}
	case (Closed, Retry):
		return Done{"arm2"}
	case (Open, Failure):
		return Done{"arm3"}
	}
}

type Breaker machine[State, Signal] {
	initial: Closed{0},
	step:    step,
}
`

// treeMixedFunc is the mixed match: wildcards that the tree copies into
// several branches, a binding, a record with a constant, an or-pattern,
// constants inside records and a final wildcard.
const treeMixedFunc = `func pickMixed(a A, b B) string {
	match (a, b) {
	case (A0, _):
		return "arm0"
	case (_, B3):
		return "arm1"
	case (A1, B2 as y):
		return "arm2" + y.S
	case (A2{N: 1}, _):
		return "arm3"
	case (A3, B0), (A4, B0):
		return "arm4"
	case (A5, B2{S: "x"}):
		return "arm5"
	case (A2{N: 2}, B4):
		return "arm6"
	case (A4, B5):
		return "arm7"
	case (A1, _):
		return "arm8"
	case (_, _):
		return "arm9"
	}
}
`

// treeFull36Func is the full product of two six-variant unions.
func treeFull36Func() string {
	var b strings.Builder
	b.WriteString("func pick36(a A, b B) string {\n\tmatch (a, b) {\n")
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			fmt.Fprintf(&b, "\tcase (A%d, B%d):\n\t\treturn \"%d.%d\"\n", i, j, i, j)
		}
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

const treeNestedFunc = `func nested(o Option[State]) string {
	match o {
	case Some{Value: Closed{Failures: 0}}:
		"arm0"
	case Some{Value: Open as op}:
		"arm1" + op.Endpoint
	case Some{Value: _}:
		"arm2"
	case None:
		"arm3"
	}
}
`

// treePathoPairs is k of the pathological match: 2k bool subjects, and
// row i tests subjects i and k+i, so a decision tree copies the later rows
// into both branches of every test: about 2^k nodes.
const treePathoPairs = 10

func treePathoFunc() string {
	var b strings.Builder
	n := 2 * treePathoPairs
	var params, subjects []string
	for i := 0; i < n; i++ {
		params = append(params, fmt.Sprintf("x%d bool", i))
		subjects = append(subjects, fmt.Sprintf("x%d", i))
	}
	fmt.Fprintf(&b, "func patho(%s) string {\n\tmatch (%s) {\n", strings.Join(params, ", "), strings.Join(subjects, ", "))
	for r := 0; r < treePathoPairs; r++ {
		cells := make([]string, n)
		for i := range cells {
			cells[i] = "_"
		}
		cells[r], cells[treePathoPairs+r] = "true", "true"
		fmt.Fprintf(&b, "\tcase (%s):\n\t\treturn \"arm%d\"\n", strings.Join(cells, ", "), r)
	}
	fmt.Fprintf(&b, "\tcase _:\n\t\treturn \"arm%d\"\n\t}\n}\n", treePathoPairs)
	return b.String()
}

// treePathoBound is the size bound of patho's emitted function, in bytes
// of Go without //line directives: the flat form is about 3 KiB; a tree
// is hundreds of KiB.
const treePathoBound = 8 << 10

// treeShapeCase is one fixed source and the function that holds its match.
type treeShapeCase struct {
	name, src, fn string
	literals      []string
}

func treeShapeCases() []treeShapeCase {
	arms := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, "arm"+strconv.Itoa(i))
		}
		return out
	}
	var full []string
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			full = append(full, fmt.Sprintf("%d.%d", i, j))
		}
	}
	return []treeShapeCase{
		{"breaker", treeBreakerDecls + "\nfunc main() {}\n", "step", []string{"arm2", "arm3"}},
		{"full36", treeCommonTypes + treeFull36Func() + "\nfunc main() {}\n", "pick36", full},
		{"mixed", treeCommonTypes + treeMixedFunc + "\nfunc main() {}\n", "pickMixed", arms(10)},
		{"nested", treeBreakerDecls + treeNestedFunc + "\nfunc main() {}\n", "nested", arms(4)},
	}
}

func TestMatchTreeShape(t *testing.T) {
	for _, c := range treeShapeCases() {
		t.Run(c.name, func(t *testing.T) {
			src := stripDirectives(mustLower(t, c.name, c.src).mainGo())
			fn, helper := treeFunc(t, src, c.fn)
			for _, problem := range treeShapeProblems(fn, helper, c.literals) {
				t.Error(problem)
			}
			if t.Failed() {
				t.Logf("emitted %s:\n%s", c.fn, treeFuncText(src, fn))
			}
		})
	}
	t.Run("patho", func(t *testing.T) {
		src := stripDirectives(mustLower(t, "patho", treePathoFunc()+"\nfunc main() {}\n").mainGo())
		fn, _ := treeFunc(t, src, "patho")
		text := treeFuncText(src, fn)
		if len(text) > treePathoBound {
			t.Errorf("patho's emitted function is %d bytes, over the bound %d: the tree's node bound did not stop it", len(text), treePathoBound)
		}
		nested := 0
		ast.Inspect(fn, func(n ast.Node) bool {
			if s, ok := n.(*ast.SwitchStmt); ok && s.Tag != nil {
				nested++
			}
			return true
		})
		if nested > 0 || !strings.Contains(text, "switch {") {
			t.Errorf("patho does not use the flat lowering (%d tag switches)", nested)
		}
	})
}

// treeFunc parses the emitted Go and returns the function name and the
// name of the ergoIs helper ("" when none is emitted).
func treeFunc(t *testing.T, src, name string) (*ast.FuncDecl, string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatalf("emitted Go does not parse: %v\n%s", err, src)
	}
	var fn *ast.FuncDecl
	helper := ""
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil {
			continue
		}
		if fd.Name.Name == name {
			fn = fd
		}
		if treeIsHelper(fd) {
			helper = fd.Name.Name
		}
	}
	if fn == nil {
		t.Fatalf("no function %s in the emitted Go:\n%s", name, src)
	}
	return fn, helper
}

// treeIsHelper reports the shape of the ergoIs helper: func F[T any](x
// any) bool { _, ok := x.(T); return ok }.
func treeIsHelper(fd *ast.FuncDecl) bool {
	if fd.Type.TypeParams == nil || len(fd.Type.TypeParams.List) != 1 || fd.Body == nil || len(fd.Body.List) != 2 {
		return false
	}
	as, ok := fd.Body.List[0].(*ast.AssignStmt)
	if !ok || len(as.Rhs) != 1 {
		return false
	}
	ta, ok := as.Rhs[0].(*ast.TypeAssertExpr)
	return ok && ta.Type != nil
}

func treeFuncText(src string, fn *ast.FuncDecl) string {
	return src[fn.Pos()-1 : fn.End()-1]
}

// treeShapeProblems checks rules 1 to 4 on fn.
func treeShapeProblems(fn *ast.FuncDecl, helper string, literals []string) []string {
	var problems []string
	ast.Inspect(fn, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if helper != "" && treeCallee(n.Fun) == helper {
				problems = append(problems, "rule 1: a call of the helper "+helper+": "+types.ExprString(n))
			}
		case *ast.TypeAssertExpr:
			if n.Type != nil {
				problems = append(problems, "rule 2: a type assertion "+types.ExprString(n))
			}
		}
		return true
	})
	w := &treePathWalk{helper: helper}
	w.list(fn.Body.List, map[string]bool{}, map[string]string{})
	problems = append(problems, w.problems...)
	counts := map[string]int{}
	ast.Inspect(fn, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				counts[s]++
			}
		}
		return true
	})
	for _, l := range literals {
		if counts[l] != 1 {
			problems = append(problems, fmt.Sprintf("rule 4: the arm literal %q occurs %d times", l, counts[l]))
		}
	}
	return problems
}

// treeCallee is the name a call's function expression names, through an
// instantiation.
func treeCallee(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return treeCallee(x.X)
	case *ast.IndexListExpr:
		return treeCallee(x.X)
	}
	return ""
}

// treePathWalk follows every path through nested switches and records a
// column tested twice on one path (rule 3).
type treePathWalk struct {
	helper   string
	problems []string
}

func (w *treePathWalk) test(path map[string]bool, col string) {
	if path[col] {
		w.problems = append(w.problems, "rule 3: the column "+col+" is tested again on one path")
	}
	path[col] = true
}

func treeCopy[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// resolve is a column's text, with each type-switch variable replaced by
// the value it narrows.
func (w *treePathWalk) resolve(x ast.Expr, bound map[string]string) string {
	switch x := x.(type) {
	case *ast.Ident:
		if v, ok := bound[x.Name]; ok {
			return v
		}
		return x.Name
	case *ast.SelectorExpr:
		return w.resolve(x.X, bound) + "." + x.Sel.Name
	case *ast.ParenExpr:
		return w.resolve(x.X, bound)
	case *ast.TypeAssertExpr:
		return w.resolve(x.X, bound) + ".(" + types.ExprString(x.Type) + ")"
	}
	return types.ExprString(x)
}

// conds records the columns a flat case condition tests.
func (w *treePathWalk) conds(x ast.Expr, path map[string]bool, bound map[string]string) {
	ast.Inspect(x, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if w.helper != "" && treeCallee(n.Fun) == w.helper && len(n.Args) == 1 {
				w.test(path, w.resolve(n.Args[0], bound))
			}
		case *ast.BinaryExpr:
			if n.Op == token.EQL {
				w.test(path, w.resolve(n.X, bound))
			}
		}
		return true
	})
}

func (w *treePathWalk) list(list []ast.Stmt, path map[string]bool, bound map[string]string) {
	for _, s := range list {
		w.stmt(s, path, bound)
	}
}

func (w *treePathWalk) stmt(s ast.Stmt, path map[string]bool, bound map[string]string) {
	switch s := s.(type) {
	case *ast.BlockStmt:
		w.list(s.List, path, bound)
	case *ast.LabeledStmt:
		w.stmt(s.Stmt, path, bound)
	case *ast.IfStmt:
		w.list(s.Body.List, treeCopy(path), bound)
		if s.Else != nil {
			w.stmt(s.Else, treeCopy(path), bound)
		}
	case *ast.ForStmt:
		w.list(s.Body.List, treeCopy(path), bound)
	case *ast.RangeStmt:
		w.list(s.Body.List, treeCopy(path), bound)
	case *ast.SwitchStmt:
		if s.Tag == nil {
			// A flat switch: the path to a case runs every earlier
			// case's condition.
			p := treeCopy(path)
			for _, c := range s.Body.List {
				cc := c.(*ast.CaseClause)
				for _, x := range cc.List {
					w.conds(x, p, bound)
				}
				w.list(cc.Body, treeCopy(p), bound)
			}
			return
		}
		p := treeCopy(path)
		w.test(p, w.resolve(s.Tag, bound))
		for _, c := range s.Body.List {
			w.list(c.(*ast.CaseClause).Body, treeCopy(p), bound)
		}
	case *ast.TypeSwitchStmt:
		var x ast.Expr
		name := ""
		switch a := s.Assign.(type) {
		case *ast.AssignStmt:
			x = a.Rhs[0].(*ast.TypeAssertExpr).X
			name = a.Lhs[0].(*ast.Ident).Name
		case *ast.ExprStmt:
			x = a.X.(*ast.TypeAssertExpr).X
		}
		p := treeCopy(path)
		col := w.resolve(x, bound)
		w.test(p, col)
		b := treeCopy(bound)
		if name != "" {
			b[name] = col
		}
		for _, c := range s.Body.List {
			w.list(c.(*ast.CaseClause).Body, treeCopy(p), b)
		}
	}
}
