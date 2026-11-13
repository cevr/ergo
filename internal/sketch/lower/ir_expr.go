package lower

// IR builder: expressions, literals and constants. See ir.go.

import (
	"go/constant"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// value is x as an IR expression, after the statements that must run
// before it (polls, an if expression's branches). Only a whole value takes
// statements: an if expression inside an operand keeps its source body,
// since hoisting it would move it before the operands to its left.
func (b *irBuilder) value(x syntax.Expr) ([]*irStmt, *irExpr) {
	var pre []*irStmt
	var e *irExpr
	switch y := unparen(x).(type) {
	case *syntax.CallExpr:
		if w, ok := b.isWith(y); ok {
			pre, e = b.withValue(w) // ir_with.go
			break
		}
		pre, e = b.call(y, false)
	case *syntax.IfStmt:
		b.plain(y)
		pre, e = b.ifValue(y)
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		b.plain(y)
		pre, e = b.matchValue(y) // ir_match.go
	case *syntax.TryExpr, *syntax.CatchExpr:
		pre, e = b.failValue(y) // ir_fail.go
	case *syntax.Ident:
		sc := b.scalarIdent(y)
		if sc == nil {
			return nil, b.expr(x)
		}
		pre, e = b.gather(sc, y, b.pos(y, y.Pos())) // ir_scalar.go
	case *syntax.BinaryExpr:
		// An if or a match as an operand of an operator that evaluates
		// both: its statements run in operand order (the emitter's binary
		// takes its operands).
		op := y.Op.String()
		if op == "&&" || op == "||" || op == ".." || !isBranch(y.X) && !isBranch(y.Y) {
			return nil, b.expr(x)
		}
		b.plain(y)
		b.goType(b.l.info.Types[y], y)
		b.operator(op, b.l.info.Types[y.X], y)
		var vals []*irExpr
		pre, vals = b.operands([]syntax.Expr{y.X, y.Y}) // ir_fail.go
		e = &irExpr{K: "bin", Pos: b.pos(y, y.OpPos), Op: op, X: vals[0], Y: vals[1]}
	default:
		return nil, b.expr(x) // expr converts
	}
	if _, ok := b.l.info.Implicit[x]; !ok {
		x = unparen(x)
	}
	return pre, b.implicit(x, e) // ir_union.go
}

// plain stops x if the emitter does more than compute it: the value
// rule's copy, an owned temporary or a context value. An implicit
// conversion is the caller's (implicit, ir_union.go).
func (b *irBuilder) plain(x syntax.Expr) {
	if b.l.vals != nil && b.l.vals.clone[x] {
		irNo("value copy")
	}
	if b.l.info.OwnedTemps[x] != nil {
		irNo("owned temporary")
	}
	if b.l.info.ContextValues[x] != nil {
		irNo("context value")
	}
}

// irOps are the binary operators of the IR: Go's, on the basic types.
var irOps = map[string]bool{
	"+": true, "-": true, "*": true, "/": true, "%": true,
	"&": true, "|": true, "^": true, "&^": true, "<<": true, ">>": true,
	"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true,
	"&&": true, "||": true,
}

// operator admits op on operands of type t (its left operand's).
func (b *irBuilder) operator(op string, t check.Type, site syntax.Node) {
	if !irOps[op] {
		irNo("operator %s", op)
	}
	if b.scalarBasic(t) == nil { // ir_union.go: a named scalar's too
		irNo("operator %s on a non-basic type", op)
	}
	b.goType(t, site)
}

// expr is x after its implicit conversion (implicit, ir_union.go).
func (b *irBuilder) expr(x syntax.Expr) *irExpr {
	return b.implicit(x, b.exprOf(x))
}

func (b *irBuilder) exprOf(x syntax.Expr) *irExpr {
	b.plain(x)
	p := b.pos(x, x.Pos())
	t, ok := b.l.info.Types[x]
	if !ok {
		irNo("expression without a type")
	}
	if v, ok := b.l.info.Values[x]; ok {
		return b.constant(t, v, x, p)
	}
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return b.expr(x.X)
	case *syntax.Ident:
		switch o := b.l.info.Uses[x].(type) {
		case *check.Var:
			if i, known := b.locals[o]; known {
				return &irExpr{K: "local", Pos: p, I: i}
			}
			if b.scalars[o] != nil {
				// Its gather writes statements: a value (ir_scalar.go).
				irNo("scalar-form local inside an expression")
			}
			if name, ok := b.l.vars[o]; ok {
				// A package let: its Go variable.
				b.goType(o.Type, x)
				b.l.reachStdLet(o)
				return &irExpr{K: "global", Pos: p, Name: name}
			}
		case *check.Const:
			return &irExpr{K: "const", Pos: p, T: "bool", V: strconv.FormatBool(o.Val)}
		case *check.TypeName:
			if e := b.bareRecord(o, t, x, p); e != nil { // ir_union.go
				return e
			}
		}
		irNo("name that is not a local")
	case *syntax.BinaryExpr:
		op := x.Op.String()
		b.operator(op, b.l.info.Types[x.X], x)
		return &irExpr{K: "bin", Pos: b.pos(x, x.OpPos), Op: op, X: b.expr(x.X), Y: b.expr(x.Y)}
	case *syntax.UnaryExpr:
		op := x.Op.String()
		switch op {
		case "!", "-", "+", "^":
		default:
			irNo("unary %s", op)
		}
		b.goType(t, x)
		return &irExpr{K: "un", Pos: p, Op: op, X: b.expr(x.X)}
	case *syntax.SelectorExpr:
		n, ok := b.l.info.Types[x.X].(*check.Named)
		if !ok {
			irNo("selector on a non-record")
		}
		b.goType(n, x)
		// goType queued the declaration, unless the runtime's Go type
		// stands for n (IRG4: a std record such as os.Exit), as fieldName.
		td := b.l.typeDecl(n.Obj, x)
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
			td := b.l.typeDecl(tt.Obj, x) // queued by goType, as above
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
	case *syntax.InterpolatedString:
		return b.text(x, p)
	case *syntax.FuncLit:
		return b.funcLit(x, nil) // ir_closure.go
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

// constant is the checked constant v of type t, as the emitter spells it
// (typeCtx.constant): an integer type takes the exact integer, a float
// type the value rounded once from the exact one.
func (b *irBuilder) constant(t check.Type, v constant.Value, x syntax.Expr, p irPos) *irExpr {
	gt := b.goType(t, x)
	bt := b.scalarBasic(t) // ir_union.go: a named scalar's too
	if bt == nil {
		irNo("constant of type %s", t)
	}
	t = bt
	switch v.Kind() {
	case constant.Bool:
		return &irExpr{K: "const", Pos: p, T: gt, V: strconv.FormatBool(constant.BoolVal(v))}
	case constant.String:
		return &irExpr{K: "const", Pos: p, T: gt, V: constant.StringVal(v)}
	case constant.Int, constant.Float:
		switch {
		case isInteger(t) || bt.Kind == check.Duration:
			i := constant.ToInt(v)
			if i.Kind() != constant.Int {
				irNo("constant kind %s", v.Kind())
			}
			return &irExpr{K: "const", Pos: p, T: gt, V: i.ExactString()}
		case bt.Kind == check.Float64:
			f, _ := constant.Float64Val(constant.ToFloat(v))
			return &irExpr{K: "const", Pos: p, T: gt, V: strconv.FormatFloat(f, 'g', -1, 64)}
		case bt.Kind == check.Float32:
			f, _ := constant.Float32Val(constant.ToFloat(v))
			return &irExpr{K: "const", Pos: p, T: gt, V: strconv.FormatFloat(float64(f), 'g', -1, 32)}
		}
	}
	irNo("constant kind %s", v.Kind())
	return nil
}
