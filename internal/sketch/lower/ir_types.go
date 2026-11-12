package lower

// IR builder: types (goType, admit). See ir.go.

import (
	"strings"

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
	b.admit(t, 0)
	s := b.fc.goType(t)
	if b.usesDuration(t) {
		name := b.l.timePkg()
		s = strings.ReplaceAll(s, name+".Duration", "time.Duration")
		b.keepImport("var _ " + name + ".Duration")
	}
	for _, r := range strings.TrimLeft(s, "[]") {
		if !(r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			irNo("Go type spelling %s", s)
		}
	}
	if strings.Contains(strings.TrimPrefix(strings.TrimLeft(s, "[]"), "time.Duration"), ".") {
		irNo("Go type spelling %s", s)
	}
	return s
}

// usesDuration reports a type the subset admits that spells duration.
func (b *irBuilder) usesDuration(t check.Type) bool {
	switch t := t.(type) {
	case *check.Basic:
		return t.Kind == check.Duration
	case *check.Slice:
		return b.usesDuration(t.Elem)
	}
	return false
}

func (b *irBuilder) admit(t check.Type, depth int) {
	if depth > 4 {
		irNo("deep type")
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
	case *check.Named:
		if len(t.TArgs) > 0 || len(t.TParams) > 0 || t.Obj.Decl == nil {
			irNo("generic or declared-elsewhere type")
		}
		if _, ok := t.Obj.Decl.Spec.(*syntax.StructType); !ok {
			b.admitNamed(t, depth) // ir_union.go
			return
		}
		s, ok := t.Underlying().(*check.Struct)
		if !ok {
			irNo("record without a struct")
		}
		for _, f := range s.Fields {
			b.admit(f.Type, depth+1)
		}
		return
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
