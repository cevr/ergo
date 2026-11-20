package testgen

import (
	"math"
	"reflect"
)

// Near variants (runtime.md, Tests 6; CONSTRAINTS C5). A law parameter
// whose where names an earlier parameter of its own type, `b K where
// Equal(a, b)`, is found by search, not by rejection: two keys that are
// Equal and not identical are rare among random draws, and a law such as
// "equal keys hash equal" says something only about them.
//
// The near variants of a value are a list that depends on the value
// alone, so a case replays from its seed:
//
//   - a string or []byte: each single-bit flip of its first 16 bytes (bit
//     5 of an ASCII letter changes its case), then each byte dropped, then
//     each byte doubled;
//   - an integer: v+1, v-1, then each single-bit flip;
//   - a float: -v, then the other zero when v is a zero;
//   - a bool: !v;
//   - a record (a Go struct): each near variant of one field, field by
//     field;
//   - a slice: each near variant of one element, element by element.
//
// Any other value has none.

// nearBytes is how many leading bytes of a string have their bits flipped.
const nearBytes = 16

// Near is the near variants of v, in their fixed order.
func Near[T any](v T) []T {
	var out []T
	for _, x := range near(reflect.ValueOf(&v).Elem()) {
		out = append(out, x.Interface().(T))
	}
	return out
}

func near(v reflect.Value) []reflect.Value {
	var out []reflect.Value
	put := func(f func(n reflect.Value)) {
		n := reflect.New(v.Type()).Elem()
		f(n)
		out = append(out, n)
	}
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		for _, b := range nearStrings([]byte(s)) {
			put(func(n reflect.Value) { n.SetString(string(b)) })
		}
	case reflect.Bool:
		put(func(n reflect.Value) { n.SetBool(!v.Bool()) })
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		x := v.Int()
		put(func(n reflect.Value) { n.SetInt(x + 1) })
		put(func(n reflect.Value) { n.SetInt(x - 1) })
		for i := 0; i < v.Type().Bits(); i++ {
			put(func(n reflect.Value) { n.SetInt(x ^ (1 << i)) })
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		x := v.Uint()
		put(func(n reflect.Value) { n.SetUint(x + 1) })
		put(func(n reflect.Value) { n.SetUint(x - 1) })
		for i := 0; i < v.Type().Bits(); i++ {
			put(func(n reflect.Value) { n.SetUint(x ^ (1 << i)) })
		}
	case reflect.Float32, reflect.Float64:
		x := v.Float()
		put(func(n reflect.Value) { n.SetFloat(-x) })
		if x == 0 {
			put(func(n reflect.Value) { n.SetFloat(math.Copysign(0, -math.Copysign(1, x))) })
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			for _, b := range nearStrings(v.Bytes()) {
				put(func(n reflect.Value) { n.SetBytes(b) })
			}
			break
		}
		for i := 0; i < v.Len(); i++ {
			for _, e := range near(v.Index(i)) {
				put(func(n reflect.Value) {
					n.Set(reflect.MakeSlice(v.Type(), v.Len(), v.Len()))
					reflect.Copy(n, v)
					n.Index(i).Set(e)
				})
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !v.Type().Field(i).IsExported() {
				continue // reflection cannot write it: a follow-up
			}
			for _, f := range near(v.Field(i)) {
				put(func(n reflect.Value) {
					n.Set(v)
					n.Field(i).Set(f)
				})
			}
		}
	}
	return out
}

// nearStrings is the near variants of the bytes b.
func nearStrings(b []byte) [][]byte {
	var out [][]byte
	for i := 0; i < len(b) && i < nearBytes; i++ {
		for bit := 0; bit < 8; bit++ {
			c := append([]byte(nil), b...)
			c[i] ^= 1 << bit
			out = append(out, c)
		}
	}
	for i := range b {
		out = append(out, append(append([]byte(nil), b[:i]...), b[i+1:]...))
	}
	for i := range b {
		out = append(out, append(append(append([]byte(nil), b[:i+1]...), b[i]), b[i+1:]...))
	}
	return out
}

// Identical reports whether a and b are the same value bit for bit: a
// float's sign of zero and NaN's bits count, as == does not (0.0 == -0.0,
// so they are Equal under ==, yet distinct values a law can tell apart).
func Identical[T any](a, b T) bool {
	return identical(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem())
}

func identical(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Float32, reflect.Float64:
		return math.Float64bits(a.Float()) == math.Float64bits(b.Float())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !identical(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !identical(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.String:
		return a.String() == b.String()
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.Uint() == b.Uint()
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// freshTries bounds the fresh draws of a parameter whose where names no
// earlier parameter of its type, drawn until the where admits one.
const freshTries = 100

// Search draws a value that admits accepts (runtime.md, Tests 6). With a
// source (hasSrc), it first, with probability 1/4, tries one fresh draw;
// then the source's near variants in order, taking the first that admits
// accepts and is not identical to src; then src itself. Without one, it
// draws fresh values, up to freshTries. It reports whether admits held
// for the value, and whether the value is distinct from src.
func Search[T any](r *Stream, src T, hasSrc bool, fresh func() T, admits func(T) bool) (v T, admitted, distinct bool) {
	if !hasSrc {
		for i := 0; i < freshTries; i++ {
			if v = fresh(); admits(v) {
				return v, true, false
			}
		}
		return v, false, false
	}
	if r.Intn(4) == 0 {
		if v = fresh(); admits(v) {
			return v, true, !Identical(v, src)
		}
	}
	for _, v := range Near(src) {
		if !Identical(v, src) && admits(v) {
			return v, true, true
		}
	}
	return src, admits(src), false
}
