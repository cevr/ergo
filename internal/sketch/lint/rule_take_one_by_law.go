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
	fi := p.fileIndex()
	if len(fi.indices) == 0 {
		return
	}
	lt := p.Memo(func() any { return lawTakes{} }).(lawTakes)
	for k, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil {
			continue
		}
		dr := fi.decls[k]
		declIndices := fi.indices[dr.indexStart:dr.indexEnd]
		if len(declIndices) == 0 {
			continue
		}
		bStart, bEnd := f.Body.Pos(), f.Body.End()
		hasIdx := false
		for _, ix := range declIndices {
			if ix.Pos() >= bStart && ix.Pos() < bEnd {
				hasIdx = true
				break
			}
		}
		if !hasIdx {
			continue
		}
		var lets map[*check.Var]syntax.Expr
		for _, s := range fi.declStmts[dr.declStmtStart:dr.declStmtEnd] {
			if s.Decl.Mut == syntax.NoPos && len(s.Decl.Specs) == 1 {
				sp := s.Decl.Specs[0]
				if len(sp.Names) == 1 && len(sp.Values) == 1 {
					if _, isCall := unparen(sp.Values[0]).(*syntax.CallExpr); isCall {
						if v, ok := p.Info.Defs[sp.Names[0]].(*check.Var); ok {
							if lets == nil {
								lets = map[*check.Var]syntax.Expr{}
							}
							lets[v] = sp.Values[0]
						}
					}
				}
			}
		}
		var found []*syntax.IndexExpr
		var facts map[*syntax.IndexExpr]takeFact
		var covered map[*syntax.Ident]bool
		hasLetFound := false
		for _, ix := range declIndices {
			if ix.Pos() < bStart || ix.Pos() >= bEnd {
				continue
			}
			value := ix.X
			id, isName := unparen(ix.X).(*syntax.Ident)
			if isName {
				if lets == nil {
					continue
				}
				v, _ := p.Info.Uses[id].(*check.Var)
				if value = lets[v]; value == nil {
					continue
				}
			}
			if fact, ok := takeSite(p.Info, lt, ix, value); ok {
				if facts == nil {
					facts = map[*syntax.IndexExpr]takeFact{}
				}
				found = append(found, ix)
				facts[ix] = fact
				if isName {
					hasLetFound = true
					if covered == nil {
						covered = map[*syntax.Ident]bool{}
					}
					markReads(ix, covered)
				}
			}
		}
		if len(found) == 0 {
			continue
		}
		var bad map[*check.Var]bool
		if hasLetFound {
			bad = map[*check.Var]bool{}
			for _, c := range fi.calls[dr.callStart:dr.callEnd] {
				if c.Pos() >= bStart && c.Pos() < bEnd && len(c.Args) == 1 && builtinLen(p.Info, c) {
					if id, ok := unparen(c.Args[0]).(*syntax.Ident); ok {
						covered[id] = true
					}
				}
			}
			for _, id := range fi.idents[dr.identStart:dr.identEnd] {
				if id.Pos() >= bStart && id.Pos() < bEnd && !covered[id] && p.Info.Defs[id] == nil {
					if v, ok := p.Info.Uses[id].(*check.Var); ok && lets[v] != nil {
						bad[v] = true
					}
				}
			}
		}
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
