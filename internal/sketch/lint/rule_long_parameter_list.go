package lint

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// longParameterList is the strict rule for a function that takes too
// many positional parameters (Fowler long-parameter-list; anti-slop
// measured it 12 of 12 on its own sample): more than option max
// parameters, a dot call's receiver counted. Out of scope: a method that
// provides an operation (the operation fixes its parameters), Go bindings
// (the Go signature fixes them), derived declarations, tests and laws
// (their parameters are generated inputs).
var longParameterList = &Rule{
	Name:    "long-parameter-list",
	Code:    "L141",
	Doc:     "A function with more parameters than max (default 5) asks every caller to line them up by position.",
	Instead: "group the parameters that travel together into a record and pass it",
	Default: Warning,
	Check:   checkLongParameterList,
	Options: capOptions(5),
}

func checkLongParameterList(p *Pass) {
	max := p.Options.(maxOptions).Max
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Derived != nil || f.Extern != nil {
			continue
		}
		fn, ok := p.Info.Defs[f.Name].(*check.Func)
		if !ok || fn.Sig == nil || fn.ProvidesOperation() {
			continue
		}
		if n := len(fn.Sig.Params); float64(n) > max {
			p.Report(f.Name.Pos(), f.Name.End(), fmt.Sprintf("%s takes %d parameters, more than %v", f.Name.Name, n, max))
		}
	}
}
