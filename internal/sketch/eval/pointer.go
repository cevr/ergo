package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Borrows (ORACLE O8; types.md, Borrows). A `*T` parameter holds a borrow
// of a place of its caller: `&x`, or the implicit borrow of a dot call
// whose function takes `*T`. The evaluator's borrow is the place itself, a
// slot and a path of fields and indexes from it: a read through the borrow
// reads the place as it is now, and a write through it writes the place,
// as the Go target's pointer does. A borrow is never stored, returned or
// captured by an escaping frame (Borrows 1), and two borrows of a call
// never overlap (Borrows 2): the checker refuses both, so the place
// outlives every use of the borrow and no write through one is seen
// through another.

// ptrV is a borrow: the place at path from root.
type ptrV struct {
	root *slot
	path []step
}

// borrow is a borrow of the place x: x's own value when x is a borrow
// already (a borrow passed on).
func (in *interp) borrow(fr *frame, x syntax.Expr) (Value, *abrupt) {
	if isPointer(fr.typeOf(x)) {
		return in.expr(fr, x)
	}
	root, steps, why, ab := in.place(fr, x)
	if ab != nil {
		return nil, ab
	}
	if why != "" {
		return nil, fr.unsupported(x, "a borrow of %s yet", why)
	}
	if ab := in.charge(fr, x, 1); ab != nil {
		return nil, ab
	}
	return &ptrV{root: root, path: steps}, nil
}

// pointerOf is the borrow x, an expression of a pointer type.
func (in *interp) pointerOf(fr *frame, x syntax.Expr) (*ptrV, *abrupt) {
	v, ab := in.expr(fr, x)
	if ab != nil {
		return nil, ab
	}
	p, ok := v.(*ptrV)
	if !ok {
		ice("a pointer's value is %T", v)
	}
	return p, nil
}

// peekBase is peekPath of x, through the borrow when x is one: the value
// of the borrowed place, the caller's slot at its root, and the place's
// path on *path.
func (in *interp) peekBase(fr *frame, x syntax.Expr, path *[]step) (Value, *slot, *abrupt) {
	if !isPointer(fr.typeOf(x)) {
		return in.peekPath(fr, x, path)
	}
	p, ab := in.pointerOf(fr, x)
	if ab != nil {
		return nil, nil, ab
	}
	if path != nil {
		*path = append(*path, p.path...)
	}
	v, ab := in.read(fr, p.root.v, p.path)
	return v, p.root, ab
}

// deref is the value of the borrowed place x points to.
func (in *interp) deref(fr *frame, x syntax.Expr) (Value, *abrupt) {
	v, root, ab := in.peekBase(fr, x, nil)
	if ab == nil && root != nil && !basicValue(v) {
		root.lend() // a part that holds a store leaves the slot
	}
	return v, ab
}

// receiver is a dot call's receiver as fn's first parameter takes it: a
// borrow of the receiver's place when fn takes *T of a value, the value
// when fn takes T through a borrow, else the receiver's value.
func (in *interp) receiver(fr *frame, recv syntax.Expr, fn *check.Func) (Value, *abrupt) {
	if fn == nil || len(fn.Sig.Params) == 0 {
		return in.expr(fr, recv)
	}
	wantPtr, isPtr := isPointer(fn.Sig.Params[0].Type), isPointer(fr.typeOf(recv))
	switch {
	case wantPtr && !isPtr:
		return in.borrow(fr, recv)
	case !wantPtr && isPtr:
		return in.deref(fr, recv)
	}
	return in.expr(fr, recv)
}

// placeThrough is the place a borrow x points to, as place gives it: its
// root and a copy of its path.
func (in *interp) placeThrough(fr *frame, x syntax.Expr) (*slot, []step, *abrupt) {
	p, ab := in.pointerOf(fr, x)
	if ab != nil {
		return nil, nil, ab
	}
	return p.root, append([]step(nil), p.path...), nil
}

func isPointer(t check.Type) bool {
	_, ok := t.(*check.Pointer)
	return ok
}
