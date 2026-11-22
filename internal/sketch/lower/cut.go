package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A cut keeps uniqueness (NB-CUT; types.md, Bindings 2, V5 own-cut). A
// cut `s[i:j]` shares s's array: a let mut that takes it copies it unless
// no other binding can see a write through it. Two forms show that:
//
//   - a cut of a fresh value is fresh (freshCut): `MakeSlice[T](m)[0:k]`,
//     a cut of a call's fresh result or of a literal, shares an array no
//     binding holds;
//   - a parameter that the body takes into a let mut as a cut of itself,
//     `let mut ys = xs[0:n + 1]`, all in the body's own frame and outside
//     any loop, whose every other use either runs in another arm of a
//     match or an if than a taking (where it may use the parameter as it
//     likes) or is a read that keeps nothing before it, with no failure
//     after the first taking, is owned (cutOwned): its callers
//     pass it unshared (V5), and its body reads it as an unshared local.
//     So the cut is fresh where its read is the parameter's last
//     (freshPath), and the let mut takes it with no copy: a push written
//     in plain Ergo over cap, copy, a cut and an index write appends in
//     place, as Go's append does, where a copy of each cut made every
//     push O(n) and dropped the array's room.
//
// The cut's bounds are integers, and its operand is read into a temporary
// before they run; a bound that writes the root replaces the place or
// writes an element, and leaves the old array to no binding (own-reslice,
// storeread.go, holds the same).
//
// The reference is cutLowering "copy" (every such cut copied, as before),
// and valueLowering "copy" turns it off too. TestCutKeepsUniqueness counts
// each seeded shape's copies under both, on the Go text and the IR, and
// TestCutAgrees runs the programs under both and on the IR through ergo,
// writing through every cut and reading every source and alias after.
//
// Prior art: Go's own append writes the room after a slice's length in
// place, which is the aliasing Ergo's value semantics forbid unless the
// slice is unique; Swift's Array keeps the buffer when
// isKnownUniquelyReferenced holds and its ArraySlice shares it until a
// write; Koka and Lean reuse an array in place when its reference count is
// one (Perceus, functional but in place), which the value rule decides
// statically; Rust's Vec::truncate and split_off move the owned buffer.
// Gleam, Elixir and ReScript have immutable lists, so a cut is a new list
// or a view and never written.
var cutLowering string

// cutOn reports whether the cut rule (above) lowers this build.
func cutOn() bool { return valueOwned() && cutLowering != "copy" }

// freshCut reports whether x, a cut whose operand has no path root (a
// call, a literal, a conversion), is fresh: its operand is.
func (w *valueWalk) freshCut(x *syntax.SliceExpr) bool {
	if !cutOn() {
		return false
	}
	return w.fresh(x.X)
}

// cutOwned reports a parameter vv of f that the body takes over as a cut
// of itself (above): owned, as a consumed parameter is.
func (w *valueWalk) cutOwned(f *check.Func, vv *vvar) bool {
	if !cutOn() || vv.ptr || vv.local || len(vv.occs) == 0 {
		return false
	}
	var cuts []*vocc
	for _, o := range vv.occs {
		if o.frame != w.root || len(o.loops) > 0 {
			return false
		}
		if w.cutTaken(o) {
			cuts = append(cuts, o)
		}
	}
	first := -1
	for _, c := range cuts {
		for _, o := range vv.occs {
			if o == c || w.exclusive(o.id, c.id) {
				// Another arm runs instead of c's: it may use the
				// parameter as it likes, as an unshared local.
				continue
			}
			if o.pre > c.pre {
				return false
			}
			if w.classify(vv, o); o.kind != occRead {
				return false
			}
		}
		if first < 0 || c.pre < first {
			first = c.pre
		}
	}
	if first < 0 {
		return false
	}
	return len(rowElems(f.Sig.Errors)) == 0 || !w.failsAfter(f, first)
}

// cutTaken reports the use o as the operand of a cut of the whole
// variable, `p[i:j]`, that is the whole value of a let mut.
func (w *valueWalk) cutTaken(o *vocc) bool {
	parent, child := w.unparenParent(o.id)
	s, ok := parent.(*syntax.SliceExpr)
	if !ok || syntax.Node(s.X) != child {
		return false
	}
	for _, own := range w.owns {
		if unparen(own.value) == syntax.Expr(s) && own.root.local && own.root.mut && !own.root.ptr && own.root.init == own.value {
			return true
		}
	}
	return false
}
