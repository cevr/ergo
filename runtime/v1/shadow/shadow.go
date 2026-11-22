// Package shadow is the runtime leaf of a test build's rewrite shadow
// (binsize BS1): fmt and reflect, which no run build links.
package shadow

import (
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
)

// The run-time half of a rewrite over functions (docs/spec/types.md, Laws
// 14; LAWFFI r2 §3.6): Shadow is the test build's check of a rewrite at a
// site. AsWritten, which keeps a call tree as written, is the prelude's
// Ergo identity marked `// ergo:aswritten` (check.AsWritten).

// LawDefect is the defect a test build raises where a rewrite law is
// false at a site: the law's two sides gave values that key equality
// tells apart. At is the site, file:line:column.
type LawDefect struct {
	Law, At string
	// Lhs is the law's left side as written; Left and Right the two
	// sides' values, as Ergo text.
	Lhs, Left, Right string
	// For is the law's parameters at the site, `s = "a"`, comma-joined.
	For string
}

// String is the defect's text, which the entry's report writes after
// "panic: ".
func (d *LawDefect) String() string {
	s := "law " + d.Law + " is false at " + d.At + ": " + d.Lhs + " gave " + d.Left + ", and the right side gives " + d.Right
	if d.For != "" {
		s += ", for " + d.For
	}
	return s
}

// RefutedPrefix begins the line a shadow writes to standard error before
// its defect: `ergo-shadow: law <law> refuted [(<case>)]`, the case in
// ergo.cases's exact text when each parameter's type has one. ergo test
// reads it and writes the refutation into ergo.laws and ergo.cases
// (runtime.md, Tests 10), so a test failure is never a fact the ledger
// does not see.
const RefutedPrefix = "ergo-shadow: law "

// stderr is where Shadow writes its refutation line.
var stderr io.Writer = os.Stderr

// Shadow is a test build's rewrite site: left is the site's value, the
// left side's as the program wrote it, and right the right side's at the
// same arguments, evaluated after it. It returns left, the program's
// value, when key equality holds (floats by their bits, a NaN equal to a
// NaN; slices, maps, records and variants by their parts). When it does
// not, it writes the refutation line (RefutedPrefix) and raises a
// LawDefect. params names the law's parameters, comma-joined, and args
// are their values at the site, in order; enc says each parameter's type
// has a corpus text (the lowering knows; a listable type's is its index
// in the class table, which no run-time value tells).
func Shadow[T any](left, right T, law, at, lhs, params string, enc bool, args []any) T {
	if !keyEqual(reflect.ValueOf(&left).Elem(), reflect.ValueOf(&right).Elem()) {
		d := &LawDefect{Law: law, At: at, Lhs: lhs, Left: shadowText(reflect.ValueOf(&left).Elem()), Right: shadowText(reflect.ValueOf(&right).Elem())}
		names := strings.Split(params, ",")
		var fors, cases []string
		for i, a := range args {
			v := reflect.ValueOf(a)
			if i < len(names) {
				fors = append(fors, names[i]+" = "+shadowText(v))
			}
			if c, ok := caseText(v); ok && enc {
				cases = append(cases, c)
			}
		}
		d.For = strings.Join(fors, ", ")
		line := RefutedPrefix + law + " refuted"
		if enc && len(cases) == len(args) && len(args) > 0 {
			line += " (" + strings.Join(cases, ", ") + ")"
		}
		io.WriteString(stderr, line+"\n")
		panic(d)
	}
	return left
}

// caseText is v in ergo.cases's exact text (eval's encodeValue): a
// string quoted, an integer in decimal, a float in Go's hexadecimal form,
// a slice as [v, …].
func caseText(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.String:
		return strconv.Quote(v.String()), true
	case reflect.Float32:
		return strconv.FormatFloat(v.Float(), 'x', -1, 32), true
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'x', -1, 64), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(v.Uint(), 10), true
	case reflect.Slice:
		parts := make([]string, v.Len())
		for i := range v.Len() {
			p, ok := caseText(v.Index(i))
			if !ok {
				return "", false
			}
			parts[i] = p
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	}
	return "", false
}

// The canary (LAWFFI r2 §3.7, LF6d): a run build under
// -rewrites=canary[=N] lowers each rewritten site's right side, and on
// each N-th call at the site evaluates its left side too and compares.
// Each site has its own counter, by the index the lowering gave it; a
// program with more sites than counters shares them, which changes only
// the calls a check falls on.
var counts [1 << 16]atomic.Uint32

// Tick counts a call at the site and reports the N-th (every) one.
func Tick(site int, every uint32) bool {
	return counts[site&(len(counts)-1)].Add(1)%every == 0
}

// CanaryDefect is the defect the canary raises where a rewrite law is
// false at a site: it names the law and the site and no value, since a
// run build's values may be private.
type CanaryDefect struct{ Law, At string }

func (d *CanaryDefect) String() string {
	return "law " + d.Law + " is false at " + d.At + ": the canary evaluated both sides of the rewrite, and key equality tells them apart"
}

// CanaryFail raises the canary's defect at a site whose sides the site
// compared itself, where Go's == is key equality (a bool, an integer, a
// string): a call that small leaves the site's function inlinable.
func CanaryFail(law, at string) {
	panic(&CanaryDefect{Law: law, At: at})
}

// Canary is a canary check at a site: right is the value the program
// returns, left the left side's at the same arguments.
func Canary[T any](left, right T, law, at string) {
	if !keyEqual(reflect.ValueOf(&left).Elem(), reflect.ValueOf(&right).Elem()) {
		panic(&CanaryDefect{Law: law, At: at})
	}
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
