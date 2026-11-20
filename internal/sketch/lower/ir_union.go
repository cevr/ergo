package lower

// IR builder: unions and named scalars (IRG3). See ir.go.
//
// The builder never decides how a union is represented. The lowering has
// decided it once per program (flatUnionSet, flatunion.go), and the
// builder reads that decision through flatOf only:
//
//   - a flat union (U1) is the Go struct of a tag and slots: a variant goes
//     in through the union's of method on its zero value, (U{}).ofK(v); a
//     match switches on the tag ("switch", ir_match.go) and narrows by
//     reading the variant's slot, or the variant's zero record when it has
//     no fields (flatValue, flatNarrow);
//   - any other union is a sealed Go interface: a variant goes in by a Go
//     conversion to the interface ("toiface"), as Go assigns it, and a
//     match is a Go type switch ("tswitch") whose case binds by an
//     assertion.
//
// A named scalar (`type N int`, an alias of a basic type) is its Go
// defined type; its values and operators are its basic type's.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// flatOf is the lowering's flat form of t, or nil when the lowering keeps
// t a Go interface (or t is no union). The builder reads the decision
// here and nowhere else.
func (b *irBuilder) flatOf(t check.Type) *flatUnion {
	return b.l.flatUnionOf(t)
}

// admitNamed admits a named type that is not a record: a union with its
// variants, a named scalar or an alias. A union that holds itself is
// admitted once.
func (b *irBuilder) admitNamed(t *check.Named, depth int) {
	if b.admitted[t.Obj] {
		return
	}
	if alias := t.Alias(); alias != nil {
		b.admit(alias, depth+1)
		return
	}
	td := b.l.typeDecl(t.Obj, t.Obj.Decl.Name)
	switch td.kind {
	case kindUnion:
		u, ok := t.Underlying().(*check.Union)
		if !ok {
			irNo("union without its variants")
		}
		if b.admitted == nil { // a niche Option too (ir_golib.go, FORK-GOLIB)
			b.admitted = map[*check.TypeName]bool{}
		}
		b.admitted[t.Obj] = true
		for _, v := range u.Variants {
			b.admit(v, depth+1)
		}
		return
	case kindDefined:
		switch d := t.Defined().(type) {
		case *check.Basic:
			b.admit(d, depth+1)
			return
		case *check.Array:
			// Go's `type N [k]E` (kmeans' Point of [8]int32): its values
			// move as N, and an index reads the array's element (indexOther,
			// ir_index.go). Once per declaration, so a type that holds
			// itself through its element ends (GP-DEFINED, lane B's
			// 6cebc4c40).
			if !b.admitOnce(t.Obj) { // ir_pkg.go
				b.admit(d, depth+1)
			}
			return
		}
		if isPreludeType(t, "Seq") && irSeqRangeLowering != "off" {
			// A Seq is a function value of the uniform shape, spelled as
			// the program's Seq[T] (PORT-FORIN, ir_seq.go): its instance's
			// function type is admitted as any function value's is.
			b.admit(t.Underlying(), depth+1)
			return
		}
		irNo("type defined over a non-basic type")
	case kindAlias:
		b.admit(td.defined, depth+1)
		return
	case kindOps:
		// An operation type is its Go interface, which the emitter
		// declares (IRG6: a with's capability fields, an adapter's
		// conversion); Go's checker checks every use of its values. A
		// generic one is an instance (admitOps, ir_pkg.go).
		b.admitOps(t)
		return
	case kindMachine:
		b.admitMachine(t) // ir_machine.go
		return
	}
	irNo("named type that is not a record")
}

// scalarBasic is t's basic type when t is a basic type or a named scalar
// (a defined type over one, or an alias of one), else nil.
func (b *irBuilder) scalarBasic(t check.Type) *check.Basic {
	for i := 0; i < 8; i++ {
		switch u := t.(type) {
		case *check.Basic:
			return u
		case *check.Named:
			if u.Obj.Decl == nil || len(u.TArgs) > 0 {
				return nil
			}
			if alias := u.Alias(); alias != nil {
				t = alias
				continue
			}
			td := b.l.typeDecl(u.Obj, u.Obj.Decl.Name)
			if td.kind != kindDefined {
				return nil
			}
			t = td.defined
			continue
		}
		return nil
	}
	return nil
}

// implicit is e, the value of x, after the implicit conversion the checker
// records at x (Info.Implicit), as the emitter's convert does.
//
// Around it, the row gaps (ir_rowgap.go): what x reads is read as x's type
// holds it first, and the converted value goes into x's place last.
func (b *irBuilder) implicit(x syntax.Expr, e *irExpr) *irExpr {
	if e == nil {
		return e
	}
	e = b.rowRead(x, e)
	if conv, ok := b.l.info.Implicit[x]; ok {
		e = b.convert(conv, e, x)
	}
	return b.rowWrite(x, e)
}

