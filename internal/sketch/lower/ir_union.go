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
		if fu := b.flatOf(t); fu != nil && fu.niche {
			irNo("Option's pointer niche")
		}
		if b.admitted == nil {
			b.admitted = map[*check.TypeName]bool{}
		}
		b.admitted[t.Obj] = true
		for _, v := range u.Variants {
			b.admit(v, depth+1)
		}
		return
	case kindDefined:
		if _, basic := td.defined.(*check.Basic); basic {
			b.admit(td.defined, depth+1)
			return
		}
		irNo("type defined over a non-basic type")
	case kindAlias:
		b.admit(td.defined, depth+1)
		return
	case kindOps:
		irNo("operation-type value")
	case kindMachine:
		irNo("machine value")
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
func (b *irBuilder) implicit(x syntax.Expr, e *irExpr) *irExpr {
	conv, ok := b.l.info.Implicit[x]
	if !ok || e == nil {
		return e
	}
	switch conv.Kind {
	case 0:
		return e
	case check.ConvVariant:
		return b.variantInto(conv.From, conv.To, e, x)
	case check.ConvMachine:
		irNo("machine conversion")
	case check.ConvRow:
		irNo("row conversion")
	case check.ConvOperations:
		irNo("operations conversion")
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
	if fu := b.flatOf(to); fu != nil {
		if fu.niche || b.goType(fu.own(), site) != gt { // own: ir_pkg.go
			irNo("value of a type defined over a flat union")
		}
		k := fu.tagOf(from)
		if k < 0 {
			irNo("value of another type")
		}
		return &irExpr{K: "mcall", Pos: e.Pos, Name: ofName(k), X: &irExpr{K: "zero", Pos: e.Pos, T: gt}, Args: []*irExpr{e}}
	}
	if n, ok := to.(*check.Named); !ok || b.l.typeDecl(n.Obj, site).kind != kindUnion {
		irNo("value of another type")
	}
	return &irExpr{K: "toiface", Pos: e.Pos, T: gt, X: e}
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
	if !fu.slot[k] {
		return &irExpr{K: "zero", Pos: p, T: b.goType(fu.variants[k], site)}
	}
	return &irExpr{K: "field", Pos: p, Name: slotName(k), X: &irExpr{K: "local", Pos: p, I: s}}
}
