// Package shadow is the runtime leaf of a test build's rewrite shadow
// (binsize BS1): fmt and reflect, which no run build links.
package shadow

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
)

// The run-time half of a rewrite over functions (docs/spec/types.md, Laws
// 14; LAWFFI r2 §3.6): Shadow is the test build's check of a rewrite at a
// site. AsWritten, which keeps a call tree as written, is pure and lives
// in data.go.

// LawDefect is the defect a test build raises where a rewrite law is
// false at a site: the law's two sides gave values that key equality
// tells apart. At is the site, file:line:column.
type LawDefect struct {
	Law, At string
	// Lhs is the law's left side as written; Left and Right the two
	// sides' values, as Ergo text.
	Lhs, Left, Right string
}

// String is the defect's text, which the entry's report writes after
// "panic: ".
func (d *LawDefect) String() string {
	return "law " + d.Law + " is false at " + d.At + ": " + d.Lhs + " gave " + d.Left + ", and the right side gives " + d.Right
}

// Shadow is a test build's rewrite site: left is the site's value, the
// left side's as the program wrote it, and right the right side's at the
// same arguments, evaluated after it. It returns left, the program's
// value, when key equality holds (floats by their bits, a NaN equal to a
// NaN; slices, maps, records and variants by their parts), and raises a
// LawDefect when it does not.
func Shadow[T any](left, right T, law, at, lhs string) T {
	if !keyEqual(reflect.ValueOf(&left).Elem(), reflect.ValueOf(&right).Elem()) {
		panic(&LawDefect{Law: law, At: at, Lhs: lhs, Left: shadowText(reflect.ValueOf(&left).Elem()), Right: shadowText(reflect.ValueOf(&right).Elem())})
	}
	return left
}

// keyEqual is key equality of two values of one Go type.
func keyEqual(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Float32, reflect.Float64:
		x, y := a.Float(), b.Float()
		return math.Float64bits(x) == math.Float64bits(y) || x != x && y != y
	case reflect.Complex64, reflect.Complex128:
		x, y := a.Complex(), b.Complex()
		return keyEqual(reflect.ValueOf(real(x)), reflect.ValueOf(real(y))) && keyEqual(reflect.ValueOf(imag(x)), reflect.ValueOf(imag(y)))
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.Uint() == b.Uint()
	case reflect.String:
		return a.String() == b.String()
	case reflect.Array, reflect.Slice:
		if a.Len() != b.Len() {
			return false
		}
		for i := range a.Len() {
			if !keyEqual(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if a.Len() != b.Len() {
			return false
		}
		for it := a.MapRange(); it.Next(); {
			w := b.MapIndex(it.Key())
			if !w.IsValid() || !keyEqual(it.Value(), w) {
				return false
			}
		}
		return true
	case reflect.Struct:
		for i := range a.NumField() {
			if !keyEqual(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Interface, reflect.Pointer:
		// A variant of a union the lowering spells as an interface or a
		// pointer: the same dynamic type, and its value.
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		if a.Kind() == reflect.Interface && a.Elem().Type() != b.Elem().Type() {
			return false
		}
		return keyEqual(a.Elem(), b.Elem())
	}
	// A value class has no other kind (check.rewriteValueWhy).
	return false
}

// shadowText is v as Ergo text: a string quoted in ASCII (two strings
// that print alike, U+FFFD and an invalid byte, are told apart), a float
// in its shortest form, any other value as Go prints it.
func shadowText(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return strconv.QuoteToASCII(v.String())
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	}
	if v.CanInterface() {
		return fmt.Sprint(v.Interface())
	}
	return v.String()
}
