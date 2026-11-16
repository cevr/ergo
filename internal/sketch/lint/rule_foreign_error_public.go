package lint

import "ergo.local/prototype/internal/sketch/check"

// foreignErrorPublic is the justifier rule for go.Error in an exported
// function's written error row (plan §3; interop.md: map a Go error to a
// closed union at the boundary).
var foreignErrorPublic = &Rule{
	Name:    "foreign-error-public",
	Code:    "L102",
	Doc:     "An exported function whose written fails row holds go.Error passes Go's open error to its callers; map it to a closed union of error types.",
	Default: Warning,
	Check:   checkForeignErrorPublic,
}

func checkForeignErrorPublic(p *Pass) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	inFile := map[any]bool{}
	for _, d := range p.File.Decls {
		inFile[d] = true
	}
	for _, f := range p.Package.Funcs {
		// An extern has no body: it is the Go binding itself, not a
		// function that could map the error.
		if f.Decl == nil || f.Decl.Body == nil || !inFile[f.Decl] || f.Sig == nil || !f.Exported() || !f.WritesErrors() {
			continue
		}
		for _, t := range f.Sig.Errors.Elems() {
			if n, ok := t.(*check.Named); ok && check.IsGoError(n) {
				p.Report(f.Decl.Name.Pos(), f.Decl.Name.End(), "exported "+f.Name+" fails with go.Error, Go's open error; map it to a closed union of error types at this boundary (docs/spec/interop.md)")
				break
			}
		}
	}
}
