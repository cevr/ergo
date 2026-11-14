package lower

// IR builder: expressions, literals and constants. See ir.go.

import (
	"fmt"
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// value is x as an IR expression, after the statements (polls) that must
// run before it.
func (b *irBuilder) value(x syntax.Expr) ([]*irStmt, *irExpr) {
	if call, ok := unparen(x).(*syntax.CallExpr); ok {
		return b.call(call, false)
	}
	return nil, b.expr(x)
}

func (b *irBuilder) expr(x syntax.Expr) *irExpr {
	if _, ok := b.l.info.Implicit[x]; ok {
		irNo("implicit conversion")
	}
	p := b.pos(x, x.Pos())
	t, ok := b.l.info.Types[x]
	if !ok {
		irNo("expression without a type")
	}
	if v, ok := b.l.info.Values[x]; ok {
		gt := b.goType(t, x)
		switch v.Kind() {
		case constant.Int:
			n, exact := constant.Int64Val(v)
			if !exact {
				irNo("big integer constant")
			}
			return &irExpr{K: "const", Pos: p, T: gt, V: fmt.Sprint(n)}
		case constant.Bool:
			return &irExpr{K: "const", Pos: p, T: gt, V: fmt.Sprint(constant.BoolVal(v))}
		case constant.String:
			return &irExpr{K: "const", Pos: p, T: gt, V: constant.StringVal(v)}
		}
		irNo("constant kind %s", v.Kind())
	}
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return b.expr(x.X)
	case *syntax.Ident:
		v, ok := b.l.info.Uses[x].(*check.Var)
		i, known := b.locals[v]
		if !ok || !known {
			irNo("name that is not a local")
		}
		return &irExpr{K: "local", Pos: p, I: i}
	case *syntax.BinaryExpr:
		op := x.Op.String()
		switch op {
		case "+", "-", "*", "/", "%", "==", "!=", "<", "<=", ">", ">=", "&&", "||":
		default:
			irNo("operator %s", op)
		}
		if _, basic := b.l.info.Types[x.X].(*check.Basic); !basic {
			irNo("operator %s on a non-basic type", op)
		}
		b.goType(b.l.info.Types[x.X], x)
		return &irExpr{K: "bin", Pos: b.pos(x, x.OpPos), Op: op, X: b.expr(x.X), Y: b.expr(x.Y)}
	case *syntax.UnaryExpr:
		op := x.Op.String()
		if op != "!" && op != "-" {
			irNo("unary %s", op)
		}
		return &irExpr{K: "un", Pos: p, Op: op, X: b.expr(x.X)}
	case *syntax.SelectorExpr:
		n, ok := b.l.info.Types[x.X].(*check.Named)
		if !ok {
			irNo("selector on a non-record")
		}
		b.goType(n, x)
		td := b.l.useType(b.l.typeDecl(n.Obj, x))
		field, ok := td.fieldName[x.Sel.Name]
		if !ok {
			irNo("selector that is not a field")
		}
		return &irExpr{K: "field", Pos: b.pos(x.Sel, x.Sel.Pos()), Name: field, X: b.expr(x.X)}
	case *syntax.IndexExpr:
		if len(x.Indices) != 1 {
			irNo("index form")
		}
		if _, ok := b.l.info.Types[x.X].(*check.Slice); !ok {
			irNo("index of a non-slice")
		}
		return &irExpr{K: "index", Pos: p, X: b.expr(x.X), Y: b.expr(x.Indices[0])}
	case *syntax.CompositeLit:
		if x.Base != nil || x.Type == nil {
			irNo("record update or elided literal type")
		}
		if _, ok := b.l.info.Spreads[x]; ok {
			irNo("spread")
		}
		lit := &irExpr{K: "lit", Pos: p, T: b.goType(t, x)}
		switch tt := t.(type) {
		case *check.Slice:
			for _, e := range x.Elts {
				if _, kv := e.(*syntax.KeyValueExpr); kv {
					irNo("keyed slice literal")
				}
				lit.Elems = append(lit.Elems, b.expr(e))
			}
		case *check.Named:
			td := b.l.useType(b.l.typeDecl(tt.Obj, x))
			s := tt.Underlying().(*check.Struct)
			if len(x.Elts) != len(s.Fields) {
				irNo("record literal without every field")
			}
			for _, e := range x.Elts {
				kv, ok := e.(*syntax.KeyValueExpr)
				if !ok {
					irNo("unkeyed record literal")
				}
				k, ok := kv.Key.(*syntax.Ident)
				if !ok {
					irNo("record key")
				}
				lit.Keys = append(lit.Keys, td.fieldName[k.Name])
				lit.Elems = append(lit.Elems, b.expr(kv.Value))
			}
		default:
			irNo("literal of %T", t)
		}
		return lit
	case *syntax.CallExpr:
		pre, e := b.call(x, false)
		if len(pre) > 0 {
			irNo("operation call inside an expression")
		}
		return e
	}
	irNo("expression %T", x)
	return nil
}
