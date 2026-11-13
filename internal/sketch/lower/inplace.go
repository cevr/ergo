package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// writtenInPlace reports an owned local whose value the code writes in
// place: the receiver of a *T function called on it, the operand of &,
// or the root of an assigned field or element. Its entry releases the
// local, not the value it was registered with (v1.OwningAt): a declared
// root written in place (b.Start() on a board, EXH1; resources R17) is
// released with its writes. The static form keeps a copy in its slot, so
// a frame with such a local takes the list form (staticPlan). The scan
// covers the sourced files, once per lowering; a std body writes no
// owned local in place.
func (l *lowerer) writtenInPlace(v *check.Var) bool {
	if l.inPlace == nil {
		l.inPlace = map[*check.Var]bool{}
		for _, s := range l.srcs {
			info := s.pkg.Info
			mark := func(x syntax.Expr) {
				if v := placeRoot(info, x); v != nil {
					l.inPlace[v] = true
				}
			}
			syntax.Inspect(s.file, func(n syntax.Node) bool {
				switch n := n.(type) {
				case *syntax.UnaryExpr:
					if n.Op.String() == "&" {
						mark(n.X)
					}
				case *syntax.AssignStmt:
					for _, lhs := range n.Lhs {
						if _, whole := unparen(lhs).(*syntax.Ident); !whole {
							mark(lhs)
						}
					}
				case *syntax.CallExpr:
					if c := info.Calls[n]; c != nil && c.Recv != nil && c.Func != nil && c.Func.Ptr {
						if _, ptr := info.Types[c.Recv].(*check.Pointer); !ptr {
							mark(c.Recv)
						}
					}
				}
				return true
			})
		}
	}
	return l.inPlace[v]
}

// placeRoot is the local a place expression is rooted at: x, or the value
// of a field or element chain from it; nil for any other expression.
func placeRoot(info *check.Info, x syntax.Expr) *check.Var {
	for {
		switch e := unparen(x).(type) {
		case *syntax.Ident:
			v, _ := info.Uses[e].(*check.Var)
			return v
		case *syntax.SelectorExpr:
			x = e.X
		case *syntax.IndexExpr:
			x = e.X
		default:
			return nil
		}
	}
}

// ownAt points the entry h of the owned local v, named target in Go, at
// the local (v1.OwningAt), when the code writes v in place.
func (fc *funcCtx) ownAt(v *check.Var, d *check.Drop, h, target string, site syntax.Node) {
	if !fc.l.writtenInPlace(v) {
		return
	}
	name := "OwningAt"
	if d.Glue() {
		name = "OwningPartsAt"
	}
	fc.line(fc.runtimeCall(name, fc.ownBinding(name, site), "", "", []string{h, "&" + target, fc.ownRelease(d, v.Type, site)}, "", nil, nil, site))
}
