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
// A record (a union's variant too) is a *record, a tuple a tuple, and the
// no-value of a unit call is unit{}. Values are never written in place: an
// assignment to a field builds a new record, so sharing is safe.
type Value any

// record is a record value with its nominal type: a union value is the
// variant's record, and a match tests its type.
type record struct {
	t      *check.Named
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

// zero is the zero value of t, or false when t has none the evaluator
// builds (a collection, a union, a function, an operation type).
func zero(t check.Type) (Value, bool) {
	if b := basicOf(t); b != nil {
		switch b.Kind {
		case check.Bool:
			return false, true
		case check.String:
			return "", true
		}
		return fromInt64(b.Kind, 0)
	}
	if n, ok := t.(*check.Named); ok {
		if s := structOf(n); s != nil {
			r := &record{t: n, fields: make([]Value, len(s.Fields))}
			for i, f := range s.Fields {
				z, ok := zero(f.Type)
				if !ok {
					return nil, false
				}
				r.fields[i] = z
			}
			return r, true
		}
	}
	if tt, ok := t.(*check.Tuple); ok {
		out := tuple{elems: make([]Value, len(tt.Types))}
		for i, e := range tt.Types {
			z, ok := zero(e)
			if !ok {
				return nil, false
			}
			out.elems[i] = z
		}
		return out, true
	}
	return nil, false
}

// sameNamed reports whether a and b are one nominal type: one declaration
// and identical type arguments.
func sameNamed(a, b *check.Named) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil || a.Obj != b.Obj || len(a.TArgs) != len(b.TArgs) {
		return false
	}
	for i := range a.TArgs {
		if a.TArgs[i].String() != b.TArgs[i].String() {
			return false
		}
	}
	return true
}

// equal is Ergo's == (types.md, Operators 1): Go's on basic values, and
// structural on records, tuples and union values (the same variant and
// equal fields). It reports the work it did through cost.
func equal(a, b Value, cost func(int64)) bool {
	switch a := a.(type) {
	case *record:
		bv, ok := b.(*record)
		if !ok || !sameNamed(a.t, bv.t) {
			return false
		}
		cost(int64(len(a.fields)))
		for i := range a.fields {
			if !equal(a.fields[i], bv.fields[i], cost) {
				return false
			}
		}
		return true
	case tuple:
		bv := b.(tuple)
		cost(int64(len(a.elems)))
		for i := range a.elems {
			if !equal(a.elems[i], bv.elems[i], cost) {
				return false
			}
		}
		return true
	case string:
		bs := b.(string)
		cost(int64(min(len(a), len(bs))))
		return a == bs
	case unit:
		return true
	}
	return a == b
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
	return fmt.Sprint(v)
}

func formatFloat(f float64, bits int) string { return strconv.FormatFloat(f, 'g', -1, bits) }
