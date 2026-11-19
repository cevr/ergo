package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// unusedTypeParameter is the speculative-generality rule of the strict
// preset (catalog CR17): a function's type parameter that nothing in the
// function names, not its signature, another type parameter's
// constraint or a type in the body. No argument can infer it, so every
// call spells a type that changes nothing. Only parameters of the type
// kind are read: a row parameter stands in a clause. A type's parameters
// are out of scope: a phantom parameter is a design (a typed id).
var unusedTypeParameter = &Rule{
	Name:    "unused-type-parameter",
	Code:    "L116",
	Doc:     "A function's type parameter that nothing in the function names generalises nothing; delete it.",
	Instead: "delete it",
	Default: Warning,
	Check:   checkUnusedTypeParameter,
}

func checkUnusedTypeParameter(p *Pass) {
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.TypeParams == nil || f.Derived != nil || f.Extern != nil {
			continue
		}
		fn, ok := p.Info.Defs[f.Name].(*check.Func)
		if !ok || fn.Sig == nil {
			continue
		}
		for _, field := range f.TypeParams.List {
			for _, id := range field.Names {
				if tp, ok := p.Info.Defs[id].(*check.TypeParam); ok && tp.Kind == check.KindType && !namesTypeParam(p.Info, f, fn, tp) {
					p.Report(id.Pos(), id.End(), "the type parameter "+id.Name+" of "+f.Name.Name+" is named nowhere in it")
				}
			}
		}
	}
}

// namesTypeParam reports whether f names tp: in its signature, in another
// type parameter's constraint, or in the type of a body's expression or
// declared variable.
func namesTypeParam(info *check.Info, f *syntax.FuncDecl, fn *check.Func, tp *check.TypeParam) bool {
	if check.MentionsParam(fn.Sig, tp) {
		return true
	}
	found := false
	syntax.Inspect(f, func(n syntax.Node) bool {
		if found {
			return false
		}
		if x, ok := n.(syntax.Expr); ok && info.Types[x] != nil && check.MentionsParam(info.Types[x], tp) {
			found = true
		}
		if id, ok := n.(*syntax.Ident); ok {
			switch o := info.Defs[id].(type) {
			case *check.Var:
				found = found || o.Type != nil && check.MentionsParam(o.Type, tp)
			case *check.TypeParam:
				found = found || o != tp && o.Constraint != nil && check.MentionsParam(o.Constraint, tp)
			}
		}
		return !found
	})
	return found
}
