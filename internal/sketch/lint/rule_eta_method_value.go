package lint

import (
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/syntax"
)

// etaMethodValue is the hint for a function literal that only calls r.f
// with its parameters, in order, where r.f as a value means the same
// (types.md, Methods 2; CV1-FIX). The checker decides it
// (check.Info.EtaMethodValue): r is a parameter or a let, never a let mut
// or a borrow, so no write can follow the take, and r.f is no S108 and has
// the literal's type. Its fix is safe: the method value binds r's value
// where it is taken, the literal reads r when it runs, and r cannot change
// in between. Prior art: Go's method value x.f; Gleam's and Elixir's
// function capture (fn(x) { f(x) } and &f/1), which their formatters and
// Credo suggest for the eta form.
var etaMethodValue = &Rule{
	Name:    "eta-method-value",
	Code:    "L162",
	Doc:     "A function literal that only calls r.f with its parameters, in order, where r is a parameter or a let, is the method value r.f; write r.f.",
	Instead: "write the method value r.f",
	Default: Hint,
	Check:   checkEtaMethodValue,
	SafeFix: true,
}

func checkEtaMethodValue(p *Pass) {
	src := p.File.Src
	skipUntil := syntax.NoPos
	for _, x := range p.fileIndex().funcLits {
		if skipUntil.IsValid() && x.Pos() < skipUntil {
			continue
		}
		sel := p.Info.EtaMethodValue(x)
		if sel == nil {
			continue
		}
		text := src[sel.Pos():sel.End()]
		fix := &report.Fix{InsertAt: int(x.Pos()), Replace: int(x.End() - x.Pos()), Text: text}
		p.ReportFix(x.Pos(), x.End(), "this literal only calls "+text+" with its parameters: it is the method value "+text, fix)
		skipUntil = x.End()
	}
}
