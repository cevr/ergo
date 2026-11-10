package testplan

import "ergo.local/prototype/internal/sketch/check"

// State keys (X1, L7 EV4). A machine law's search hashes each state by its
// key, the canonical encoding runtime/explore's Key writes: a record its
// fields in declaration order, a union its variant's index and the variant,
// a slice its length and elements, a Map its entries in the order of their
// encodings, a float its bits (so 0 and -0 are apart and a NaN is one state
// by its payload). The test build's key functions (lower, machinelaws.go)
// and check-time evaluation (eval, machine.go) both read the rule here, so
// the two cannot disagree on which states have a key or how a basic value
// is written.

// Scalar is how a key writes a basic value.
type Scalar int

const (
	NoScalar Scalar = iota
	ScalarBool
	ScalarString
	ScalarInt     // Key.Int of the value as an int64
	ScalarUint    // Key.Uint of the value as a uint64
	ScalarFloat   // Key.Float of the value as a float64, by its bits
	ScalarComplex // Key.Complex of the value as a complex128
)

// ScalarOf is how a key writes a value of the basic kind k, NoScalar when
// the kind has no key.
func ScalarOf(k check.BasicKind) Scalar {
	switch {
	case k == check.Bool:
		return ScalarBool
	case k == check.String:
		return ScalarString
	case k == check.Duration, k >= check.Int && k <= check.Int64:
		return ScalarInt
	case k >= check.Uint && k <= check.Uintptr:
		return ScalarUint
	case k == check.Float32, k == check.Float64:
		return ScalarFloat
	case k == check.Complex64, k == check.Complex128:
		return ScalarComplex
	}
	return NoScalar
}

// KeyWhy is why values of t have no key, or "" when they have one. A type
// that holds a function, an operation type's value, a Go type or a runtime
// handle has no key: runtime reports a std type the target binds to a
// runtime value, and text writes a type as a message names it.
func KeyWhy(t check.Type, runtime func(*check.Named) bool, text func(check.Type) string) string {
	return keyWhy(t, runtime, text, map[string]bool{})
}

func keyWhy(t check.Type, runtime func(*check.Named) bool, text func(check.Type) string, seen map[string]bool) string {
	all := func(ts []check.Type) string {
		for _, e := range ts {
			if why := keyWhy(e, runtime, text, seen); why != "" {
				return why
			}
		}
		return ""
	}
	switch u := t.(type) {
	case *check.Basic:
		return ""
	case *check.Slice:
		return keyWhy(u.Elem, runtime, text, seen)
	case *check.Map:
		return all([]check.Type{u.Key, u.Elem})
	case *check.Tuple:
		return all(u.Types)
	case *check.Union:
		if u.Row == nil {
			return all(u.Variants)
		}
	case *check.Named:
		k := u.String()
		if seen[k] {
			return ""
		}
		seen[k] = true
		if _, ok := check.GoRefElem(u); ok || u.Obj.Decl == nil || u.Obj.Pkg == nil || u.Obj.Pkg.Host() || check.IsGoError(u) {
			return text(t) + " is a Go type"
		}
		if _, ops := u.Underlying().(*check.Interface); ops {
			return text(t) + " is an operation type"
		}
		if runtime(u) {
			return text(t) + " is a runtime handle"
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			fs := make([]check.Type, len(under.Fields))
			for i, f := range under.Fields {
				fs[i] = f.Type
			}
			return all(fs)
		case *check.Union:
			return all(under.Variants)
		case *check.Basic, *check.Slice, *check.Map, *check.Tuple:
			return keyWhy(under, runtime, text, seen)
		}
	}
	return text(t) + " holds a value with no key"
}
