package eval

import (
	"cmp"
	"fmt"
	"reflect"
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
	elem  int64 // the Go bytes of one slot, 0 when not known (slotBytes)
}

// sliceV is a slice or array value: n elements from off of a store. An
// array (array set) has its type's length.
type sliceV struct {
	b      *backing
	off, n int
	array  bool
}

// sliceRange observes element writes, not replacement of the ranged place.
// The place is root and the path from it (fields, and indexes with their
// values when the loop began); a write follows the view only through that
// same place, since another place may share the store until it is written.
type sliceRange struct {
	root *slot
	path []step
	view *sliceV
}

// samePath reports whether a and b name one place: the same fields, and
// the same index values.
func samePath(a, b []step) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].field != b[i].field || canonKey(a[i].index) != canonKey(b[i].index) {
			return false
		}
	}
	return true
}

func newSlice(elems []Value) sliceV {
	return sliceV{b: &backing{data: elems}, n: len(elems)}
}

func (s sliceV) elems() []Value { return s.b.data[s.off : s.off+s.n] }

// mapV is a Map value. A basic key is held as its Go value, so Go's own
// == and hashing decide it: -0 and +0 are one key and each NaN is a key
// of its own, as in the Go target; a key with parts is held by its
// canonical text (mapkeys.go).
type mapV struct{ b *mapBacking }

type mapBacking struct {
	m     map[Value]Value // by canonKey
	orig  map[compKey]Value
	owner *token
}

func newMap() mapV { return mapV{b: &mapBacking{m: map[Value]Value{}}} }

// keyOrder is the keys of m in one fixed order, a function of the keys
// alone. Go's map order changes from one range to the next, so work the
// evaluator charges while it walks a Map (a sort's comparisons, an
// equality that stops at the first different entry) walks this order, and
// a program's step counts are the same on every run
// (TestEvalReportIsDeterministic). The order costs no steps.
func keyOrder(m map[Value]Value) []Value {
	keys := make([]Value, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keyLess(keys[i], keys[j]) })
	return keys
}

// keyLess is a strict total order on the basic values a Map holds as keys:
// by Go type, then by value (cmp.Less, which puts NaN first).
func keyLess(a, b Value) bool {
	if ta, tb := reflect.TypeOf(a), reflect.TypeOf(b); ta != tb {
		return ta.String() < tb.String()
	}
	switch x := a.(type) {
	case string:
		return x < b.(string)
	case bool:
		return !x && b.(bool)
	case int:
		return cmp.Less(x, b.(int))
	case int8:
		return cmp.Less(x, b.(int8))
	case int16:
		return cmp.Less(x, b.(int16))
	case int32:
		return cmp.Less(x, b.(int32))
	case int64:
		return cmp.Less(x, b.(int64))
	case uint:
		return cmp.Less(x, b.(uint))
	case uint8:
		return cmp.Less(x, b.(uint8))
	case uint16:
		return cmp.Less(x, b.(uint16))
	case uint32:
		return cmp.Less(x, b.(uint32))
	case uint64:
		return cmp.Less(x, b.(uint64))
	case uintptr:
		return cmp.Less(x, b.(uintptr))
	case float32:
		return cmp.Less(x, b.(float32))
	case float64:
		return cmp.Less(x, b.(float64))
	case complex64:
		y := b.(complex64)
		if real(x) != real(y) {
			return cmp.Less(real(x), real(y))
		}
		return cmp.Less(imag(x), imag(y))
	case complex128:
		y := b.(complex128)
		if real(x) != real(y) {
			return cmp.Less(real(x), real(y))
		}
		return cmp.Less(imag(x), imag(y))
	}
	return fmt.Sprintf("%#v", a) < fmt.Sprintf("%#v", b)
}

// basicValue reports a value with no parts: one that holds no store.
func basicValue(v Value) bool {
	switch v.(type) {
	case *record, tuple, sliceV, mapV, *closure:
		return false
	}
	return true
}

// holdsNoStore reports a value that reaches no slice or Map store: a
// basic value, or a record or tuple of such values. A record is never
// written in place (storePath builds a new one at each field), so a read
// that lets such a part out of a slot shares nothing a later write
// through the slot could change, and the slot need not lend: Go copies
// the element and leaves the slice alone (MX-EVWITNESS: vt's Print read
// a cell and then wrote one, and each write copied the whole screen).
func holdsNoStore(v Value) bool {
	switch v := v.(type) {
	case *record:
		for _, f := range v.fields {
			if !holdsNoStore(f) {
				return false
			}
		}
		return true
	case tuple:
		for _, e := range v.elems {
			if !holdsNoStore(e) {
				return false
			}
		}
		return true
	}
	return basicValue(v)
}

