package lint

import (
	"go/constant"
	"go/token"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// constantArgument is the strict rule for a parameter no caller varies
// (anti-slop speculative-generality; Fowler): in a closed program (the
// run's root has main, so every caller is in it), a parameter of a
// function called two or more times, to which every call passes the same
// constant, calls in tests and laws aside (a fixture a test passes is the
// test's choice). The parameter is a constant the function's callers
// spell out.
// Out of scope: main, a method that provides an operation, Go bindings,
// derived declarations, and a function used as a value (a caller through
// the value is not seen).
var constantArgument = &Rule{
	Name:    "constant-argument",
	Code:    "L140",
	Doc:     "A parameter to which every call passes the same constant is a constant the callers spell out.",
	Instead: "drop the parameter and use the constant inside the function",
	Default: Warning,
	Check:   checkConstantArgument,
}

func checkConstantArgument(p *Pass) {
	if !p.Closed() {
		return
	}
	sites := p.Memo(func() any { return callSitesOf(p, true) }).(*callSites)
	entry := p.Package.Entry()
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Derived != nil || f.Extern != nil || f.Body == nil || f.Type.Params == nil {
			continue
		}
		fn, ok := p.Info.Defs[f.Name].(*check.Func)
		if !ok || fn == entry || fn.ProvidesOperation() || sites.value[fn] || len(sites.calls[fn]) < 2 {
			continue
		}
		index := -1
		for _, field := range f.Type.Params.List {
			for _, id := range field.Names {
				index++
				if v, ok := sites.sameConstantAt(fn, index); ok {
					p.Report(id.Pos(), id.End(), "every call of "+f.Name.Name+" passes "+v.ExactString()+" as "+id.Name)
				}
			}
		}
	}
}

// sameConstantAt is the constant every call of fn passes at parameter
// index i, if every call passes the same one.
func (s *callSites) sameConstantAt(fn *check.Func, i int) (constant.Value, bool) {
	var first constant.Value
	for _, args := range s.calls[fn] {
		if i >= len(args) {
			return nil, false
		}
		v := s.info[args[i]].Values[args[i]]
		if v == nil {
			return nil, false
		}
		if first == nil {
			first = v
			continue
		}
		if first.Kind() != v.Kind() || !constant.Compare(first, token.EQL, v) {
			return nil, false
		}
	}
	return first, first != nil
}
