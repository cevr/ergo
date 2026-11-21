package lower

// IR builder: the lets the "let pattern" gate refused that are no tuple
// (FLIP F7; F4b's letTuple takes `let a, b = x` and `let (a, b) = x`).
// The census at flip-f1 found the let with no value, `let mut x T`, as the
// one shape of the family besides tuples: x starts at T's default
// (types.md, Defaults), as the emitter's let writes it (body.go).

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// letDefault is `let x T` (no value) in the declaration d: x defined from
// T's default. A Map's default is an empty Map (defaultOf); one the
// emitter builds by a function or a loop (an array or tuple that holds a
// Map: typeCtx.defaultValue) keeps the emitter's body; the defaults of
// scalars, arrays and slices are Go's zero value: a constant, the literal
// T{}, nil (a record has no default).
func (b *irBuilder) letDefault(d *syntax.VarDecl, spec *syntax.VarSpec) []*irStmt {
	id := spec.Names[0]
	if id.Name == "_" {
		return nil
	}
	v, ok := b.l.info.Defs[id].(*check.Var)
	if !ok {
		irNo("let without a variable")
	}
	if b.fc.scalarOf(v) != nil {
		b.reserve(v)
		irNo("scalar-form local")
	}
	p := b.pos(id, id.Pos())
	x := b.zeroOf(v.Type, id, p)
	i := -1
	b.part(func() { i = b.local(v, id) })
	if i < 0 {
		b.reserve(v)
	}
	return []*irStmt{{K: "def", Pos: b.pos(d, d.Pos()), Local: i, X: x}}
}

// defaultOf is t's default as the emitter writes it (types.go
// defaultValue; Defaults): Go's zero value where t holds no Map, else an
// empty Map (the int map's empty table), or a record whose fields each
// take theirs. A tuple, an array or a defined Map type that holds a Map
// keeps the emitter's default.
func (b *irBuilder) defaultOf(t check.Type, site syntax.Node, p irPos) *irExpr {
	if !b.l.holdsMap(t) {
		return &irExpr{K: "zero", Pos: p, T: b.goType(t, site)}
	}
	switch u := t.(type) {
	case *check.Map:
		if b.l.intMap(u) { // ir_intmap.go: an empty table
			return b.intMapLit(u, site, p)
		}
		return &irExpr{K: "lit", Pos: p, T: b.goType(t, site)}
	case *check.Named:
		s, ok := u.Underlying().(*check.Struct)
		if !ok {
			break
		}
		td := b.l.typeDecl(u.Obj, site)
		if td.goError || td.kind == kindMachine {
			break
		}
		lit := &irExpr{K: "lit", Pos: p, T: b.goType(t, site)}
		for _, f := range s.Fields {
			goName, ok := td.fieldName[f.Name]
			if !ok {
				break
			}
			lit.Keys = append(lit.Keys, goName)
			lit.Elems = append(lit.Elems, b.defaultOf(f.Type, site, p))
		}
		if len(lit.Keys) == len(s.Fields) {
			return lit
		}
	}
	irNo("let of a default that holds a Map")
	return nil
}

// defaultable reports whether defaultOf states t's default: it holds no
// Map, or it is a Map, or a record (not a Go error or a machine) whose
// fields each are.
func (b *irBuilder) defaultable(t check.Type, site syntax.Node) bool {
	if !b.l.holdsMap(t) {
		return true
	}
	switch u := t.(type) {
	case *check.Map:
		return true
	case *check.Named:
		s, ok := u.Underlying().(*check.Struct)
		if !ok {
			return false
		}
		td := b.l.typeDecl(u.Obj, site)
		if td.goError || td.kind == kindMachine {
			return false
		}
		for _, f := range s.Fields {
			if _, ok := td.fieldName[f.Name]; !ok || !b.defaultable(f.Type, site) {
				return false
			}
		}
		return true
	}
	return false
}

// zeroOf is the default of t when it is Go's zero value.
func (b *irBuilder) zeroOf(t check.Type, site syntax.Expr, p irPos) *irExpr {
	if b.l.holdsMap(t) {
		return b.defaultOf(t, site, p)
	}
	if bt := b.scalarBasic(t); bt != nil { // ir_union.go: a named scalar's too
		switch {
		case bt.Kind == check.Bool:
			return b.constant(t, constant.MakeBool(false), site, p)
		case bt.Kind == check.String:
			return b.constant(t, constant.MakeString(""), site, p)
		default:
			return b.constant(t, constant.MakeInt64(0), site, p)
		}
	}
	switch t.(type) {
	case *check.Slice, *check.Array:
	default:
		irNo("let of a default of %s", t)
	}
	return &irExpr{K: "zero", Pos: p, T: b.goType(t, site)}
}
