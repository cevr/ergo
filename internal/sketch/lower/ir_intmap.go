package lower

// IR builder: the operations of an int map (MI-2, intmap.go), a Map whose
// key is a Go integer type, which the program's Go spells
// *v1.IntMap[K, V]. Each Map operation the IR writes for Go's map has its
// table method here:
//
//	m.Get(k) in a match   v, ok := m.Get(k)     (tdef of the method call)
//	m[k] = v, m.Put(k, v) m.Put(k, v)
//	m.Delete(k)           m.Delete(k)
//	len(m)                m.Len()
//	clear(m)              m.Clear()
//	a value copy          m.Clone()
//	Map[K, V]{}           v1.NewIntMap[K, V](0)
//
// Every other operation on an int map (a literal with entries, m[k] op=
// v, a for loop over it, Take, Get as a value) stops the body (irNo). Its
// emitter body then writes Go's map operations, so the lowering records a
// conflict (intmap.go, intMapBody) and lowers again with that Map type as
// Go's map: no operation is ever written for the wrong representation.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// intMapOf is t as an int map: a Map type, not a type defined over one,
// that the lowering spells IntMap.
func (b *irBuilder) intMapOf(t check.Type) (*check.Map, bool) {
	m, ok := b.fc.boundType(t).(*check.Map)
	return m, ok && b.l.intMap(m)
}

// tableCall is the call of the table's method name on m.
func tableCall(p irPos, name string, m *irExpr, args ...*irExpr) *irExpr {
	return &irExpr{K: "mcall", Pos: p, Name: name, X: m, Args: args}
}

// intMapValue is the statements and value of a Map operation that writes
// m in place and whose value is m: the call alone where the value is
// dropped (stmt), else m in a temporary that the call writes.
func (b *irBuilder) intMapValue(x syntax.Expr, p irPos, stmt bool, pre []*irStmt, m *irExpr, name string, args ...*irExpr) ([]*irStmt, *irExpr) {
	if stmt {
		return pre, tableCall(p, name, m, args...)
	}
	t := b.temp("t", b.goType(b.l.info.Types[x], x), p)
	local := func() *irExpr { return &irExpr{K: "local", Pos: p, I: t} }
	pre = append(pre,
		&irStmt{K: "def", Pos: p, Local: t, X: m},
		&irStmt{K: "expr", Pos: p, X: tableCall(p, name, local(), args...)})
	return pre, local()
}

// intMapPrelude is the call x of the prelude's Map operation f on an int
// map: Put (Delete binds Go's builtin, intMapBuiltin). ok is false when
// x is no such call.
func (b *irBuilder) intMapPrelude(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos, stmt bool) ([]*irStmt, *irExpr, bool) {
	op := check.CollOpOf(f)
	switch op {
	case check.CollPut, check.CollDelete, check.CollGet, check.CollTake:
	default:
		return nil, nil, false
	}
	list := x.Args
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	if len(list) == 0 {
		return nil, nil, false
	}
	if _, ok := b.intMapOf(b.l.info.Types[list[0]]); !ok {
		return nil, nil, false
	}
	switch op {
	case check.CollPut:
		if len(list) != 3 {
			irNo("Map write form")
		}
		pre, args := b.operands(list)   // ir_fail.go
		b.valueSource(x, c, list, args) // ir_pkg.go: a copy unless in place
		pre, v := b.intMapValue(x, p, stmt, pre, args[0], "Put", args[1], args[2])
		return pre, v, true
	case check.CollDelete:
		if len(list) != 2 {
			irNo("Map write form")
		}
		pre, args := b.operands(list)
		b.valueSource(x, c, list, args)
		pre, v := b.intMapValue(x, p, stmt, pre, args[0], "Delete", args[1])
		return pre, v, true
	}
	irNo("int map %s", map[check.CollOp]string{check.CollGet: "Get as a value", check.CollTake: "Take"}[op])
	return nil, nil, false
}

// intMapBuiltin is the call of Go's builtin name on an int map's
// operands, args after the value rule: ok is false when the first operand
// is no int map.
func (b *irBuilder) intMapBuiltin(x *syntax.CallExpr, name string, list []syntax.Expr, pre []*irStmt, args []*irExpr, p irPos, stmt bool) ([]*irStmt, *irExpr, bool, bool) {
	if len(list) == 0 {
		return nil, nil, false, false
	}
	t := b.l.info.Types[list[0]]
	if ptr, ok := t.(*check.Pointer); ok {
		t = ptr.Elem // a borrow operand, written through (clear's)
	}
	if _, ok := b.intMapOf(t); !ok {
		return nil, nil, false, false
	}
	switch name {
	case "delete":
		if len(args) != 2 {
			irNo("delete form")
		}
		pre, v := b.intMapValue(x, p, stmt, pre, args[0], "Delete", args[1])
		return pre, v, false, true
	case "len":
		return pre, tableCall(p, "Len", args[0]), false, true
	case "clear":
		return pre, tableCall(p, "Clear", args[0]), true, true
	}
	irNo("call of the Go builtin %s on an int map", name)
	return nil, nil, false, false
}

// intMapGet is the statement v, ok := m.Get(k) for an int map's m and k.
func intMapGet(p irPos, v, ok int, m, k *irExpr) *irStmt {
	return &irStmt{K: "tdef", Pos: p, Ls: []int{v, ok}, X: tableCall(p, "Get", m, k)}
}

// intMapStore is the statement m.Put(k, v) of the store m[k] = v (to is
// its "mapAt" target).
func intMapStore(p irPos, to, v *irExpr) *irStmt {
	return &irStmt{K: "expr", Pos: p, X: tableCall(p, "Put", to.X, to.Y, v)}
}

// intMapLit is the empty int map literal of type m.
func (b *irBuilder) intMapLit(m *check.Map, x syntax.Node, p irPos) *irExpr {
	return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "NewIntMap", V: b.goType(m.Key, x) + ", " + b.goType(m.Elem, x), Args: []*irExpr{{K: "const", Pos: p, T: "int", V: "0"}}}
}
