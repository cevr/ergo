package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Codecs (types.md, Codecs; L11 design). A type has a wire form when the
// predeclared constraint codable admits it (spelled once, SetWire.String(); the owner keeps wire an ordinary identifier, 196w): the checker decides it from the
// type's structure, as comparable, and the predeclared encode and decode
// (step 2) walk that same structure. One rule, wireReason, owns the
// decision; the constraint, encode, decode and the lowering read it.
//
// The wire types: bool, the integer and float types, string, a refined
// type (it encodes as its base and decodes through its ParseN), a record
// whose fields are wire and that its package alone does not build (no
// unexported field: a codec never builds such a record field by field,
// decisions 2026-11-03 (a)), a union whose variants are wire, a slice, a
// Map with string keys, a tuple of wire types, and a type parameter whose
// constraint is codable. A function, an operation type, a pointer, a
// duration, a complex and uintptr are not.
//
// Prior art: Rust serde and Roc derive per type over a format's event
// interface; Go's encoding/json walks the structure by reflection at run
// time. Ergo walks it once, in the checker.

// wireReason is "" when t has a wire form, else why not: the first part
// of t that has none, and the path to it from t (S108's text).
func (c *checker) wireReason(t Type) string {
	path, bad := c.wireWalk(t, map[Type]bool{})
	if bad == nil {
		return ""
	}
	if path == "" {
		return fmt.Sprintf("%s is not "+SetWire.String()+": it %s (types.md, Codecs 1)", t, bad.why)
	}
	return fmt.Sprintf("%s is not "+SetWire.String()+": its part %s%s, of type %s, %s (types.md, Codecs 1)", t, typeBaseName(t), path, bad.typ, bad.why)
}

// wireFault is the part of a type that has no wire form, and why.
type wireFault struct {
	typ Type
	why string
}

// wireWalk returns the path to the first part of t with no wire form and
// that part, or a nil fault. seen holds the named types on the way, so a
// recursive type is wire when its other parts are.
func (c *checker) wireWalk(t Type, seen map[Type]bool) (string, *wireFault) {
	if tp, ok := t.(*TypeParam); ok {
		if typeSet(tp) == SetWire {
			return "", nil
		}
		return "", &wireFault{t, "is a type parameter without the codable constraint"}
	}
	if RefinementOf(t) != nil {
		return "", nil
	}
	if p := c.wirePair(t); p != nil {
		if _, bad := c.wireWalk(p.Base, seen); bad != nil {
			return "", &wireFault{t, fmt.Sprintf("has the wire form %s, which is not "+SetWire.String(), p.Base)}
		}
		return "", nil
	}
	if n, ok := t.(*Named); ok {
		if seen[n] {
			return "", nil
		}
		seen[n] = true
	}
	switch u := under(t).(type) {
	case *Basic:
		if isBoolean(u) || isString(u) || isFloat(u) || (isInteger(u) && u.Kind != Uintptr) {
			return "", nil
		}
		return "", &wireFault{t, "has no wire form"}
	case *Struct:
		for _, f := range u.Fields {
			if !isExported(f.Name) {
				return "", &wireFault{t, fmt.Sprintf("is built only by its package (its field %s is unexported), which declares no %s and Wire pair", f.Name, syntax.ParseName(typeBaseName(t)))}
			}
		}
		for _, f := range u.Fields {
			if p, bad := c.wireWalk(f.Type, seen); bad != nil {
				return "." + f.Name + p, bad
			}
		}
		return "", nil
	case *Union:
		for _, v := range u.Variants {
			if p, bad := c.wireWalk(v, seen); bad != nil {
				return fmt.Sprintf(".(%s)", v) + p, bad
			}
		}
		return "", nil
	case *Slice:
		p, bad := c.wireWalk(u.Elem, seen)
		return "[_]" + p, bad
	case *Map:
		if RefinementOf(u.Key) != nil {
			return "", &wireFault{t, fmt.Sprintf("has the refined key type %s, which a decode would have to parse key by key (not yet: write Map[string, V])", u.Key)}
		}
		if !isString(u.Key) {
			return "", &wireFault{t, fmt.Sprintf("has the key type %s, which is not a string", u.Key)}
		}
		p, bad := c.wireWalk(u.Elem, seen)
		return "[k]" + p, bad
	case *Tuple:
		for i, e := range u.Types {
			if p, bad := c.wireWalk(e, seen); bad != nil {
				return fmt.Sprintf(".%d", i) + p, bad
			}
		}
		return "", nil
	case *Interface:
		return "", &wireFault{t, "is an operation type"}
	case *Signature:
		return "", &wireFault{t, "is a function"}
	case *Pointer:
		return "", &wireFault{t, "is a pointer"}
	}
	return "", &wireFault{t, "has no wire form"}
}

// typeBaseName is the declared name of a named type, its text otherwise.
func typeBaseName(t Type) string {
	if n, ok := t.(*Named); ok && n.Obj != nil {
		return n.Obj.Name
	}
	return fmt.Sprint(t)
}
