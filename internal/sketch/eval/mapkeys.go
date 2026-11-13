package eval

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
)

// Map keys (ORACLE O30). A basic key is held as its Go value, so Go's
// own == and hashing decide it. A key with parts (a record, a tuple, an
// array, as dense's ID[K] is a record of one uint32) is held by its
// canonical text, a compKey: two keys are one key exactly when Go's ==
// on the lowered struct, array or tuple holds, field by field. The
// backing keeps the key itself beside its text, so a for over the Map, a
// report and the Map's Keys see the record, not the text.

// compKey is the canonical text of a key with parts.
type compKey string

// nans makes each NaN in a key with parts a key of its own: Go's == on a
// struct that holds a NaN is false, so no two such keys are one.
var nans atomic.Uint64

// canonKey is k as the backing's map holds it.
func canonKey(k Value) Value {
	if basicValue(k) {
		return k
	}
	var b strings.Builder
	writeCanon(&b, k)
	return compKey(b.String())
}

// writeCanon writes v's canonical text: its Go type and value, each part
// in order. -0 is 0, as Go's == has it.
func writeCanon(b *strings.Builder, v Value) {
	switch v := v.(type) {
	case *record:
		fmt.Fprintf(b, "R%p%s(", v.t, strconv.Quote(v.key))
		for _, f := range v.fields {
			writeCanon(b, f)
			b.WriteByte(',')
		}
		b.WriteByte(')')
	case tuple:
		b.WriteString("T(")
		for _, e := range v.elems {
			writeCanon(b, e)
			b.WriteByte(',')
		}
		b.WriteByte(')')
	case sliceV:
		if !v.array {
			ice("a Map key holds a slice")
		}
		b.WriteString("A(")
		for _, e := range v.elems() {
			writeCanon(b, e)
			b.WriteByte(',')
		}
		b.WriteByte(')')
	case string:
		b.WriteString("s" + strconv.Quote(v))
	case float64:
		writeFloat(b, "f", v)
	case float32:
		writeFloat(b, "g", float64(v))
	case mapV, *closure:
		ice("a Map key holds a %T", v)
	default:
		fmt.Fprintf(b, "%T:%v", v, v)
	}
}

func writeFloat(b *strings.Builder, kind string, f float64) {
	switch {
	case math.IsNaN(f):
		fmt.Fprintf(b, "%sNaN#%d", kind, nans.Add(1))
	case f == 0:
		b.WriteString(kind + "0")
	default:
		b.WriteString(kind + strconv.FormatFloat(f, 'g', -1, 64))
	}
}

// get is k's value.
func (m *mapBacking) get(k Value) (Value, bool) {
	v, ok := m.m[canonKey(k)]
	return v, ok
}

// has reports whether k is a key.
func (m *mapBacking) has(k Value) bool {
	_, ok := m.m[canonKey(k)]
	return ok
}

// put sets k to v.
func (m *mapBacking) put(k, v Value) {
	c := canonKey(k)
	if ck, ok := c.(compKey); ok {
		if m.orig == nil {
			m.orig = map[compKey]Value{}
		}
		if _, held := m.orig[ck]; !held {
			m.orig[ck] = k
		}
	}
	m.m[c] = v
}

// del removes k.
func (m *mapBacking) del(k Value) {
	c := canonKey(k)
	if ck, ok := c.(compKey); ok {
		delete(m.orig, ck)
	}
	delete(m.m, c)
}

// key is the key the backing holds as c.
func (m *mapBacking) key(c Value) Value {
	if ck, ok := c.(compKey); ok {
		return m.orig[ck]
	}
	return c
}

// each calls f with each key and its value, in Go's map order.
func (m *mapBacking) each(f func(k, v Value)) {
	for c, v := range m.m {
		f(m.key(c), v)
	}
}

// keys is the Map's keys, in Go's map order.
func (m *mapBacking) keys() []Value {
	ks := make([]Value, 0, len(m.m))
	for c := range m.m {
		ks = append(ks, m.key(c))
	}
	return ks
}

// plain is the Map as a Go map from its keys.
func (m *mapBacking) plain() map[Value]Value {
	if len(m.orig) == 0 {
		return m.m
	}
	out := make(map[Value]Value, len(m.m))
	m.each(func(k, v Value) { out[k] = v })
	return out
}
