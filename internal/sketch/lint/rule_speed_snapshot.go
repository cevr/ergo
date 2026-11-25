package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// snapshotArgument is the speed hint for `f(*s, g(s))` (catalog PF5;
// S3d2's profile: typing bytes 16.3 MB to 1.3 MB): an argument `*s`
// followed by an argument that passes the pointer s to a call. That call
// may write through s, so the lowering copies *s first to keep Ergo's
// left-to-right order; when g only reads, the copy is waste.
var snapshotArgument = &Rule{
	Name:    "snapshot-argument",
	Code:    "L144",
	Doc:     "An argument *s before an argument that passes s to a call makes the lowering copy *s, to keep the order of evaluation.",
	Instead: "bind the later argument with let before the call, then pass *s",
	Default: Hint,
	Check:   checkSnapshotArgument,
}

func checkSnapshotArgument(p *Pass) {
	for _, c := range p.fileIndex().calls {
		for i, a := range c.Args {
			star, ok := a.(*syntax.StarExpr)
			if !ok {
				continue
			}
			x, ok := star.X.(*syntax.Ident)
			if !ok {
				continue
			}
			s, ok := p.Info.Uses[x].(*check.Var)
			if !ok {
				continue
			}
			if _, isPtr := underOf(s.Type).(*check.Pointer); !isPtr {
				continue
			}
			if passesLater(p.Info, c.Args[i+1:], s) {
				p.Report(star.Pos(), star.End(), "this *"+x.Name+" is copied, because a later argument passes "+x.Name+" to a call")
			}
		}
	}
}

// passesLater reports whether one of args holds a call with s itself as
// an argument.
func passesLater(info *check.Info, args []syntax.Expr, s *check.Var) bool {
	found := false
	for _, a := range args {
		syntax.Inspect(a, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.CallExpr); ok {
				for _, b := range c.Args {
					if id, isID := b.(*syntax.Ident); isID && info.Uses[id] == s {
						found = true
					}
				}
			}
			return !found
		})
	}
	return found
}
