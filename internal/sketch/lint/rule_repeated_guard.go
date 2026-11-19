package lint

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// repeatedGuard is the defensive rule of the strict preset (catalog CR36,
// hand validation in place of a schema): the same guard on a parameter of
// the same type opens three or more functions of the program. A guard is
// an if, written directly in the body with no else, that refuses the
// value (its block ends in a return of a failure of the function's error
// row; an early return of an ordinary value handles a case, as `if q ==
// "" { return 0 }`, and is no guard: the 2026-11-18 read sample) on a
// condition that reads one
// parameter and nothing but constants, len, comparisons, !, && and ||.
// Two guards are the same when their conditions have the same shape with
// the parameter in the same places and the same constants. The values the
// guards refuse are a type of their own: a refined type checks them once,
// where the value is made.
var repeatedGuard = &Rule{
	Name:    "repeated-guard",
	Code:    "L129",
	Doc:     "The same guard on a parameter of the same type in three or more functions validates by hand what a refined type would check once.",
	Instead: "declare a refined type (type T base where …) for the values the guard accepts, and take it as the parameter's type",
	Default: Warning,
	Check:   checkRepeatedGuard,
}

// guardKey is a guard's parameter type and its condition's shape.
type guardKey struct {
	t     check.Type
	shape string
}

// guardSite is one guard: its function and its condition.
type guardSite struct {
	fn   *check.Func
	cond syntax.Expr
}

// minGuardFuncs is how many functions must open with the same guard.
const minGuardFuncs = 3

func checkRepeatedGuard(p *Pass) {
	sites := p.Memo(func() any { return guardsOf(p) }).(map[guardKey][]guardSite)
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil {
			continue
		}
		fn, ok := p.Info.Defs[f.Name].(*check.Func)
		if !ok {
			continue
		}
		for _, g := range guardsIn(p.Info, f) {
			funcs := map[*check.Func]bool{}
			for _, s := range sites[g.key] {
				funcs[s.fn] = true
			}
			if len(funcs) >= minGuardFuncs && funcs[fn] {
				p.Report(g.cond.Pos(), g.cond.End(), fmt.Sprintf("this guard on a parameter opens %d functions of the program", len(funcs)))
			}
		}
	}
}

// guardsOf is every guard of the program's functions, by key.
func guardsOf(p *Pass) map[guardKey][]guardSite {
	out := map[guardKey][]guardSite{}
	for _, u := range p.Program() {
		info := u.Package.Info
		for _, file := range u.Files {
			if file.Syntax == nil {
				continue
			}
			for _, d := range file.Syntax.Decls {
				f, ok := d.(*syntax.FuncDecl)
				if !ok || f.Body == nil {
					continue
				}
				fn, ok := info.Defs[f.Name].(*check.Func)
				if !ok {
					continue
				}
				for _, g := range guardsIn(info, f) {
					out[g.key] = append(out[g.key], guardSite{fn: fn, cond: g.cond})
				}
			}
		}
	}
	return out
}

// guard is one guard of a function.
type guard struct {
	key  guardKey
	cond syntax.Expr
}

// guardsIn is the guards that open the function f.
func guardsIn(info *check.Info, f *syntax.FuncDecl) []guard {
	if f.Derived != nil || f.Extern != nil || f.Type.Params == nil {
		return nil
	}
	fn, ok := info.Defs[f.Name].(*check.Func)
	if !ok || fn.Sig == nil || fn.Sig.Errors == nil {
		return nil
	}
	params := map[*check.Var]bool{}
	for _, field := range f.Type.Params.List {
		for _, id := range field.Names {
			if v, ok := info.Defs[id].(*check.Var); ok {
				params[v] = true
			}
		}
	}
	var out []guard
	for _, s := range f.Body.List {
		is, ok := s.(*syntax.IfStmt)
		if !ok || is.Else != nil || is.Body == nil || len(is.Body.List) == 0 {
			continue
		}
		ret, ok := is.Body.List[len(is.Body.List)-1].(*syntax.ReturnStmt)
		if !ok || !returnsFailure(info, fn, ret) {
			continue
		}
		var param *check.Var
		shape, ok := guardShape(info, is.Cond, params, &param)
		if ok && param != nil {
			out = append(out, guard{key: guardKey{t: param.Type, shape: string(shape)}, cond: is.Cond})
		}
	}
	return out
}

// returnsFailure reports a return of a failure: a value whose type is
// in the function's error row.
func returnsFailure(info *check.Info, fn *check.Func, ret *syntax.ReturnStmt) bool {
	if len(ret.Results) != 1 {
		return false
	}
	t, ok := info.Types[ret.Results[0]].(*check.Named)
	if !ok {
		return false
	}
	for _, e := range fn.Sig.Errors.Elems() {
		if n, ok := e.(*check.Named); ok && n.Obj == t.Obj {
			return true
		}
	}
	return false
}

// guardShape writes the shape of a guard's condition: its operators and
// constants, with the one parameter it reads as $. It fails on anything
// else, or a second parameter.
func guardShape(info *check.Info, x syntax.Expr, params map[*check.Var]bool, param **check.Var) ([]byte, bool) {
	x = unparen(x)
	if v := info.Values[x]; v != nil {
		return fmt.Appendf(nil, "c%d:%s", v.Kind(), v.ExactString()), true
	}
	switch x := x.(type) {
	case *syntax.Ident:
		v, ok := info.Uses[x].(*check.Var)
		if !ok || !params[v] || *param != nil && *param != v {
			return nil, false
		}
		*param = v
		return []byte("$"), true
	case *syntax.UnaryExpr:
		if x.Op != syntax.NOT {
			return nil, false
		}
		inner, ok := guardShape(info, x.X, params, param)
		return fmt.Appendf(nil, "!(%s)", inner), ok
	case *syntax.BinaryExpr:
		switch x.Op {
		case syntax.LAND, syntax.LOR, syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ:
		default:
			return nil, false
		}
		l, ok := guardShape(info, x.X, params, param)
		if !ok {
			return nil, false
		}
		r, ok := guardShape(info, x.Y, params, param)
		return fmt.Appendf(nil, "(%s %d %s)", l, x.Op, r), ok
	case *syntax.CallExpr:
		c := info.Calls[x]
		if c == nil || c.Kind != check.CallLen || len(x.Args) != 1 {
			return nil, false
		}
		inner, ok := guardShape(info, x.Args[0], params, param)
		return fmt.Appendf(nil, "len(%s)", inner), ok
	}
	return nil, false
}
