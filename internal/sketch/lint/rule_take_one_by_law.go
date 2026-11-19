package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// takeOneByLaw is the speed hint for building a whole result to take one
// element of it (SKILL-RULES SR-ERGO-5, SR-P02: do not sort to take the
// first or last): the first or last element of a call's result, where a
// law of the called function's package states that element is another
// function's result for the same arguments, at the very argument types
// the law was tested at (lawtake.go). The result read through a let
// counts when the let is read only so: by the first and last elements and
// its length. Until a compiler rewrite admits the law (LF6), the author
// makes the change; the hint never rewrites. Laws and tests are skipped:
// a law states the equation itself.
var takeOneByLaw = &Rule{
	Name:    "take-one-by-law",
	Code:    "L151",
	Doc:     "The first or last element of a call's result, which a law states is another function's result for the same arguments, built in full to take one element.",
	Instead: "call the function the law names, which gives the element without building the whole result",
	Default: Hint,
	Check:   checkTakeOneByLaw,
}

func checkTakeOneByLaw(p *Pass) {
	lt := p.Memo(func() any { return lawTakes{} }).(lawTakes)
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil {
			continue
		}
		lets := map[*check.Var]syntax.Expr{}
		syntax.Inspect(f.Body, func(n syntax.Node) bool {
			if s, ok := n.(*syntax.DeclStmt); ok && s.Decl.Mut == syntax.NoPos && len(s.Decl.Specs) == 1 {
				sp := s.Decl.Specs[0]
				if len(sp.Names) == 1 && len(sp.Values) == 1 {
					if v, ok := p.Info.Defs[sp.Names[0]].(*check.Var); ok {
						lets[v] = sp.Values[0]
					}
				}
			}
			return true
		})
		var found []*syntax.IndexExpr
		facts := map[*syntax.IndexExpr]takeFact{}
		covered := map[*syntax.Ident]bool{} // a let's reads a fact covers
		bad := map[*check.Var]bool{}
		syntax.Inspect(f.Body, func(n syntax.Node) bool {
			ix, ok := n.(*syntax.IndexExpr)
			if !ok {
				return true
			}
			value := ix.X
			id, isName := unparen(ix.X).(*syntax.Ident)
			if isName {
				v, _ := p.Info.Uses[id].(*check.Var)
				if value = lets[v]; value == nil {
					return true
				}
			}
			if fact, ok := takeSite(p.Info, lt, ix, value); ok {
				found = append(found, ix)
				facts[ix] = fact
				if isName {
					markReads(ix, covered)
				}
			}
			return true
		})
		syntax.Inspect(f.Body, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.CallExpr); ok && len(c.Args) == 1 && builtinLen(p.Info, c) {
				if id, ok := unparen(c.Args[0]).(*syntax.Ident); ok {
					covered[id] = true
				}
			}
			if id, ok := n.(*syntax.Ident); ok && !covered[id] && p.Info.Defs[id] == nil {
				if v, ok := p.Info.Uses[id].(*check.Var); ok && lets[v] != nil {
					bad[v] = true
				}
			}
			return true
		})
		for _, ix := range found {
			if id, ok := unparen(ix.X).(*syntax.Ident); ok {
				if v, _ := p.Info.Uses[id].(*check.Var); bad[v] {
					continue
				}
			}
			fact := facts[ix]
			p.Report(ix.Pos(), ix.End(), "law "+fact.law+" states "+fact.one.Name+" of the same arguments gives this element")
		}
	}
}
