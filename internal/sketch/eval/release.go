package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Releases (ORACLE O2; resources.md, Context ownership as built). A with
// that acquires a resource owns it: when its block ends, on every exit
// (the normal end, a return, a break, a typed failure, a defect), it
// releases each provider it built, last built first, each once, in the
// environment outside the with (item 6). A release is the provider's
// checked Close (Acquisition.Close), or its generated release
// (Acquisition.Drop: the type's own Close first, then each part that
// holds a resource). A Close has no error row, so a release only ends
// normally or with a defect; every release runs after an earlier one's
// defect, and the first defect is the with's outcome unless the block's
// own outcome is a defect already (R9, R25: every cause is kept; a cause
// that holds a defect ends the run as a defect). A budget stop or a form
// the evaluator does not run ends the obligation as incomplete or
// skipped, and releases nothing more.

// ownership is the reason a body is skipped when it owns a resource
// outside a with: an owned local or taken parameter (Info.OwnedVars), a
// temporary a statement owns (OwnedTemps), a std result a boundary drops
// (ResultDrops); each holds the release the lowering registers. Their
// releases (L8 B1, B2) wait for a later unit, so the evaluator runs no
// such body rather than run it without its releases. It is "" for a body
// that owns none, and is computed once per declaration.
func (in *interp) ownership(fr *frame) string {
	if in.owns == nil {
		in.owns = map[syntax.Decl]string{}
	}
	why, ok := in.owns[fr.decl]
	if ok {
		return why
	}
	info := fr.info
	if len(info.OwnedVars)+len(info.OwnedTemps)+len(info.ResultDrops) > 0 && fr.decl != nil {
		syntax.Inspect(fr.decl, func(n syntax.Node) bool {
			if n == nil || why != "" {
				return false
			}
			switch x := n.(type) {
			case *syntax.Ident:
				if v, ok := info.Defs[x].(*check.Var); ok && info.OwnedVars[v] != nil {
					why = "a body that owns the resource " + v.Name + ", whose release check-time evaluation does not run yet"
				}
			case *syntax.CallExpr:
				if info.ResultDrops[x] != nil {
					why = "a body that drops a std result holding a resource, whose release check-time evaluation does not run yet"
				}
			}
			if x, ok := n.(syntax.Expr); ok && info.OwnedTemps[x] != nil && why == "" {
				why = "a body that owns a temporary resource, whose release check-time evaluation does not run yet"
			}
			return why == ""
		})
	}
	in.owns[fr.decl] = why
	return why
}

// acquired is one provider a with built: its acquisition and its value
// before any conversion to an operation type.
type acquired struct {
	a *check.Acquisition
	v Value
}

// releaseAll releases built, last first, with the capabilities outer
// bound (the environment outside the with), and joins their defects with
// the block's outcome ab.
func (in *interp) releaseAll(fr *frame, at syntax.Node, built []acquired, outer *capEnv, ab *abrupt) *abrupt {
	if ab != nil && (ab.kind == ctlExhausted || ab.kind == ctlUnsupported) {
		return ab
	}
	saved := fr.caps
	fr.caps = outer
	defer func() { fr.caps = saved }()
	for i := len(built) - 1; i >= 0; i-- {
		b := built[i]
		if !b.a.Releases() {
			continue
		}
		var rel *abrupt
		if b.a.Drop != nil {
			rel = in.drop(fr, at, b.v, b.a.Drop)
		} else {
			rel = in.closeValue(fr, at, b.v, b.a.Close)
		}
		if rel == nil {
			continue
		}
		switch {
		case rel.kind == ctlExhausted || rel.kind == ctlUnsupported:
			return rel
		case ab == nil || ab.kind != ctlDefect:
			// A release's defect replaces a normal end, a return or a
			// typed failure: the cause holds a defect.
			ab = rel
		}
	}
	return ab
}

// closeValue calls the checked Close c on v.
func (in *interp) closeValue(fr *frame, at syntax.Node, v Value, c *check.Close) *abrupt {
	if ab := in.charge(fr, at, 1); ab != nil {
		return ab
	}
	switch {
	case c == nil:
		ice("a release with no Close")
	case c.Op != nil:
		_, ab := in.dispatch(fr, nil, v, c.Op.Name, nil)
		return releaseOutcome(fr, at, ab)
	case c.Func != nil:
		fn := c.Func
		if len(fn.Sig.TParams) > 0 {
			return fr.unsupported(at, "the release of a generic Close (%s) yet", fn.Name)
		}
		if len(fn.Sig.Params) != 1 {
			ice("a Close of %d parameters", len(fn.Sig.Params))
		}
		if _, ptr := fn.Sig.Params[0].Type.(*check.Pointer); ptr || fn.Ptr {
			return fr.unsupported(at, "the release of a Close that borrows its receiver yet")
		}
		_, ab := in.callResolved(fr, nil, fn, nil, []Value{v})
		return releaseOutcome(fr, at, ab)
	}
	ice("a Close with neither a function nor an operation")
	return nil
}

// releaseOutcome is a Close's completion as a release's: a Close has no
// error row, so anything but a defect or a stop is an internal error.
func releaseOutcome(fr *frame, at syntax.Node, ab *abrupt) *abrupt {
	if ab == nil {
		return nil
	}
	switch ab.kind {
	case ctlDefect, ctlExhausted, ctlUnsupported:
		return ab
	}
	ice("a Close completed with %d", ab.kind)
	return nil
}

// drop runs the generated release d of v (check/drop.go): the type's own
// Close first, then each part that holds a resource, last declared first;
// each part's release runs after an earlier one's defect, and the first
// defect is kept.
func (in *interp) drop(fr *frame, at syntax.Node, v Value, d *check.Drop) *abrupt {
	if ab := in.charge(fr, at, 1); ab != nil {
		return ab
	}
	var out *abrupt
	keep := func(ab *abrupt) bool {
		if ab == nil {
			return true
		}
		if ab.kind != ctlDefect {
			out = ab
			return false
		}
		if out == nil {
			out = ab
		}
		return true
	}
	if d.Close != nil && !keep(in.closeValue(fr, at, v, d.Close)) {
		return out
	}
	if d.Drain != nil {
		return fr.unsupported(at, "the release of the values a std operation type holds yet")
	}
	for _, f := range d.Fields {
		var part Value
		switch r := v.(type) {
		case *record:
			part = r.fields[f.Index]
		case tuple:
			part = r.elems[f.Index]
		default:
			return fr.unsupported(at, "the release of a part of %s yet", describe(v))
		}
		if !keep(in.drop(fr, at, part, f.Drop)) {
			return out
		}
	}
	if d.Elem != nil {
		s, ok := v.(sliceV)
		if !ok {
			// A Map's values: the release order would be the Map's,
			// which the Go target leaves unspecified.
			return fr.unsupported(at, "the release of the values of %s yet", describe(v))
		}
		for i := s.n - 1; i >= 0; i-- {
			if !keep(in.drop(fr, at, s.b.data[s.off+i], d.Elem)) {
				return out
			}
		}
	}
	if len(d.Variants) > 0 {
		r, ok := v.(*record)
		if !ok {
			return fr.unsupported(at, "the release of %s as a union yet", describe(v))
		}
		for _, vd := range d.Variants {
			if n, ok := vd.Type.(*check.Named); ok && n.Obj == r.t.Obj {
				keep(in.drop(fr, at, v, vd))
				break
			}
		}
	}
	return out
}
