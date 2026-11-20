package lower

// IR builder: expressions, literals and constants. See ir.go.

import (
	"go/constant"
	"strconv"
	"strings"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irStringConst is the string constant s of the Go type t as the IR file
// holds it: JSON text, which carries only UTF-8 (encoding/json writes
// U+FFFD for any other byte), so a constant that is not (CBOR's bytes) is
// its Go quoted form, op "quoted", which the reader unquotes
// (ergoir_expr.go).
func irStringConst(p irPos, t, s string) *irExpr {
	if utf8.ValidString(s) {
		return &irExpr{K: "const", Pos: p, T: t, V: s}
	}
	return &irExpr{K: "const", Pos: p, T: t, V: strconv.Quote(s), Op: "quoted"}
}

// fieldType is the type of record s's field name, or nil.
func fieldType(s *check.Struct, name string) check.Type {
	for _, f := range s.Fields {
		if f.Name == name {
			return f.Type
		}
	}
	return nil
}

// intoRow is e, the value of x, as a value of want: a variant that goes
// into a row type (a Go error, as a generic record's row field holds it)
// takes an explicit conversion, as Go assigns it implicitly. The checker
// records no conversion there (the emitter leaves it to Go).
func (b *irBuilder) intoRow(want check.Type, x syntax.Expr, e *irExpr) *irExpr {
	if _, ok := want.(*check.Union); !ok || e == nil {
		return e
	}
	gt := b.goType(want, x)
	if gt != "error" || b.goType(b.operandType(x), x) == gt {
		return e
	}
	return &irExpr{K: "toiface", Pos: e.Pos, T: gt, X: e}
}

// holdsRowParam reports a parameter type that is an error row parameter
// that is not a Slot, or a row that holds one: a Go error in the
// declaration (types.md, Generics 1), where a Slot is a Go type parameter
// (Generics 6). The row gaps start from it (rowgap.go).
func holdsRowParam(t check.Type) bool {
	switch t := t.(type) {
	case *check.TypeParam:
		return t.Kind == check.KindErrors && !t.Slot
	case *check.Union:
		for _, v := range t.Variants {
			if holdsRowParam(v) {
				return true
			}
		}
	}
	return false
}

// value is x as an IR expression, after the statements that must run
// before it (polls, an if expression's branches). An if or a match inside an
// operand runs first too, after the operands to its left (exprOf, ir_order.go).
func (b *irBuilder) value(x syntax.Expr) ([]*irStmt, *irExpr) {
	if i, ok := b.preset[x]; ok {
		return nil, &irExpr{K: "local", Pos: b.pos(x, x.Pos()), I: i} // evaluated once already (ir_uigroup.go)
	}
	// The statements its operands run first come first (ir_order.go).
	return b.collect(func() ([]*irStmt, *irExpr) { return b.valueOf(x) })
}

func (b *irBuilder) valueOf(x syntax.Expr) ([]*irStmt, *irExpr) {
	var pre []*irStmt
	var e *irExpr
	switch y := unparen(x).(type) {
	case *syntax.CallExpr:
		if w, ok := b.isWith(y); ok {
			pre, e = b.withValue(w) // ir_with.go
			break
		}
		pre, e = b.call(y, false)
		// A whole value may run statements before it, so a loop-invariant
		// pure call computes its value once there (ir_memo.go).
		if mpre, me, ok := b.memoCall(y, pre, e); ok {
			pre, e = mpre, me
		}
	case *syntax.IfStmt:
		b.plain(y)
		pre, e = b.ifValue(y)
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		b.plain(y)
		pre, e = b.matchValue(y) // ir_match.go
	case *syntax.TryExpr, *syntax.CatchExpr:
		pre, e = b.failValue(y) // ir_fail.go
	case *syntax.TupleExpr:
		b.plain(y)
		pre, e = b.tupleValue(y) // ir_tuple.go
	case *syntax.Ident:
		sc := b.scalarIdent(y)
		if sc == nil {
			return nil, b.expr(x)
		}
		pre, e = b.gather(sc, y, b.pos(y, y.Pos())) // ir_scalar.go
	case *syntax.SelectorExpr:
		// A Go package variable whose Option goResult builds: Go's
		// pointer read once, then the Option (ir_govar.go).
		if t := b.l.info.Types[y]; hostRecv(b.l.info.Types[y.X]) && (isGoRef(t) || b.optionResult(t)) {
			// A Go struct's field checked for nil or made an Option.
			b.plain(y)
			pre, e, _ = b.hostField(y, b.pos(y, y.Pos()))
			break
		}
		o, _ := check.QualifiedMember(b.l.info, y).(*check.Var)
		path := hostVarPath(b.l.info, y)
		if path == "" || !b.optionResult(o.Type) {
			return nil, b.expr(x)
		}
		b.plain(y)
		pre, e = b.goVar(o, path, y, b.pos(y, y.Pos()))
	default:
		return nil, b.expr(x) // expr converts
	}
	e = b.valueCopy(unparen(x), e) // ir_copy.go
	if _, ok := b.l.info.Implicit[x]; !ok {
		x = unparen(x)
	}
	return pre, b.implicit(x, e) // ir_union.go
}

// plain stops x if the emitter does more than compute it: an owned
// temporary or a context value. The value rule's copy is built
// (valueCopy, ir_copy.go); an implicit conversion is the caller's
// (implicit, ir_union.go).
func (b *irBuilder) plain(x syntax.Expr) {
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
	if b.scalarBasic(t) == nil && !orderedParam(op, t) { // ir_union.go: a named scalar's too
		irNo("operator %s on a non-basic type", op)
	}
	b.goType(t, site)
}

// orderedParam reports an ordering operator on a type parameter whose
// constraint is ordered (GP-ORDER): its Go constraint is the generated
// type-set interface (constraint, decls.go), whose every type is ordered,
// so Go's < compiles it per GC shape, as the text path writes it. Equality
// goes through equalOn (ir_equal.go); arithmetic on number is left out
// until a body needs it.
func orderedParam(op string, t check.Type) bool {
	switch op {
	case "<", "<=", ">", ">=":
	default:
		return false
	}
	tp, ok := t.(*check.TypeParam)
	if !ok || tp.Slot || tp.Constraint == nil {
		return false
	}
	it := constraintOf(tp)
	return it != nil && it.Set == check.SetOrdered
}

// expr is x after its implicit conversion (implicit, ir_union.go).
func (b *irBuilder) expr(x syntax.Expr) *irExpr {
	if i, ok := b.preset[x]; ok {
		return &irExpr{K: "local", Pos: b.pos(x, x.Pos()), I: i} // evaluated once already (ir_uigroup.go)
	}
	return b.implicit(x, b.valueCopy(x, b.foldConst(x, b.exprOf(x)))) // ir_constfold.go // ir_copy.go
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
			if i, held := b.held[o]; held {
				return &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: i}} // ir_ownlit.go
			}
			if e, bound := b.lawArgs[o]; bound {
				// A law's parameter, bound to a site's operand while a
				// side of the law is lowered (ir_rewrite.go).
				c := *e
				return &c
			}
			if i, known := b.locals[o]; known {
				return b.handoff(x, &irExpr{K: "local", Pos: p, I: i}) // ir_own.go
			}
			if i, built := b.strBufs[o]; built {
				return b.strCall("StrGet", i, p) // strbuf.go
			}
			if b.scalars[o] != nil {
				// Its gather writes statements: a value (ir_scalar.go).
				irNo("scalar-form local inside an expression")
			}
			if val, ok := b.l.constVals[o]; ok && b.l.consts[o] {
				// A package let lowered as a Go constant (constLet): its
				// value, an IR constant, which Go does not fold either
				// (the emitter's v1.Let read).
				return b.constant(o.Type, val, x, p)
			}
			if name, ok := b.l.vars[o]; ok {
				// A package let: its Go variable.
				b.goType(o.Type, x)
				b.l.reachStdLet(o)
				return &irExpr{K: "global", Pos: p, Name: name}
			}
		case *check.Const:
			return &irExpr{K: "const", Pos: p, T: "bool", V: strconv.FormatBool(o.Val)}
		case *check.Func:
			return b.funcName(o, t, x, p, nil) // ir_fnname.go
		case *check.TypeName:
			if e := b.bareRecord(o, t, x, p); e != nil { // ir_union.go
				return e
			}
		}
		irNo("name that is not a local")
	case *syntax.BinaryExpr:
		if e := b.binaryOther(x, p); e != nil { // ir_equal.go
			return e
		}
		op := x.Op.String()
		b.operator(op, b.l.info.Types[x.X], x)
		return b.binaryOf(x, op) // ir_order.go: operands in order
	case *syntax.UnaryExpr:
		op := x.Op.String()
		switch op {
		case "!", "-", "+", "^":
		case "&":
			if b.l.info.Relends[x] {
				// &x of a mut parameter passes its pointer on (Info.Relends).
				return b.expr(x.X)
			}
			b.goType(t, x)
			return &irExpr{K: "addr", Pos: p, X: b.place(x.X)}
		default:
			irNo("unary %s", op)
		}
		b.goType(t, x)
		return &irExpr{K: "un", Pos: p, Op: op, X: b.expr(x.X)}
	case *syntax.StarExpr:
		// *p: the value p points at (FLIP F2).
		b.goType(t, x)
		return &irExpr{K: "deref", Pos: p, X: b.expr(x.X)}
	case *syntax.SelectorExpr:
		switch o := check.QualifiedMember(b.l.info, x).(type) {
		case *check.Func:
			return b.funcName(o, t, x, p, nil) // pkg.F as a value (ir_fnname.go)
		case *check.TypeName:
			// pkg.T of a record with no fields, as the bare name T is.
			if e := b.bareRecord(o, t, x, p); e != nil {
				return e
			}
			irNo("selector on a non-record")
		case *check.Var:
			// pkg.v, a package let of another package, as the bare name is.
			if val, ok := b.l.constVals[o]; ok && b.l.consts[o] {
				return b.constant(o.Type, val, x, p)
			}
			if name, ok := b.l.vars[o]; ok {
				b.goType(o.Type, x)
				b.l.reachStdLet(o)
				return &irExpr{K: "global", Pos: p, Name: name}
			}
			if path := hostVarPath(b.l.info, x); path != "" {
				pre, v := b.goVar(o, path, x, p) // ir_govar.go
				if len(pre) > 0 {
					irNo("Go package variable of an Option where no statement runs") // valueOf binds it
				}
				return v
			}
			irNo("selector on a non-record")
		}
		// A Go struct's field (ir_govar.go); one whose read runs a
		// statement is valueOf's.
		if pre, v, ok := b.hostField(x, p); ok {
			if len(pre) > 0 {
				irNo("Go field read that runs a statement where none runs")
			}
			return v
		}
		// A field through a pointer (FLIP F2): Go's p.f.
		n, ok := derefNamed(b.l.info.Types[x.X]) // ir_pkg.go
		if !ok {
			return b.selectOther(x, p) // ir_select.go
		}
		b.goType(n, x)
		// goType queued the declaration, unless the runtime's Go type
		// stands for n (IRG4: a std record such as os.Exit), as fieldName.
		if check.ScriptOf(n) != nil {
			// A Script's field is its operation's, as the emitter's
			// fieldName (expr.go) names it: the Go struct script.go
			// writes has one field per operation (testing.Script[Runner]'s
			// s.Run).
			return &irExpr{K: "field", Pos: b.pos(x.Sel, x.Sel.Pos()), Name: memberName(x.Sel.Name), X: b.expr(x.X)}
		}
		td := b.l.typeDecl(n.Obj, x)
		if td.goError {
			return b.goErrorText(x, td, p) // ir_golib.go
		}
		field, ok := td.fieldName[x.Sel.Name]
		if !ok {
			irNo("selector that is not a field")
		}
		if m, ok := strings.CutSuffix(field, "()"); ok {
			// A field the runtime's Go type reads by a method (go.Error's
			// Text, decls.go): the method's call.
			return &irExpr{K: "mcall", Pos: b.pos(x.Sel, x.Sel.Pos()), Name: m, X: b.expr(x.X)}
		}
		if h := b.hoistField(x, field); h != nil { // ir_hoist.go
			return h
		}
		return &irExpr{K: "field", Pos: b.pos(x.Sel, x.Sel.Pos()), Name: field, X: b.expr(x.X)}
	case *syntax.IndexExpr:
		// A generic function named at its type arguments, f[A, B], is a
		// value of the instance (the emitter's index, funcValue).
		if sig, ok := b.l.info.Types[x.X].(*check.Signature); ok && len(sig.TParams) > 0 {
			inst := b.l.info.Instances[x]
			if inst == nil {
				irNo("index form")
			}
			return b.funcName(inst.Func, b.l.info.Types[x], x, p, inst) // ir_fnname.go
		}
		if len(x.Indices) != 1 {
			irNo("index form")
		}
		if e, ok := b.uncheckedIndex(x, p); ok { // ir_bounds.go
			return e
		}
		if _, ok := b.l.info.Types[x.X].(*check.Slice); !ok {
			return b.indexOther(x, p) // ir_index.go
		}
		xs := b.inOrder([]syntax.Expr{x.X, x.Indices[0]}) // ir_order.go
		return &irExpr{K: "index", Pos: p, X: xs[0], Y: xs[1]}
	case *syntax.SliceExpr:
		return b.sliceOf(x, p) // ir_slice.go
	case *syntax.CompositeLit:
		if x.Base != nil || x.Type == nil {
			return b.updateLit(x, t, p) // ir_update.go
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
			}
			lit.Elems = b.listElems(x, tt.Elem)
			if b.l.valueFacts().seeds[x] && len(lit.Elems) == 1 {
				// append(slices.Grow([]T{}, seedCap), e) (seedcap.go).
				et := b.goType(tt.Elem, x)
				keep := lit.T
				if b.namesTypeParam(keep) {
					keep = "[]int" // a generic body's slice is no package-level type (ir_loops.go)
				}
				b.keepImport("var _ = " + b.l.goPkg("slices") + ".Grow[" + keep + "]")
				room := &irExpr{K: "pcall", Pos: p, T: "slices", Name: "Grow", V: lit.T + ", " + et, Args: []*irExpr{{K: "lit", Pos: p, T: lit.T}, {K: "const", Pos: p, T: "int", V: strconv.Itoa(seedCap)}}}
				b.seedSites++
				return &irExpr{K: "append", Pos: p, Args: []*irExpr{room, lit.Elems[0]}}
			}
		case *check.Array:
			// The checker took every element in order, or none for the
			// default (types.md, Arrays 2), as the emitter writes it:
			// [N]T{e1, …, eN}, or [N]T{}, the decoder's zero of an array.
			if len(x.Elts) == 0 {
				if b.l.holdsMap(tt) {
					irNo("literal of an array whose default holds a Map") // the emitter's default, built in a loop
				}
				return &irExpr{K: "zero", Pos: p, T: lit.T}
			}
			lit.Elems = b.listElems(x, tt.Elem)
		case *check.Map:
			if b.l.intMap(tt) { // ir_intmap.go
				if len(x.Elts) > 0 {
					irNo("int map literal with entries")
				}
				return b.intMapLit(tt, x, p)
			}
			lit.Elems = b.mapLit(x) // ir_pkg.go
		case *check.Named:
			td := b.l.typeDecl(tt.Obj, x) // queued by goType, as above
			if td.goError {
				return b.goErrorLit(x, t, p) // ir_golib.go
			}
			if td.kind == kindMachine {
				return b.machineLit(x, lit) // ir_machine.go
			}
			s, ok := tt.Underlying().(*check.Struct)
			if !ok {
				// A type defined over an array (kmeans' Point{}): with no
				// elements, its zero value, as Go's T{} (the decoder's
				// zero of an array). With elements it keeps its reason.
				if _, array := tt.Underlying().(*check.Array); array && len(x.Elts) == 0 {
					return &irExpr{K: "zero", Pos: p, T: lit.T}
				}
				irNo("literal of a defined %T", tt.Underlying())
			}
			if len(x.Elts) != len(s.Fields) {
				irNo("record literal without every field")
			}
			var values []syntax.Expr
			var fts []check.Type
			var names []string
			for i, e := range x.Elts {
				name := ""
				kv, keyed := e.(*syntax.KeyValueExpr)
				switch {
				case keyed:
					k, ok := kv.Key.(*syntax.Ident)
					if !ok {
						irNo("record key")
					}
					name = k.Name
					values = append(values, kv.Value)
				case i < len(td.fields):
					// A positional element sets the field of its place (the
					// emitter's composite).
					name = td.fields[i].Name
					values = append(values, e)
				default:
					irNo("unkeyed record literal")
				}
				var ft check.Type
				for _, f := range s.Fields {
					if f.Name == name {
						ft = f.Type
					}
				}
				fts = append(fts, ft)
				names = append(names, name)
				lit.Keys = append(lit.Keys, td.fieldName[name])
			}
			lit.Elems = b.inOrder(values) // ir_order.go
			plain := true                 // every element as written: no field conversion or slot adapter
			for i, e := range lit.Elems {
				// A variant that joins a union field goes into it as the
				// emitter's Go assignment puts it (assignedTo, ir_union.go:
				// failureCase[A | B]{Error: A{}}); any other value as the
				// field holds it (intoField, B-CONVFIELD).
				if fts[i] == nil || b.goType(fts[i], values[i]) == "error" {
					lit.Elems[i] = b.intoField(values[i], e, fts[i]) // ir_field.go: a row's error
				} else if a := b.assignedTo(values[i], e, fts[i]); a != e {
					lit.Elems[i] = a
				} else {
					lit.Elems[i] = b.intoField(values[i], e, fts[i]) // ir_field.go
				}
				lit.Elems[i] = b.intoSlot(td, t, names[i], values[i], lit.Elems[i]) // ir_field.go (OPC-ADAPT2)
				plain = plain && lit.Elems[i] == e
			}
			if plain {
				lit.FT = b.recordFieldTypes(fts, x) // ir_forms_store.go
			}
		default:
			irNo("literal of %T", t)
		}
		return lit
	case *syntax.InterpolatedString:
		return b.text(x, p)
	case *syntax.TupleExpr:
		tt, ok := t.(*check.Tuple)
		if !ok || len(tt.Types) != len(x.Elts) || len(tt.Types) < 2 {
			irNo("type *check.Tuple")
		}
		vals := b.inOrder(x.Elts)         // ir_order.go
		return b.tupleLit(tt, vals, x, p) // ir_tuple.go
	case *syntax.FuncLit:
		return b.closureLit(x, nil) // ir_closure.go
	case *syntax.CallExpr:
		pre, e := b.call(x, false)
		b.runFirst(pre) // ir_order.go: before the expression that holds it
		return e
	case *syntax.TryExpr, *syntax.CatchExpr:
		// A try or catch inside an expression: its statements run before
		// the expression, in order, as a call's do (ir_fail.go, ir_order.go).
		pre, e := b.failValue(x)
		b.runFirst(pre)
		return e
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		// An if or a match inside an expression: its local, declared and
		// yielded into by its arms, runs before the expression, in order,
		// as a call's statements do (ir_order.go); an earlier operand an
		// arm may assign is spilled first (ordered, isBranch).
		pre, e := b.valueOf(x)
		b.runFirst(pre)
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
	// Each constant keeps its value as Go reads its spelling, for the
	// fold of an operation over it (ir_constfold.go).
	case constant.Bool:
		return b.constValue(&irExpr{K: "const", Pos: p, T: gt, V: strconv.FormatBool(constant.BoolVal(v))}, v)
	case constant.String:
		return b.constValue(irStringConst(p, gt, constant.StringVal(v)), v)
	case constant.Int, constant.Float:
		switch {
		case isInteger(t) || bt.Kind == check.Duration:
			i := constant.ToInt(v)
			if i.Kind() != constant.Int {
				irNo("constant kind %s", v.Kind())
			}
			return b.constValue(&irExpr{K: "const", Pos: p, T: gt, V: i.ExactString()}, i)
		case bt.Kind == check.Float64:
			f, _ := constant.Float64Val(constant.ToFloat(v))
			return b.constValue(&irExpr{K: "const", Pos: p, T: gt, V: strconv.FormatFloat(f, 'g', -1, 64)}, constant.MakeFloat64(f))
		case bt.Kind == check.Float32:
			f, _ := constant.Float32Val(constant.ToFloat(v))
			return b.constValue(&irExpr{K: "const", Pos: p, T: gt, V: strconv.FormatFloat(float64(f), 'g', -1, 32)}, constant.MakeFloat64(float64(f)))
		}
	}
	irNo("constant kind %s", v.Kind())
	return nil
}

// listElems are the elements of a slice or array literal x, in order
// (ir_order.go), each as an element of type elem holds it: a value into
// Go's any by assignedTo (ir_union.go, IR-GOCALLS), any other as written.
func (b *irBuilder) listElems(x *syntax.CompositeLit, elem check.Type) []*irExpr {
	elems := b.inOrder(x.Elts) // ir_order.go
	if check.IsGoAny(elem) {
		for i, e := range elems {
			elems[i] = b.assignedTo(x.Elts[i], e, elem) // ir_union.go
		}
	}
	return elems
}
