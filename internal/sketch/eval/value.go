package eval

import (
	"fmt"
	"go/constant"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// Value is an evaluated Ergo value, in a private tagged form. A basic
// value is the Go value of the Go type its checked type lowers to, so Go's
// own operators give the Go target's wrapping and rounding:
//
//	bool, string; int8 … int64 and uint8 … uint64; int and duration are
//	int64, uint and uintptr uint64 (the 64-bit Go targets); float32,
//	float64.
//
// A record (a union's variant too) is a *record, a tuple a tuple, a slice
// or an array a sliceV, a Map a mapV, a function literal's value a
// *closure, and the no-value of a unit call is unit{}. Records and tuples
// are never written in place: an assignment to a field builds a new
// record, so sharing one is safe. Slices, arrays and Maps share a store
// until a write, by ownership (coll.go).
type Value any

// record is a record value with its nominal type: a union value is the
// variant's record, and a match tests its type. key is the key of its
// type arguments in the frame that built it (types.go), "" for a type
// that is not generic.
type record struct {
	t      *check.Named
	key    string
	fields []Value
}

type tuple struct{ elems []Value }

type unit struct{}

// basicOf is the basic type under t, or nil.
func basicOf(t check.Type) *check.Basic {
	switch t := t.(type) {
	case *check.Basic:
		return t
	case *check.Named:
		b, _ := t.Underlying().(*check.Basic)
		return b
	}
	return nil
}

// structOf is the record type under t, or nil.
func structOf(t check.Type) *check.Struct {
	if n, ok := t.(*check.Named); ok {
		s, _ := n.Underlying().(*check.Struct)
		return s
	}
	return nil
}

// fromConstant is the value of the checked constant v of type t. The
// checker proved it fits.
func fromConstant(t check.Type, v constant.Value) (Value, bool) {
	b := basicOf(t)
	if b == nil {
		return nil, false
	}
	switch b.Kind {
	case check.Bool:
		return constant.BoolVal(v), true
	case check.String:
		return constant.StringVal(v), true
	case check.Float32:
		f, _ := constant.Float32Val(constant.ToFloat(v))
		return f, true
	case check.Float64:
		f, _ := constant.Float64Val(constant.ToFloat(v))
		return f, true
	}
	iv := constant.ToInt(v)
	if iv.Kind() != constant.Int {
		return nil, false
	}
	if i, exact := constant.Int64Val(iv); exact {
		return fromInt64(b.Kind, i)
	}
	if u, exact := constant.Uint64Val(iv); exact {
		return fromUint64(b.Kind, u)
	}
	return nil, false
}

// fromInt64 converts i to kind k as Go converts an int64.
func fromInt64(k check.BasicKind, i int64) (Value, bool) {
	switch k {
	case check.Int, check.Int64, check.Duration:
		return i, true
	case check.Int8:
		return int8(i), true
	case check.Int16:
		return int16(i), true
	case check.Int32:
		return int32(i), true
	case check.Uint, check.Uint64, check.Uintptr:
		return uint64(i), true
	case check.Uint8:
		return uint8(i), true
	case check.Uint16:
		return uint16(i), true
	case check.Uint32:
		return uint32(i), true
	case check.Float32:
		return float32(i), true
	case check.Float64:
		return float64(i), true
	}
	return nil, false
}

// fromUint64 converts u to kind k as Go converts a uint64.
func fromUint64(k check.BasicKind, u uint64) (Value, bool) {
	switch k {
	case check.Float32:
		return float32(u), true
	case check.Float64:
		return float64(u), true
	}
	return fromInt64(k, int64(u))
}

// fromFloat converts f to kind k: only to a float kind. Go leaves a float
// out of an integer's range implementation-defined, so the evaluator does
// not guess (plan section 4).
func fromFloat(k check.BasicKind, f float64) (Value, bool) {
	switch k {
	case check.Float32:
		return float32(f), true
	case check.Float64:
		return f, true
	}
	return nil, false
}

// convertBasic converts v to the basic kind k as Go's conversion does, or
// reports false for a conversion the evaluator does not take.
func convertBasic(v Value, k check.BasicKind) (Value, bool) {
	switch v := v.(type) {
	case int8:
		return fromInt64(k, int64(v))
	case int16:
		return fromInt64(k, int64(v))
	case int32:
		return fromInt64(k, int64(v))
	case int64:
		return fromInt64(k, v)
	case uint8:
		return fromUint64(k, uint64(v))
	case uint16:
		return fromUint64(k, uint64(v))
	case uint32:
		return fromUint64(k, uint64(v))
	case uint64:
		return fromUint64(k, v)
	case float32:
		return fromFloat(k, float64(v))
	case float64:
		return fromFloat(k, v)
	case string:
		if k == check.String {
			return v, true
		}
	case bool:
		if k == check.Bool {
			return v, true
		}
	}
	return nil, false
}

// sameNominal reports whether two records are of one nominal type: one
// declaration and type arguments of one key.
func sameNominal(a, b *record) bool {
	return a.t.Obj == b.t.Obj && a.key == b.key
}

// equal is Ergo's == (types.md, Operators 1): Go's on basic values, and
// structural on records, tuples, union values (the same variant and equal
// fields), slices and arrays (equal lengths, equal elements in order) and
// Maps (the same keys with equal values). spend pays each level's work
// before it is done; when it cannot pay, equal stops and reports !paid.
func equal(a, b Value, spend func(int64) bool) (eq, paid bool) {
	switch a := a.(type) {
	case *record:
		bv, ok := b.(*record)
		if !ok || !sameNominal(a, bv) {
			return false, true
		}
		if !spend(int64(len(a.fields))) {
			return false, false
		}
		for i := range a.fields {
			if eq, paid := equal(a.fields[i], bv.fields[i], spend); !eq || !paid {
				return eq, paid
			}
		}
		return true, true
	case tuple:
		bv := b.(tuple)
		if !spend(int64(len(a.elems))) {
			return false, false
		}
		for i := range a.elems {
			if eq, paid := equal(a.elems[i], bv.elems[i], spend); !eq || !paid {
				return eq, paid
			}
		}
		return true, true
	case sliceV:
		bv := b.(sliceV)
		if a.n != bv.n {
			return false, true
		}
		if !spend(int64(a.n)) {
			return false, false
		}
		ae, be := a.elems(), bv.elems()
		for i := range ae {
			if eq, paid := equal(ae[i], be[i], spend); !eq || !paid {
				return eq, paid
			}
		}
		return true, true
	case mapV:
		bv := b.(mapV)
		if len(a.b.m) != len(bv.b.m) {
			return false, true
		}
		if !spend(int64(len(a.b.m))) {
			return false, false
		}
		for k, av := range a.b.m {
			bw, ok := bv.b.m[k]
			if !ok {
				return false, true
			}
			if eq, paid := equal(av, bw, spend); !eq || !paid {
				return eq, paid
			}
		}
		return true, true
	case string:
		bs := b.(string)
		if !spend(int64(min(len(a), len(bs)))) {
			return false, false
		}
		return a == bs, true
	case unit:
		return true, true
	case *closure:
		ice("a function value compared")
	}
	return a == b, true
}

func truth(v Value) bool {
	b, ok := v.(bool)
	if !ok {
		ice("a condition of %T, not bool", v)
	}
	return b
}

// describe is an Ergo-like text of v for a report.
func describe(v Value) string {
	switch v := v.(type) {
	case *record:
		var parts []string
		s := structOf(v.t)
		for i, f := range v.fields {
			name := strconv.Itoa(i)
			if s != nil && i < len(s.Fields) {
				name = s.Fields[i].Name
			}
			parts = append(parts, name+": "+describe(f))
		}
		if len(parts) == 0 {
			return v.t.String()
		}
		return v.t.String() + "{" + strings.Join(parts, ", ") + "}"
	case tuple:
		var parts []string
		for _, e := range v.elems {
			parts = append(parts, describe(e))
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case string:
		return strconv.Quote(v)
	case float32:
		return formatFloat(float64(v), 32)
	case float64:
		return formatFloat(v, 64)
	case unit:
		return "no value"
	}
	if s, ok := describeColl(v); ok {
		return s
	}
	return fmt.Sprint(v)
}

func formatFloat(f float64, bits int) string { return strconv.FormatFloat(f, 'g', -1, bits) }