// convert is e after the conversion conv, made at x (the emitter's
// convert): x's implicit one, or an element's (Info.ImplicitElems).
func (b *irBuilder) convert(conv check.Conversion, e *irExpr, x syntax.Expr) *irExpr {
	switch conv.Kind {
	case 0:
		return e
	case check.ConvVariant:
		return b.variantInto(conv.From, conv.To, e, x)
	case check.ConvMachine:
		return b.machineValue(conv, e, x) // ir_machine.go
	case check.ConvRow:
		// The row value (a Go error) holds one of the union's error
		// variants, which implement its sealed interface (the emitter's
		// convert, D-C3). A flat union (MSB3) is no interface: the emitter
		// builds its variant by a type switch, which the IR does not write
		// yet (I22: FLIP's IR x MSB3).
		if b.l.flatUnionOf(conv.To) != nil {
			irNo("row into a flat union")
		}
		return &irExpr{K: "assert", Pos: e.Pos, T: b.goType(conv.To, x), X: e}
	case check.ConvOperations:
		return b.opsConv(conv, e, x) // ir_with.go
	case check.ConvNarrow:
		return b.narrowValue(conv, e, x) // ir_rowgap.go
	case check.ConvWiden:
		// A sub-union's value into a union that holds its variants (the
		// emitter's widen): an assertion between two sealed interfaces.
		if b.l.flatUnionOf(conv.From) != nil || b.l.flatUnionOf(conv.To) != nil {
			irNo("sub-union into a flat union")
		}
		if b.goType(conv.From, x) == b.goType(conv.To, x) {
			return e
		}
		return &irExpr{K: "assert", Pos: e.Pos, T: b.goType(conv.To, x), X: e}
	}
	irNo("implicit conversion")
	return nil
}

// assignedTo is e, the value of x, put into a place of the type to (the
// emitter's assignedTo): after x's implicit conversion, which value and
// expr apply, a variant that joins a match's arms goes into its union.
func (b *irBuilder) assignedTo(x syntax.Expr, e *irExpr, to check.Type) *irExpr {
	if _, ok := b.l.info.Implicit[x]; ok || to == nil || e == nil {
		return e
	}
	from := b.l.info.Types[x]
	if from == nil {
		return e
	}
	return b.variantInto(from, to, e, x)
}

// variantInto is e, a value of from, put into the union to (flatValue, or
// Go's conversion to a sealed interface); e itself when from is to.
func (b *irBuilder) variantInto(from, to check.Type, e *irExpr, site syntax.Node) *irExpr {
	gt, gf := b.goType(to, site), b.goType(from, site)
	if gt == gf {
		return e
	}
	if check.IsGoAny(to) {
		// A value into go.Any is Go's own conversion to its empty
		// interface (GB2), which the IR spells (IR-GOCALLS).
		return &irExpr{K: "toiface", Pos: e.Pos, T: "any", X: e}
	}
	if fu := b.flatOf(to); fu != nil {
		if fu.niche && b.goType(fu.inst, site) == gt && fu.tagOf(from) >= 0 {
			return b.nicheValue(fu, gt, fu.tagOf(from), e) // ir_golib.go
		}
		if fu.niche || b.goType(fu.own(), site) != gt { // own: ir_pkg.go
			irNo("value of a type defined over a flat union")
		}
		k := fu.tagOf(from)
		if k < 0 {
			irNo("value of another type")
		}
		return b.variantForm(e, gt, k, layoutFlat, ofName(k)) // ir_forms.go
	}
	if n, ok := to.(*check.Named); !ok || b.l.typeDecl(n.Obj, site).kind != kindUnion {
		irNo("value of another type")
	}
	return b.variantForm(e, gt, unionTag(to, from), layoutIface, "")
}

// bareRecord is the value of a record with no fields written bare, `A`,
// of type t: its zero value (the emitter's A{}); nil for any other type
// name.
func (b *irBuilder) bareRecord(tn *check.TypeName, t check.Type, site syntax.Node, p irPos) *irExpr {
	if tn.Decl == nil {
		return nil
	}
	td := b.l.typeDecl(tn, site)
	if td.kind != kindRecord || len(td.fields) != 0 {
		return nil
	}
	return &irExpr{K: "zero", Pos: p, T: b.goType(t, site)}
}

// narrow is variant k of the flat union fu, read from the local s: its
// slot, or the variant's zero record when it has no fields (flatNarrow).
func (b *irBuilder) narrow(fu *flatUnion, s int, k int, p irPos, site syntax.Node) *irExpr {
	if fu.niche {
		return b.nicheNarrow(fu, s, k, p, site) // ir_golib.go
	}
	if !fu.slot[k] {
		return &irExpr{K: "zero", Pos: p, T: b.goType(fu.variants[k], site)}
	}
	if fu.shared {
		// U1's shared slots: the as method rebuilds the variant from its
		// slots (sharedDecl), as flatNarrow reads it.
		if len(fu.variants[k].Underlying().(*check.Struct).Fields) == 0 {
			return &irExpr{K: "zero", Pos: p, T: b.goType(fu.variants[k], site)}
		}
		return &irExpr{K: "mcall", Pos: p, Name: asName(k), X: &irExpr{K: "local", Pos: p, I: s}}
	}
	return &irExpr{K: "field", Pos: p, Name: slotName(k), X: &irExpr{K: "local", Pos: p, I: s}}
}
