package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// testReadsUnexported is the layout hint for testing through the public
// interface (catalog EF14 and CD3; anti-slop insider-trading): a test (not
// a law) that calls an unexported function of its own package declared in
// a file that holds no test or law. The test then pins an internal: a
// refactor that keeps the package's behaviour breaks it. A helper declared
// beside the tests is the test's own and is out of scope, as is a law,
// which states a property of the code it names.
var testReadsUnexported = &Rule{
	Name:    "test-reads-unexported",
	Code:    "L135",
	Doc:     "A test that calls an unexported function of the package pins an internal; test through the exported functions.",
	Instead: "test the behaviour through the package's exported functions, or state the internal's property as a law beside it",
	Default: Warning,
	Check:   checkTestReadsUnexported,
}

func checkTestReadsUnexported(p *Pass) {
	internal := p.Memo(func() any { return internalFuncs(p) }).(map[*check.Func]bool)
	for _, d := range p.File.Decls {
		t, ok := d.(*syntax.TestDecl)
		if !ok || t.Body == nil {
			continue
		}
		syntax.Inspect(t.Body, func(n syntax.Node) bool {
			if x, ok := n.(*syntax.CallExpr); ok {
				if c := p.Info.Calls[x]; c != nil && c.Func != nil && internal[c.Func] {
					p.Report(x.Pos(), x.End(), "this test calls "+c.Func.Name+", an unexported function of the package's code")
				}
			}
			return true
		})
	}
}

// internalFuncs is the program's unexported functions declared in a file
// that holds no test or law.
func internalFuncs(p *Pass) map[*check.Func]bool {
	out := map[*check.Func]bool{}
	for _, u := range p.Program() {
		for _, f := range u.Files {
			if f.Syntax == nil || testFile(f.Syntax) {
				continue
			}
			for _, d := range f.Syntax.Decls {
				fd, ok := d.(*syntax.FuncDecl)
				if !ok {
					continue
				}
				if fn, ok := u.Package.Info.Defs[fd.Name].(*check.Func); ok && !fn.Exported() && fn != u.Package.Entry() {
					out[fn] = true
				}
			}
		}
	}
	return out
}
