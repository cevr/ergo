package lower

// IR builder: types (goType, admit). See ir.go.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// goType is t's Go spelling, for the types the subset admits: int, bool,
// string, records of those and slices of those. It declares the Go type.
func (b *irBuilder) goType(t check.Type, site syntax.Node) string {
	b.admit(t, 0)
	s := b.fc.goType(t)
	for _, r := range strings.TrimLeft(s, "[]") {
		if !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			irNo("Go type spelling %s", s)
		}
	}
	return s
}

func (b *irBuilder) admit(t check.Type, depth int) {
	if depth > 4 {
		irNo("deep type")
	}
	switch t := t.(type) {
	case *check.Basic:
		switch t.Kind {
		case check.Int, check.Bool, check.String:
			return
		}
		irNo("basic type %s", t.Name)
	case *check.Slice:
		b.admit(t.Elem, depth+1)
		return
	case *check.Named:
		if len(t.TArgs) > 0 || len(t.TParams) > 0 || t.Obj.Decl == nil {
			irNo("generic or declared-elsewhere type")
		}
		if _, ok := t.Obj.Decl.Spec.(*syntax.StructType); !ok {
			irNo("named type that is not a record")
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
