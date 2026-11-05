package check

import "ergo.local/prototype/internal/sketch/syntax"

// recordUpdate checks a record update `T{..base, F: v, …}` (grammar.md,
// Expressions 11; types.md, Structs 3). It is a composite literal of T,
// checked by compositeLit's own rules, so every rule about who may write a
// T literal applies to it unchanged:
//   - T is a record type; its type arguments are written, given by the
//     expected type, or else taken from the base;
//   - the base is a value of exactly T, typed once and first, with T as its
//     hint: a union that has T as a variant is not T (match it first);
//   - at least one field follows, by the keyed-literal rules (structLit);
//     every field it omits is the base's.
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
	typed := false // the base is typed
	if n, ok := raw.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
		t = instanceFromHint(n, hint)
		if t == nil {
			// No written or expected type arguments: the base gives them.
			base := c.valueExpr(x.Base, nil)
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
		base := c.valueExpr(x.Base, t)
		if !base.invalid() && !c.identical(base.typ, t) {
			c.errorf(x.Base, CodeType, "%s", c.updateBaseText(x.Base, base.typ, t))
		}
	}
	if len(x.Elts) == 0 {
		// One form per job: the copy with no field replaced is the base.
		what := exprText(x.Base)
		if what == "this expression" {
			what = "the base"
		}
		c.errorf(x, CodeType, "a record update names at least one field; the copy is %s itself (types.md, Structs 3)", what)
		return operand{mode: modeValue, typ: t}
	}
	c.structLit(x, t, s, mark)
	return operand{mode: modeValue, typ: t}
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
// want: a union that lists want is named as such, since its value may be
// another variant.
func (c *checker) updateBaseText(base syntax.Expr, got, want Type) string {
	what := exprText(base)
	if what == "this expression" {
		what = "the base"
	}
	if u := asUnion(got); u != nil {
		for _, v := range u.Variants {
			if c.identical(v, want) || isOrigin(v, want) {
				return "the base of a record update of " + c.localTypeText(want) + " has the union type " + c.localTypeText(got) + ": " + what + " may be another variant; match it first (types.md, Structs 3)"
			}
		}
	}
	return "the base of a record update of " + c.localTypeText(want) + " has type " + c.localTypeText(got) + ", not " + c.localTypeText(want) + " (types.md, Structs 3)"
}

// isOrigin reports whether v is an instance of the generic origin want.
func isOrigin(v, want Type) bool {
	vn, ok := v.(*Named)
	wn, isNamed := want.(*Named)
	return ok && isNamed && wn.Orig == nil && vn.origin() == wn
}