// ownSlice is s as a value whose store tok owns: s itself, or a copy of
// its elements, charged before it is made.
func (in *interp) ownSlice(fr *frame, at syntax.Node, s sliceV, tok *token) (sliceV, *abrupt) {
	if tok != nil && s.b.owner == tok {
		return s, nil
	}
	if ab := in.buildSlots(fr, at, int64(s.n)+1, int64(s.n), s.b.slotBytes()); ab != nil {
		return s, ab
	}
	data := append([]Value(nil), s.elems()...)
	return sliceV{b: &backing{data: data, owner: tok, elem: s.b.elem}, n: s.n, array: s.array}, nil
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
	var orig map[compKey]Value
	if len(m.b.orig) > 0 {
		orig = make(map[compKey]Value, len(m.b.orig))
		for k, v := range m.b.orig {
			orig[k] = v
		}
	}
	return mapV{b: &mapBacking{m: out, orig: orig, owner: tok}}, nil
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
			if ab := in.buildSlots(fr, at, int64(len(xs.b.data)), 2*int64(len(xs.b.data))+1, xs.b.slotBytes()); ab != nil {
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
	if ab := in.buildSlots(fr, at, int64(xs.n)+1, int64(xs.n)+1+int64(xs.n)/2, xs.b.slotBytes()); ab != nil {
		return nil, ab
	}
	data := make([]Value, xs.n, xs.n+1+xs.n/2)
	copy(data, xs.elems())
	data = append(data, x)
	return sliceV{b: &backing{data: data, owner: own, elem: xs.b.elem}, n: xs.n + 1}, nil
}

// appendAll is xs with ys's elements added, Go's append(xs, ys...) (an
// extern of append at its spread shape, slices.AppendAll): appendTo's
// rules for one bulk append. xs's store grows in place only when no value
// sees past xs's end; else the result has a store of its own.
func (in *interp) appendAll(fr *frame, at syntax.Node, xs, ys sliceV) (Value, *abrupt) {
	for _, r := range in.ranges {
		if r.view.b == xs.b {
			return nil, fr.unsupported(at, "an Append of an active slice range, whose backing capacity the Go target decides")
		}
	}
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	if ys.n == 0 {
		return xs, nil
	}
	add := ys.elems()
	n := int64(xs.n) + int64(ys.n)
	if xs.off+xs.n == len(xs.b.data) {
		if len(xs.b.data)+ys.n > cap(xs.b.data) {
			if ab := in.build(fr, at, int64(ys.n), n+n/2, 0); ab != nil {
				return nil, ab
			}
		} else if ab := in.charge(fr, at, int64(ys.n)); ab != nil {
			return nil, ab
		}
		xs.b.owner = nil // another value may share the store now
		xs.b.data = append(xs.b.data, add...)
		return sliceV{b: xs.b, off: xs.off, n: int(n)}, nil
	}
	if ab := in.build(fr, at, n, n+n/2, 0); ab != nil {
		return nil, ab
	}
	data := make([]Value, xs.n, int(n+n/2))
	copy(data, xs.elems())
	data = append(data, add...)
	return sliceV{b: &backing{data: data}, n: int(n)}, nil
}

// putIn is m with k set to v (Put, and an index write's last step).
func (in *interp) putIn(fr *frame, at syntax.Node, m mapV, k, v Value, own *token) (Value, *abrupt) {
	m, ab := in.ownMap(fr, at, m, own)
	if ab != nil {
		return nil, ab
	}
	m.b.put(k, v)
	return m, nil
}

// deleteIn is m without k.
func (in *interp) deleteIn(fr *frame, at syntax.Node, m mapV, k Value, own *token) (Value, *abrupt) {
	if !m.b.has(k) {
		return m, nil
	}
	m, ab := in.ownMap(fr, at, m, own)
	if ab != nil {
		return nil, ab
	}
	m.b.del(k)
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
		v.b.each(func(k, e Value) {
			parts = append(parts, describe(k)+": "+describe(e))
		})
		sort.Strings(parts)
		return "{" + strings.Join(parts, ", ") + "}", true
	case *closure:
		return fmt.Sprintf("a function literal at %d", v.lit.Pos()), true
	case *funcRef:
		return "the function " + v.fn.Name, true
	case *nativeFunc:
		return "the function " + v.name, true
	case *machinePlan:
		return v.tn.Name + "{}", true
	case *ptrV:
		return "a borrow", true
	}
	return "", false
}
