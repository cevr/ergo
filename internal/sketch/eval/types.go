package eval

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// tenv is a generic call's substitution: each type parameter of the callee
// stands for its type argument, read in the caller's own substitution. A
// type is never rebuilt: a type that names a parameter is read through the
// environment of the frame that holds it (rtype), so the evaluator needs
// no instantiation of its own and makes no second type semantics.
type tenv struct {
	m map[*check.TypeParam]rtype
}

// rtype is a checked type read in an environment: what it stands for in
// one running frame.
type rtype struct {
	t   check.Type
	env *tenv
}

// resolve follows type parameters to the type they stand for.
func (e *tenv) resolve(t check.Type) rtype {
	r := rtype{t: t, env: e}
	for {
		tp, ok := r.t.(*check.TypeParam)
		if !ok || r.env == nil {
			return r
		}
		b, bound := r.env.m[tp]
		if !bound {
			return r
		}
		r = b
	}
}

// under is the type under r: a defined type's base, read in r's env.
func (r rtype) under() rtype {
	r = r.env.resolve(r.t)
	if n, ok := r.t.(*check.Named); ok {
		return r.env.resolve(n.Underlying())
	}
	return r
}

// basic is the basic type under r, or nil.
func (r rtype) basic() *check.Basic {
	b, _ := r.under().t.(*check.Basic)
	return b
}

// typeKey is the identity of r's type as a string: one nominal type, one
// key, whatever *Named instance the checker made for it and whatever
// environment it is read in.
func typeKey(r rtype) string {
	var b strings.Builder
	writeKey(&b, r)
	return b.String()
}

func writeKey(b *strings.Builder, r rtype) {
	r = r.env.resolve(r.t)
	switch t := r.t.(type) {
	case *check.Named:
		if t.Obj.Pkg != nil {
			b.WriteString(t.Obj.Pkg.Path)
			b.WriteByte('.')
		}
		b.WriteString(t.Obj.Name)
		if len(t.TArgs) > 0 {
			b.WriteByte('[')
			for i, a := range t.TArgs {
				if i > 0 {
					b.WriteByte(',')
				}
				writeKey(b, rtype{a, r.env})
			}
			b.WriteByte(']')
		}
	case *check.Slice:
		b.WriteString("[]")
		writeKey(b, rtype{t.Elem, r.env})
	case *check.Array:
		b.WriteString("[" + strconv.FormatInt(t.Len, 10) + "]")
		writeKey(b, rtype{t.Elem, r.env})
	case *check.Map:
		b.WriteString("Map[")
		writeKey(b, rtype{t.Key, r.env})
		b.WriteByte(',')
		writeKey(b, rtype{t.Elem, r.env})
		b.WriteByte(']')
	case *check.Tuple:
		b.WriteByte('(')
		for i, e := range t.Types {
			if i > 0 {
				b.WriteByte(',')
			}
			writeKey(b, rtype{e, r.env})
		}
		b.WriteByte(')')
	case *check.Union:
		b.WriteByte('<')
		for i, v := range t.Variants {
			if i > 0 {
				b.WriteByte('|')
			}
			writeKey(b, rtype{v, r.env})
		}
		b.WriteByte('>')
	case *check.TypeParam:
		// Unbound: a parameter of no running call (a checker gap).
		b.WriteString("?" + t.Name)
	default:
		b.WriteString(r.t.String())
	}
}

// argsKey is the key of a nominal type's arguments: "" for a type with
// none, so a record of a type that is not generic costs no string.
func argsKey(n *check.Named, env *tenv) string {
	if len(n.TArgs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, a := range n.TArgs {
		if i > 0 {
			b.WriteByte(',')
		}
		writeKey(&b, rtype{a, env})
	}
	return b.String()
}

// zeroSize is the count of values the default of r builds, saturating at
// limit, so a huge [N]T is charged before anything is built. ok is false
// for a type with no default the evaluator builds.
func zeroSize(r rtype, limit int64) (int64, bool) {
	r = r.env.resolve(r.t)
	if b := r.basic(); b != nil {
		return 1, true
	}
	switch t := r.under().t.(type) {
	case *check.Slice, *check.Map:
		return 1, true
	case *check.Array:
		each, ok := zeroSize(rtype{t.Elem, r.under().env}, limit)
		if !ok {
			return 0, false
		}
		if t.Len > 0 && each > (limit-1)/t.Len {
			return limit, true
		}
		return 1 + t.Len*each, true
	case *check.Struct:
		n := int64(1)
		for _, f := range t.Fields {
			s, ok := zeroSize(rtype{f.Type, r.under().env}, limit)
			if !ok {
				return 0, false
			}
			n += s
			if n >= limit {
				return limit, true
			}
		}
		return n, true
	case *check.Tuple:
		n := int64(1)
		for _, e := range t.Types {
			s, ok := zeroSize(rtype{e, r.under().env}, limit)
			if !ok {
				return 0, false
			}
			n += s
			if n >= limit {
				return limit, true
			}
		}
		return n, true
	}
	return 0, false
}

// zeroOf builds the default of r (types.md, Zero values): every Map its
// own empty Map, an array of N defaults. The caller charged zeroSize.
func zeroOf(r rtype) (Value, bool) {
	r = r.env.resolve(r.t)
	if b := r.basic(); b != nil {
		switch b.Kind {
		case check.Bool:
			return false, true
		case check.String:
			return "", true
		}
		return fromInt64(b.Kind, 0)
	}
	u := r.under()
	switch t := u.t.(type) {
	case *check.Slice:
		return newSlice(nil), true
	case *check.Map:
		return newMap(), true
	case *check.Array:
		elems := make([]Value, t.Len)
		for i := range elems {
			z, ok := zeroOf(rtype{t.Elem, u.env})
			if !ok {
				return nil, false
			}
			elems[i] = z
		}
		s := newSlice(elems)
		s.array = true
		return s, true
	case *check.Struct:
		n, ok := r.t.(*check.Named)
		if !ok {
			return nil, false
		}
		rec := &record{t: n, key: argsKey(n, r.env), fields: make([]Value, len(t.Fields))}
		for i, f := range t.Fields {
			z, ok := zeroOf(rtype{f.Type, u.env})
			if !ok {
				return nil, false
			}
			rec.fields[i] = z
		}
		return rec, true
	case *check.Tuple:
		out := tuple{elems: make([]Value, len(t.Types))}
		for i, e := range t.Types {
			z, ok := zeroOf(rtype{e, u.env})
			if !ok {
				return nil, false
			}
			out.elems[i] = z
		}
		return out, true
	}
	return nil, false
}
