package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// deprecated is the justifier rule for a use of a deprecated declaration
// (plan §3; tsgo outdatedApi, unstableApiUsage, experimentalApiUsage;
// staticcheck SA1019). The fact is the declaration's doc comment paragraph
// "Deprecated:" (syntax.CommentGroup.Deprecated), read through the object
// the use resolves to. A use inside the declaring package is its
// maintainer's and is not reported.
var deprecated = &Rule{
	Name:    "deprecated",
	Code:    "L104",
	Doc:     "A use of a function, type, context or operation whose doc comment has a Deprecated: paragraph; use what the paragraph names.",
	Default: Warning,
	Check:   checkDeprecated,
}

func checkDeprecated(p *Pass) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	report := func(at syntax.Node, doc *syntax.CommentGroup, owner *check.Package) {
		if owner == p.Package {
			return
		}
		if paragraph, ok := doc.Deprecated(); ok {
			p.Report(at.Pos(), at.End(), "this declaration is deprecated: "+paragraph)
		}
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Ident:
			if o := p.Info.Uses[n]; o != nil {
				doc, owner := check.DocOf(o)
				report(n, doc, owner)
			}
		case *syntax.SelectorExpr:
			if o := check.QualifiedMember(p.Info, n); o != nil {
				doc, owner := check.DocOf(o)
				report(n.Sel, doc, owner)
			}
		case *syntax.CallExpr:
			doc, owner := check.OpDoc(p.Info, n)
			if sel, ok := n.Fun.(*syntax.SelectorExpr); ok {
				report(sel.Sel, doc, owner)
			}
		}
		return true
	})
}
