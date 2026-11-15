package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// withAtEntry is the layout rule for choosing implementations in one place
// (AGENTS.md: "each binary's cmd/<name>/main.ergo binds them in a context
// and is the only place that chooses them"; tsgo strictEffectProvide,
// effect-oxlint noInlineProvide). A with that converts a value of a
// program package's type into a capability picks the implementation; one
// that binds a capability value passed down picks nothing, and one of std
// providers (a virtual clock, a script) picks no program implementation.
// The pick stands only in an entry file (cmd/NAME/main.ergo, or a program
// of one source text), in a test file (one that declares a test or a law,
// as Go's _test.go files hold tests and their helpers), or in a function
// option functions lists as DIR.Name (DIR the package's folder under the
// module root: loopback.NewLoopback).
var withAtEntry = &Rule{
	Name:    "with-at-entry",
	Code:    "L112",
	Doc:     "A with that picks a program's implementation outside cmd/NAME/main.ergo or a file of tests and laws chooses it away from the entry; move the choice to the entry.",
	Default: Warning,
	Check:   checkWithAtEntry,
	Options: options(func() functionOptions { return functionOptions{} }, nil),
}

func checkWithAtEntry(p *Pass) {
	if p.Package.Std() || p.Package.Host() || entryFile(p.Rel()) {
		return
	}
	for _, d := range p.File.Decls {
		switch d.(type) {
		case *syntax.TestDecl, *syntax.LawDecl:
			return // a test file: tests, laws and their helpers pick freely
		}
	}
	listed := p.Options.(functionOptions).Functions
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil {
			continue
		}
		if fn, _ := p.Info.Defs[f.Name].(*check.Func); listedFunction(p.Rel(), fn, listed) {
			continue
		}
		syntax.Inspect(f.Body, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			w := p.Info.Withs[x]
			if w == nil {
				return true
			}
			for _, t := range w.ImplementationTypes() {
				if check.ProgramType(t) {
					p.Report(x.Pos(), x.Fun.End(), "this with picks a program implementation ("+t.String()+") away from the entry; bind it in cmd/NAME/main.ergo and pass the capability down")
					break
				}
			}
			return true
		})
	}
}
