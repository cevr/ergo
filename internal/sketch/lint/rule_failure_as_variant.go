package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// failureAsVariant is the strict rule for a failure returned as a value
// (anti-slop error-as-value; catalog §1d): a function whose fails row is
// empty and whose result is a union holding a type that some function of
// the program fails with. The type is a failure where it is failed and a
// value here, so a caller can ignore it: no row tells it to catch. The
// function must build the failure itself (builds); files of tests and
// laws are out (their helpers build expected messages).
var failureAsVariant = &Rule{
	Name:    "failure-as-variant",
	Code:    "L137",
	Doc:     "A function with no fails row whose result union holds a type other functions fail with returns a failure as a value; the caller may ignore it.",
	Instead: "fail with it: move the type from the result union into the function's fails row",
	Default: Warning,
	Check:   checkFailureAsVariant,
}

func checkFailureAsVariant(p *Pass) {
	if testFile(p.File) {
		return
	}
	failures := p.Memo(func() any { return programFailures(p) }).(map[*check.TypeName]bool)
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil || f.Extern != nil || f.Derived != nil {
			continue
		}
		fn, ok := p.Info.Defs[f.Name].(*check.Func)
		if !ok || fn.Sig == nil || len(fn.Sig.Results) != 1 || fn.Sig.Errors != nil && len(fn.Sig.Errors.Elems()) > 0 {
			continue
		}
		for _, v := range unionVariants(fn.Sig.Results[0]) {
			if n, ok := v.(*check.Named); ok && failures[origin(n).Obj] && builds(p.Info, f.Body, origin(n).Obj) {
				p.Report(f.Name.Pos(), f.Name.End(), f.Name.Name+" returns "+n.String()+", which other functions fail with, as a value")
				break
			}
		}
	}
}

// builds reports whether the body, outside function literals, writes a
// composite literal of the type tn declares: the function makes the
// failure itself, rather than passing on one a machine or a protocol
// carries as a message (the 2026-11-18 read sample: signals and answers
// that hold a failure variant were 7 of 9 findings).
func builds(info *check.Info, body *syntax.BlockStmt, tn *check.TypeName) bool {
	found := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.CompositeLit:
			if t, ok := info.Types[x].(*check.Named); ok && origin(t).Obj == tn {
				found = true
			}
		}
		return !found
	})
	return found
}

// unionVariants is the variants of t when it is a union, written or
// declared, and not an error row's value.
func unionVariants(t check.Type) []check.Type {
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	u, ok := t.(*check.Union)
	if !ok || u.Row != nil {
		return nil
	}
	return u.Variants
}

// programFailures is the declarations of the named types some function of
// the program fails with.
func programFailures(p *Pass) map[*check.TypeName]bool {
	out := map[*check.TypeName]bool{}
	for _, u := range p.Program() {
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			for _, d := range f.Syntax.Decls {
				fd, ok := d.(*syntax.FuncDecl)
				if !ok {
					continue
				}
				fn, ok := u.Package.Info.Defs[fd.Name].(*check.Func)
				if !ok || fn.Sig == nil || fn.Sig.Errors == nil {
					continue
				}
				for _, e := range fn.Sig.Errors.Elems() {
					if n, ok := e.(*check.Named); ok {
						out[origin(n).Obj] = true
					}
				}
			}
		}
	}
	return out
}
