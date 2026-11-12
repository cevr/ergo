package check

import "fmt"

// Go builtins as extern targets (OPT4b). `extern go append func Append[T](xs
// []T, x T) []T` binds Go's builtin append, in std and in user code alike:
// a target with no package path names a builtin of Go's universe scope, and
// the lowering writes the builtin at the call, with no Go function between
// (lower, builtinCall). Go's builtins are not functions of a package (their
// types are not Go types), so no binding is read: the declaration's
// signature must be one of the shapes below, each the builtin's own Go type
// at one instance. A builtin that writes its argument (copy, clear) takes a
// borrow, so the write is seen through a let mut root, as Ergo writes are
// (types.md, Bindings 1).
//
//	append  [T](xs []T, x T) []T
//	len     [T](xs []T) int        cap [T](xs []T) int
//	copy    [T](dst *[]T, src []T) int
//	clear   [T](xs *[]T)
//	delete  [K comparable, V](m Map[K, V], k K) Map[K, V]
//	min     (a T, b T) T           max (a T, b T) T, T an ordered type
//
// delete's value is the Map it wrote, as append's is the slice: a Map is a
// value (owner q26), so a binding returns m with k removed, and the
// lowering writes the source itself only where it is not seen again, and a
// copy of it otherwise (the value rule, lower/values.go preludeMapWrite).
// The prelude's Delete is this binding (OPT7).
//
// Prior art: ReScript's external binds a JavaScript builtin by its name;
// Gleam's @external names a host function; Go's own builtins have no
// function value, so a binding is call-only here too (externValue).
var goBuiltins = map[string]bool{"append": true, "len": true, "cap": true, "copy": true, "clear": true, "delete": true, "min": true, "max": true}

// GoBuiltin is the Go builtin an extern function f binds, or "" when its
// target names a function of a Go package.
func GoBuiltin(f *Func) string {
	if !isExtern(f) || f.Decl.Extern.Target == nil {
		return ""
	}
	path, name := f.Decl.Extern.Split()
	if path != "" || !goBuiltins[name] {
		return ""
	}
	return name
}

// builtinRefusal is the refusal of an extern of the Go builtin b whose
// signature is not b's shape, or "".
func builtinRefusal(f *Func, b string) string {
	s := f.Sig
	bad := func(shape string) string {
		return fmt.Sprintf("the extern %s binds Go's builtin %s, whose shape is func %s; this signature is not it", f.Name, b, shape)
	}
	if !s.Errors.Empty() || !s.Uses.Empty() {
		return bad("with no fails or needs row")
	}
	elem := func(t Type) Type {
		if sl, ok := t.(*Slice); ok {
			return sl.Elem
		}
		return nil
	}
	ptrElem := func(t Type) Type {
		if p, ok := t.(*Pointer); ok {
			return elem(p.Elem)
		}
		return nil
	}
	isInt := func(t Type) bool {
		bt, ok := t.(*Basic)
		return ok && bt.Kind == Int
	}
	one := len(s.TParams) == 1 && s.TParams[0].Kind == KindType
	var t Type
	if one {
		t = s.TParams[0]
	}
	switch b {
	case "append":
		if !one || len(s.Params) != 2 || len(s.Results) != 1 || elem(s.Params[0].Type) != t || s.Params[1].Type != t || elem(s.Results[0]) != t {
			return bad("[T](xs []T, x T) []T")
		}
	case "len", "cap":
		if !one || len(s.Params) != 1 || len(s.Results) != 1 || elem(s.Params[0].Type) != t || !isInt(s.Results[0]) {
			return bad("[T](xs []T) int")
		}
	case "copy":
		if !one || len(s.Params) != 2 || len(s.Results) != 1 || ptrElem(s.Params[0].Type) != t || elem(s.Params[1].Type) != t || !isInt(s.Results[0]) {
			return bad("[T](dst *[]T, src []T) int")
		}
	case "clear":
		if !one || len(s.Params) != 1 || len(s.Results) != 0 || ptrElem(s.Params[0].Type) != t {
			return bad("[T](xs *[]T)")
		}
	case "delete":
		if len(s.TParams) != 2 || s.TParams[0].Kind != KindType || s.TParams[1].Kind != KindType || len(s.Params) != 2 || len(s.Results) != 1 {
			return bad("[K comparable, V](m Map[K, V], k K) Map[K, V]")
		}
		k, v := s.TParams[0], s.TParams[1]
		isMap := func(t Type) bool {
			m, ok := t.(*Map)
			return ok && m.Key == k && m.Elem == v
		}
		if !isMap(s.Params[0].Type) || s.Params[1].Type != k || !isMap(s.Results[0]) {
			return bad("[K comparable, V](m Map[K, V], k K) Map[K, V]")
		}
	case "min", "max":
		if len(s.TParams) != 0 || len(s.Params) != 2 || len(s.Results) != 1 || s.Params[0].Type.String() != s.Params[1].Type.String() || s.Params[0].Type.String() != s.Results[0].String() || !orderedBasic(s.Results[0]) {
			return bad("(a T, b T) T, T an ordered basic type")
		}
	}
	return ""
}

// orderedBasic reports an integer, float or string type, or a type defined
// over one: the types Go's min and max take.
func orderedBasic(t Type) bool {
	if n, ok := t.(*Named); ok {
		t = n.Underlying()
	}
	bt, ok := t.(*Basic)
	if !ok {
		return false
	}
	switch bt.Kind {
	case Invalid, Bool, Complex64, Complex128:
		return false
	}
	return true
}
