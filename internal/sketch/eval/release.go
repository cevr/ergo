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
	if stops(ab) {
		return ab
	}
	saved := fr.caps
	fr.caps = outer
	defer func() { fr.caps = saved }()
	in.mask(1)
	defer in.mask(-1)
	for i := len(built) - 1; i >= 0; i-- {
		b := built[i]
		if !b.a.Releases() {
			continue
		}
		var rel *abrupt
		if b.a.Drop != nil {
			rel = in.drop(fr, at, b.v, b.a.Drop, 0)
		} else {
			rel = in.closeValue(fr, at, b.v, b.a.Close)
		}
		if rel == nil {
			continue
		}
		switch {
		case stops(rel):
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
	case ctlDefect, ctlExhausted, ctlUnsupported, ctlAbort:
		return ab
	}
	ice("a Close completed with %d", ab.kind)
	return nil
}

// drop runs the generated release d of v (check/drop.go): the type's own
// Close first, then each part that holds a resource, last declared first;
// each part's release runs after an earlier one's defect, and the first
// defect is kept. taken are the top record's fields its owner took in
// place (R39), which it skips.
func (in *interp) drop(fr *frame, at syntax.Node, v Value, d *check.Drop, taken uint64) *abrupt {
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
	for _, f := range d.Fields {
		if f.Index < 64 && taken&(1<<f.Index) != 0 {
			continue
		}
		var part Value
		switch r := v.(type) {
		case *record:
			part = r.fields[f.Index]
		case tuple:
			part = r.elems[f.Index]
		default:
			return fr.unsupported(at, "the release of a part of %s yet", describe(v))
		}
		if !keep(in.drop(fr, at, part, f.Drop, 0)) {
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
			if !keep(in.drop(fr, at, s.b.data[s.off+i], d.Elem, 0)) {
				return out
			}
		}
	}
	if d.Drain != nil {
		// A std queue's values left, or a Task's value no Wait took
		// (Q7, R13, Q5): released last entered first.
		dr, ok := v.(drainer)
		if !ok {
			return fr.unsupported(at, "the release of the values %s holds yet", describe(v))
		}
		left := dr.drain()
		for i := len(left) - 1; i >= 0; i-- {
			if !keep(in.drop(fr, at, left[i], d.Drain, 0)) {
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
				keep(in.drop(fr, at, v, vd, 0))
				break
			}
		}
	}
	return out
}
