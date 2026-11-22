package lower

// IR builder: a record update, T{..b, F: v} (FLIP F7, "record update or
// elided literal type"). The emitter copies the base and stores the named
// fields (expr.go, update):
//
//	u := b
//	u.F = v
//
// The IR writes the same value as one literal, T{F: v, G: b.G}: the named
// fields in source order, then each field the update keeps, read from the
// base. That needs the base read more than once: a local or a package
// let is read in place, which no field value can change (a field value
// assigns nothing); any other base is computed once, before the field
// values, into a temporary (PORT-UPDATE), as the emitter's copy runs
// first.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// updateLit is the literal x of type t whose base or type the record
// literal does not take: a record update.
func (b *irBuilder) updateLit(x *syntax.CompositeLit, t check.Type, p irPos) *irExpr {
	if x.Type == nil {
		irNo("elided literal type")
	}
	if _, ok := b.l.info.Spreads[x]; ok {
		irNo("spread")
	}
	n, ok := t.(*check.Named)
	if !ok {
		irNo("record update of %s", t)
	}
	// The emitter's update also hands the base over when it is a part
	// (handBase) and owns the fields it replaces (ownReplaced): those keep
	// the emitter's body. A replaced field of a type parameter's type has
	// no owner entry (B2: ownReplaced skips it), so nothing is released and
	// the update is a plain literal (FORK-RELUPD; domain.Sourced's Ledger).
	if b.l.info.PartHands[x.Base] {
		irNo("record update that hands its base over")
	}
	if ownsReplaced(b.l.info, x) { // release.go
		irNo("record update that releases the fields it replaces")
	}
	gt := b.goType(t, x)
	td := b.l.typeDecl(n.Obj, x) // queued by goType
	if td.kind != kindRecord || td.goError {
		irNo("record update of %s", t)
	}
	base := b.expr(x.Base)
	if base.K != "local" && base.K != "global" {
		// A computed base (a call, a field: T{..fresh(n), F: v}) runs
		// once, first, into a temporary the literal reads, as the
		// emitter's u := b does before it stores the named fields
		// (PORT-UPDATE).
		u := b.temp("base", gt, p)
		b.runFirst([]*irStmt{{K: "def", Pos: p, Local: u, X: base}}) // ir_order.go
		base = &irExpr{K: "local", Pos: p, I: u}
	}
	lit := &irExpr{K: "lit", Pos: p, T: gt}
	set := map[string]bool{}
	for _, e := range x.Elts {
		kv, ok := e.(*syntax.KeyValueExpr)
		if !ok {
			irNo("record update element without a key")
		}
		id, ok := kv.Key.(*syntax.Ident)
		if !ok {
			irNo("record key")
		}
		lit.Keys = append(lit.Keys, td.fieldName[id.Name])
		lit.Elems = append(lit.Elems, b.expr(kv.Value))
		set[id.Name] = true
	}
	for _, f := range td.fields {
		if set[f.Name] {
			continue
		}
		name := td.fieldName[f.Name]
		lit.Keys = append(lit.Keys, name)
		lit.Elems = append(lit.Elems, &irExpr{K: "field", Pos: p, Name: name, X: base})
	}
	return lit
}
