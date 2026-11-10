package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Spread is one field a record update or spread `T{..s, …}` copies from
// its base: T's field and the base's field of the same declaration
// (types.md, Structs 3). Info.Spreads lists them for a base of another
// type; the lowering reads them and never derives them again.
type Spread struct {
	Field *Field // T's field
	From  *Field // the base's field: Origin() is Field's
}

// recordUpdate checks a record update or spread `T{..base, F: v, …}`
// (grammar.md, Expressions 11; types.md, Structs 3, owner q43 = A). It is
// a composite literal of T, checked by compositeLit's own rules, so every
// rule about who may write a T literal applies to it unchanged:
//   - T is a record type; its type arguments are written, given by the
//     expected type, or else taken from the base;
//   - the base is a record value, typed once and first, with T as its
//     hint: a union value is not one (match it first);
//   - the named fields follow the keyed-literal rules (structLit);
//   - with a base of exactly T, every field it omits is the base's, and at
//     least one is named (the copy with none is the base);
//   - with a base of another record type S, each field of T it omits is
//     copied from S's field of the same declaration (one record type's
//     instances, or one included ..R) with the same type, and is S108
//     otherwise (spreadFields); none need be named (a projection or an
//     injection).
//
// Each refusal route types what it skips as an orphan, the base included
// (orphanElements), so typing and the reference walk agree on the names
// in it.
func (c *checker) recordUpdate(x *syntax.CompositeLit, hint Type, mark int, lt func() litType) operand {
	raw := c.typExpr0(x.Type, c.localTypeCtx())
	if isInvalid(raw) {
		c.orphanElements(x, mark, lt())
		return bad(x)
	}
	t := raw
	var base operand
	typed := false // the base is typed
	if n, ok := raw.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
		t = instanceFromHint(n, hint)
		if t == nil {
			// No written or expected type arguments: the base gives them.
			base = c.valueExpr(x.Base, nil)
			typed = true
			b, ok := base.typ.(*Named)
			if base.invalid() || !ok || b.Orig == nil || b.origin() != n {
				if !base.invalid() {
					c.errorf(x.Base, CodeType, "%s", c.updateBaseText(x.Base, base.typ, n))
				}
				c.orphanFields(x, mark, lt)
				return bad(x)
			}
			t = b
		}
	}
	s, ok := under(t).(*Struct)
	if !ok {
		c.errorf(x, CodeType, "a record update needs a record type, and %s is not one (types.md, Structs 3)", c.localTypeText(t))
		if typed {
			c.orphanFields(x, mark, lt)
		} else {
			c.orphanElements(x, mark, lt())
		}
		return bad(x)
	}
	if !typed {
		base = c.valueExpr(x.Base, t)
	}
	if !base.invalid() && c.identical(base.typ, t) {
		if len(x.Elts) == 0 {
			// One form per job: the copy with no field replaced is the base.
			c.errorf(x, CodeType, "a record update names at least one field; the copy is %s itself (types.md, Structs 3)", baseText(x.Base))
			return operand{mode: modeValue, typ: t}
		}
		c.structLit(x, t, s, mark)
		return operand{mode: modeValue, typ: t}
	}
	var from *Struct
	if !base.invalid() {
		from, _ = under(base.typ).(*Struct)
		if from == nil || asUnion(base.typ) != nil {
			c.errorf(x.Base, CodeType, "%s", c.updateBaseText(x.Base, base.typ, t))
		}
	}
	if c.structLit(x, t, s, mark) && from != nil {
		c.spreadFields(x, t, s, base.typ, from)
	}
	return operand{mode: modeValue, typ: t}
}

