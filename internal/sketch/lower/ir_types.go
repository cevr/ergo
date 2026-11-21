package lower

// IR builder: types (goType, admit). See ir.go.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// goType is t's IR spelling, for the types the subset admits: the basic
// types but complex and uintptr, records of those and slices of those. It
// declares the Go type. The spelling is the lowered Go type's, except that
// a type of another package is qualified by the package's import path,
// not by its name in main.go: `time.Duration` (the decoder resolves the
// path among the package's imports, and the lowering keeps the import used,
// keepImport).
func (b *irBuilder) goType(t check.Type, site syntax.Node) string {
	switch u := t.(type) {
	case *check.Signature:
		return b.sigType(u, site) // ir_closure.go
	case *check.Tuple:
		if len(u.Types) >= 2 {
			return b.tupleType(u, site) // ir_tuple.go
		}
	case *check.Slice:
		if hasSig(u) || isTuple(u.Elem) {
			return "[]" + b.goType(u.Elem, site)
		}
	}
	b.admit(t, 0)
	return b.spelling(b.fc.goType(t)) // ir_pkg.go
}

func (b *irBuilder) admit(t check.Type, depth int) {
	if t == check.NoValue {
		return // a type parameter bound by a block with no value: Go's struct{} (goType), which the decoder spells (PORT-CALLS-2)
	}
	switch t := t.(type) {
	case *check.Basic:
		switch t.Kind {
		case check.Complex64, check.Complex128, check.Uintptr, check.Invalid:
			irNo("basic type %s", t.Name)
		}
		return
	case *check.Slice:
		b.admit(t.Elem, depth+1)
		return
	case *check.Array:
		b.admit(t.Elem, depth+1) // FLIP F7 (ir_index.go): Go's [N]T
		return
	case *check.Map:
		b.admitMap(t, depth) // ir_pkg.go
		return
	case *check.Pointer:
		// A pointer to an admitted type (FLIP F2; IRG4b r2): a *T
		// parameter, a receiver, &p.
		b.admit(t.Elem, depth+1)
		return
	case *check.TypeParam:
		// A type parameter of the generic body (F1): the decoder resolves
		// its Go name among the function's (and its receiver's) type
		// parameters. Values of it are moved, never operated on: an
		// operator admits only basic types (operator).
		if _, ok := b.fc.params[t]; !ok {
			irNo("type parameter outside its declaration")
		}
		return
	case *check.Tuple:
		// A tuple value: the emitter's struct{F0 T0; F1 T1} (ir_tuple.go).
		if len(t.Types) < 2 {
			irNo("type *check.Tuple")
		}
		for _, e := range t.Types {
			b.admit(e, depth+1)
		}
		return
	case *check.Signature:
		// A function value's type (ir_closure.go, sigType).
		if len(t.Results) > 1 {
			irNo("function type with several results")
		}
		for _, p := range t.Params {
			// A pointer to a record, a function value's parameter (std
			// html's Writer, func(b *strings.Builder)): the IR carries the
			// function value and spells its type; a body that reads through
			// the pointer is refused where it does (CF1).
			if pt, ok := p.Type.(*check.Pointer); ok {
				if n, ok := pt.Elem.(*check.Named); ok {
					b.admit(n, depth+1)
					continue
				}
			}
			b.admit(p.Type, depth+1)
		}
		for _, r := range t.Results {
			b.admit(r, depth+1)
		}
		return
	case *check.Named:
		if check.IsGoError(t) {
			// go.Error is the runtime's GoError, a Go type of its own
			// that every program may name (SP4: a Go call's failure).
			b.keepImport("var _ " + b.l.rt() + ".GoError")
			return
		}
		if check.IsGoAny(t) {
			return // Go's any (GB2), the predeclared go.Any (IR-GOCALLS)
		}
		if t.Obj.Pkg != nil && t.Obj.Pkg.Host() || isGoRef(t) {
			b.admitHost(t, depth) // ir_gocall.go (FORK-GOCALL, FORK-GOLIB)
			return
		}
		if len(t.TParams) > 0 || t.Obj.Decl == nil {
			irNo("generic or declared-elsewhere type")
		}
		b.admitArgs(t, depth) // ir_pkg.go
		if _, ok := t.Obj.Decl.Spec.(*syntax.StructType); !ok {
			b.admitNamed(t, depth) // ir_union.go
			return
		}
		if check.IsNever(t) {
			// The prelude's Never, a union of no variants declared with
			// a record's braces: Go's struct{}, as the emitter's (decls.go
			// typeDecl), which no body builds.
			return
		}
		s, ok := t.Underlying().(*check.Struct)
		if !ok {
			irNo("record without a struct")
		}
		if b.admitOnce(t.Obj) { // ir_pkg.go
			return
		}
		for _, f := range s.Fields {
			if funcField(f.Type) { // ir_machine.go
				continue
			}
			b.admit(f.Type, depth+1)
		}
		return
	case *check.Union:
		// A row, the error of a fallible call (a catch's `case err:`): a
		// Go error, its variants the error types (ir_tree.go).
		if failureRow(b.fc, t) { // ir_field.go
			for _, v := range t.Variants {
				if tp, ok := v.(*check.TypeParam); ok && tp.Kind == check.KindErrors {
					// An error row parameter of the row: its errors are
					// the Go error's values, whatever they are (the
					// emitter's goType of a row), no Go type of their own.
					continue
				}
				if _, ok := v.(*check.Wired); ok {
					// wired[T]: decode[T]'s shape failures, error types
					// T's argument decides (types.md, Codecs 2), values
					// of the Go error as an error row parameter's are
					// (the emitter's isErrorValue).
					continue
				}
				b.admit(v, depth+1)
			}
			return
		}
	}
	irNo("type %T", t)
}

// basicOf is t's basic type: t itself, or nil.
func basicOf(t check.Type) *check.Basic {
	bt, _ := t.(*check.Basic)
	return bt
}

// keepImport records a declaration that keeps an import of main.go used,
// such as `var _ time.Duration`: an IR body names the package, and Go
// refuses an unused import (the IR body is not Go text). The lowering has
// imported the package: asking for its name (timePkg, strconvPkg)
// registered the import. A census writes nothing.
func (b *irBuilder) keepImport(decl string) {
	if b.st.keep == nil {
		b.st.keep = map[string]bool{}
	}
	b.st.keep[decl] = true
}
