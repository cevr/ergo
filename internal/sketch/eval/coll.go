package eval

import (
	"fmt"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Collections are values (owner q26; types.md, Bindings 1): an
// assignment, an argument, a field and a capture each hold their own. The
// evaluator shares storage and copies on a write, by ownership:
//
//   - A slot (a local or a parameter) holds a value and a token. A store
//     whose owner is the slot's token is reachable only through that
//     slot's value, so a write through the slot changes it in place.
//   - A read that lets a part of the slot's value out (the whole value, or
//     a part that is not a basic value) lends it: the slot drops its
//     token, so the next write through the slot copies each store on the
//     written path once, and owns the copies.
//   - xs.Append(x) extends the store in place when xs ends at the store's
//     end, as Go's append does at a slice with room; a value that ends
//     before the store's end copies. So no Append writes over an element
//     another value sees.
//   - x = x.Append(e), x = x.Put(k, v) and x = x.Delete(k) hand x over: the
//     receiver is not lent, since the assignment replaces it, and a store x
//     owns changes in place, as the lowering's owned parameters do.
//
// A copy is charged its length before it is made, so a program that copies
// much exhausts the budget, never the host's memory.

// token is the owner of stores: one per slot, until a read lends. It has
// a size: Go may give every new zero-size value one address, and two
// tokens must never be equal.
type token struct{ _ byte }

// slot is a local's or parameter's storage.
type slot struct {
	v   Value
	tok *token
}

// lend drops the slot's ownership: what was its own may now be shared.
func (s *slot) lend() { s.tok = nil }

// own is the slot's token, made on the first write.
func (s *slot) own() *token {
	if s.tok == nil {
		s.tok = &token{}
	}
	return s.tok
}

// backing is the store of a slice or an array.
type backing struct {
	data  []Value
	owner *token
}

// sliceV is a slice or array value: n elements from off of a store. An
// array (array set) has its type's length.
type sliceV struct {
	b      *backing
	off, n int
	array  bool
}

// sliceRange observes element writes, not replacement of the ranged place.
type sliceRange struct {
	root *slot
	view *sliceV
}

func newSlice(elems []Value) sliceV {
	return sliceV{b: &backing{data: elems}, n: len(elems)}
}

func (s sliceV) elems() []Value { return s.b.data[s.off : s.off+s.n] }

// mapV is a Map value. A key is a basic value, held as its Go value, so
// Go's own == and hashing decide it: -0 and +0 are one key and each NaN
// is a key of its own, as in the Go target.
type mapV struct{ b *mapBacking }

type mapBacking struct {
	m     map[Value]Value
	owner *token
}

func newMap() mapV { return mapV{b: &mapBacking{m: map[Value]Value{}}} }

// basicValue reports a value with no parts: one that holds no store.
func basicValue(v Value) bool {
	switch v.(type) {
	case *record, tuple, sliceV, mapV, *closure:
		return false
	}
	return true
}

// ownSlice is s as a value whose store tok owns: s itself, or a copy of
// its elements, charged before it is made.
func (in *interp) ownSlice(fr *frame, at syntax.Node, s sliceV, tok *token) (sliceV, *abrupt) {
	if tok != nil && s.b.owner == tok {
		return s, nil
	}
	if ab := in.build(fr, at, int64(s.n)+1, int64(s.n), 0); ab != nil {
		return s, ab
	}
	data := append([]Value(nil), s.elems()...)
	return sliceV{b: &backing{data: data, owner: tok}, n: s.n, array: s.array}, nil
}

// ownMap is m as a value whose store tok owns.
func (in *interp) ownMap(fr *frame, at syntax.Node, m mapV, tok *token) (mapV, *abrupt) {
	if tok != nil && m.b.owner == tok {
		return m, nil
	}
	if ab := in.build(fr, at, int64(len(m.b.m))+1, 2*int64(len(m.b.m)+1), 0); ab != nil {
		return m, ab
	}
	out := make(map[Value]Value, len(m.b.m)+1)
	for k, v := range m.b.m {
		out[k] = v
	}
	return mapV{b: &mapBacking{m: out, owner: tok}}, nil
}

// appendTo is xs with x at its end. own is the token of the slot that
// hands xs over (x = x.Append(e)), nil otherwise.
func (in *interp) appendTo(fr *frame, at syntax.Node, xs sliceV, x Value, own *token) (Value, *abrupt) {
	for _, r := range in.ranges {
		if r.view.b == xs.b {
			return nil, fr.unsupported(at, "an Append of an active slice range, whose backing capacity the Go target decides")
		}
	}
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	if xs.off+xs.n == len(xs.b.data) {
		if len(xs.b.data) == cap(xs.b.data) {
			if ab := in.build(fr, at, int64(len(xs.b.data)), 2*int64(len(xs.b.data))+1, 0); ab != nil {
				return nil, ab
			}
		}
		// No value sees past xs's end, so the store grows in place. It
		// stays the slot's own only when the slot hands over its own
		// store: else another value may share it now.
		if own == nil || xs.b.owner != own {
			xs.b.owner = nil
		}
		xs.b.data = append(xs.b.data, x)
		return sliceV{b: xs.b, off: xs.off, n: xs.n + 1}, nil
	}
	if ab := in.build(fr, at, int64(xs.n)+1, int64(xs.n)+1+int64(xs.n)/2, 0); ab != nil {
		return nil, ab
	}
	data := make([]Value, xs.n, xs.n+1+xs.n/2)
	copy(data, xs.elems())
	data = append(data, x)
	return sliceV{b: &backing{data: data, owner: own}, n: xs.n + 1}, nil
}

// putIn is m with k set to v (Put, and an index write's last step).
func (in *interp) putIn(fr *frame, at syntax.Node, m mapV, k, v Value, own *token) (Value, *abrupt) {
	m, ab := in.ownMap(fr, at, m, own)
	if ab != nil {
		return nil, ab
	}
	m.b.m[k] = v
	return m, nil
}

// deleteIn is m without k.
func (in *interp) deleteIn(fr *frame, at syntax.Node, m mapV, k Value, own *token) (Value, *abrupt) {
	if _, ok := m.b.m[k]; !ok {
		return m, nil
	}
	m, ab := in.ownMap(fr, at, m, own)
	if ab != nil {
		return nil, ab
	}
	delete(m.b.m, k)
	return m, nil
}

// describeColl is the text of a collection for a report.
func describeColl(v Value) (string, bool) {
	switch v := v.(type) {
	case sliceV:
		parts := make([]string, 0, v.n)
		for _, e := range v.elems() {
			parts = append(parts, describe(e))
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	case mapV:
		parts := make([]string, 0, len(v.b.m))
		for k, e := range v.b.m {
			parts = append(parts, describe(k)+": "+describe(e))
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ", ") + "}", true
	case *closure:
		return fmt.Sprintf("a function literal at %d", v.lit.Pos()), true
	}
	return "", false
}
