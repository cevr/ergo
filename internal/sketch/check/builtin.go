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
//	append  [T](xs []T, x T) []T   (xs []byte, s string) []byte
//	len     [T](xs []T) int        cap [T](xs []T) int
//	copy    [T](dst *[]T, src []T) int
//	clear   [T](xs *[]T)
//	min     (a T, b T) T           max (a T, b T) T, T an ordered type
//
// Prior art: ReScript's external binds a JavaScript builtin by its name;
// Gleam's @external names a host function; Go's own builtins have no
// function value, so a binding is call-only here too (externValue).
var goBuiltins = map[string]bool{"append": true, "len": true, "cap": true, "copy": true, "clear": true, "min": true, "max": true}

// IsGoBuiltin reports whether name is a Go builtin an extern may bind
// with no package path.
func IsGoBuiltin(name string) bool { return goBuiltins[name] }

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

// AppendsString reports an extern of Go's append at its second shape,
// `(xs []byte, s string) []byte`: Go's append(xs, s...), the bytes of s
// added at the end of xs (interop.md, Lowering 9). It is a concatenation, as
// `+` on strings is, and the check-time evaluator runs it as one.
func AppendsString(f *Func) bool {
	if GoBuiltin(f) != "append" {
		return false
	}
	s := f.Sig
	if len(s.TParams) != 0 || len(s.Params) != 2 || len(s.Results) != 1 || !s.Errors.Empty() || !s.Uses.Empty() {
		return false
	}
	isBytes := func(t Type) bool {
		sl, ok := t.(*Slice)
		if !ok {
			return false
		}
		b, ok := sl.Elem.(*Basic)
		return ok && b.Kind == Uint8
	}
	b, ok := s.Params[1].Type.(*Basic)
	return isBytes(s.Params[0].Type) && ok && b.Kind == String && isBytes(s.Results[0])
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
		if AppendsString(f) {
			return ""
		}
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