// spreadFields checks the fields a spread `T{..b, …}` with b of the record
// type S (not T) takes from b: each field of T that x does not name is
// copied from S's field of the same declaration and the same type. A field
// S lacks, has from another declaration, or has with another type is S108
// at the base, naming the field and the fix. The copies go to Info.Spreads.
func (c *checker) spreadFields(x *syntax.CompositeLit, t Type, s *Struct, st Type, from *Struct) {
	named := map[string]bool{}
	for _, e := range x.Elts {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			if id, ok := kv.Key.(*syntax.Ident); ok {
				named[id.Name] = true
			}
		}
	}
	what := exprText(x.Base)
	fix := func(name string) string { return "name it: " + name + ": …" }
	if what == "this expression" {
		what = "…"
	} else {
		fix = func(name string) string { return "write " + name + ": " + what + "." + name }
	}
	lit := c.localTypeText(t) + "{.." + what + "}"
	src := c.localTypeText(st)
	copies := []Spread{}
	for _, f := range s.Fields {
		if named[f.Name] || isInvalid(f.Type) {
			continue
		}
		g, _ := from.field(f.Name)
		switch {
		case g == nil:
			c.errorf(x.Base, CodeType, "%s: %s has no %s; name it: %s: … (types.md, Structs 3)", lit, src, f.Name, f.Name)
		case g.Origin() != f.Origin():
			c.errorf(x.Base, CodeType, "%s does not copy %s: %s's %s and %s's %s are declared apart, and a spread copies only a field both take from one declaration; %s (types.md, Structs 3)",
				lit, f.Name, src, f.Name, c.localTypeText(t), f.Name, fix(f.Name))
		case !c.identical(g.Type, f.Type):
			c.errorf(x.Base, CodeType, "%s does not copy %s: %s's %s is %s, and %s's is %s; name it: %s: … (types.md, Structs 3)",
				lit, f.Name, src, f.Name, c.localTypeText(g.Type), c.localTypeText(t), c.localTypeText(f.Type), f.Name)
		default:
			copies = append(copies, Spread{Field: f, From: g})
		}
	}
	if len(copies)+len(named) == len(s.Fields) {
		c.pkg.Info.Spreads[x] = copies
	}
}

// baseText is the base of an update as a diagnostic names it.
func baseText(base syntax.Expr) string {
	if what := exprText(base); what != "this expression" {
		return what
	}
	return "the base"
}

// orphanFields types the fields of a refused record update whose base is
// already typed, as orphans (orphanElements without the base).
func (c *checker) orphanFields(x *syntax.CompositeLit, mark int, lt func() litType) {
	for _, e := range x.Elts {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			e = kv.Value
		}
		c.orphanValue(e, mark, lt(), false)
	}
}

// updateBaseText is the diagnostic for a base of type got in an update of
// the type want: a union (its value may be another variant, so a match
// comes first), a generic record's other instance where the base gives the
// type arguments, or a value that is not a record.
func (c *checker) updateBaseText(base syntax.Expr, got, want Type) string {
	what := baseText(base)
	if u := asUnion(got); u != nil {
		for _, v := range u.Variants {
			if c.identical(v, want) || isOrigin(v, want) {
				return "the base of a record update of " + c.localTypeText(want) + " has the union type " + c.localTypeText(got) + ": " + what + " may be another variant; match it first (types.md, Structs 3)"
			}
		}
		return fmt.Sprintf("the base of %s{..%s} has the union type %s, which is not a record: match it first, and spread the variant (types.md, Structs 3)", c.localTypeText(want), what, c.localTypeText(got))
	}
	if _, ok := under(got).(*Struct); ok {
		// Only a generic T whose base gives its type arguments gets here.
		return "the base of a record update of " + c.localTypeText(want) + " has type " + c.localTypeText(got) + ", not " + c.localTypeText(want) + "; write " + c.localTypeText(want) + "'s type arguments to spread another record (types.md, Structs 3)"
	}
	return "the base of a record update of " + c.localTypeText(want) + " has type " + c.localTypeText(got) + ", which is not a record (types.md, Structs 3)"
}

// isOrigin reports whether v is an instance of the generic origin want.
func isOrigin(v, want Type) bool {
	vn, ok := v.(*Named)
	wn, isNamed := want.(*Named)
	return ok && isNamed && wn.Orig == nil && vn.origin() == wn
}
