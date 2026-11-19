package lower

// IR builder: a selector whose operand is no record (FLIP F7, "selector on
// a non-record"). The census at flip-f1 found one shape: a name of an
// imported package, pkg.Name, which the checker types as no value.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// selectOther is x, a selector whose operand is no record. An imported
// package's let reads its Go variable, as the emitter's selector does
// (expr.go): the same "global" the package's own let reads. A Go
// package's variable and a package function used as a value keep the
// emitter's body, each with a reason of its own.
func (b *irBuilder) selectOther(x *syntax.SelectorExpr, p irPos) *irExpr {
	id, ok := x.X.(*syntax.Ident)
	if !ok {
		irNo("selector on a non-record")
	}
	pn, ok := b.l.info.Uses[id].(*check.PkgName)
	if !ok {
		irNo("selector on a non-record")
	}
	// The checker records no Use of the selected name: the package's
	// exported object of that name is the one (expr.go, selector).
	for _, o := range pn.Pkg.Exported() {
		switch o := o.(type) {
		case *check.TypeName:
			// A record with no fields written bare, pkg.A: its zero
			// value, as the package's own A (ir_union.go).
			if o.Name == x.Sel.Name {
				if e := b.bareRecord(aliased(pn.Pkg, o.Name), b.l.info.Types[x], x, p); e != nil { // an alias stands for its record (S109-ALIAS)
					return e
				}
				irNo("package type name as a value")
			}
		case *check.Func:
			if o.Name == x.Sel.Name {
				return b.funcName(o, b.l.info.Types[x], x, p) // ir_fnname.go
			}
		case *check.Var:
			if o.Name != x.Sel.Name {
				continue
			}
			if pn.Pkg.Host() {
				irNo("Go package variable")
			}
			if val, ok := b.l.constVals[o]; ok && b.l.consts[o] {
				// A package let lowered as a Go constant (constLet), as
				// for a name of the package itself (ir_expr.go).
				return b.constant(o.Type, val, x, p)
			}
			name, ok := b.l.vars[o]
			if !ok {
				irNo("package let without a Go name")
			}
			b.goType(o.Type, x)
			b.l.reachStdLet(o)
			return &irExpr{K: "global", Pos: p, Name: name}
		}
	}
	irNo("selector on a non-record")
	return nil
}
