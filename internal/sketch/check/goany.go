package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// go.Any (GB2; docs/spec/interop.md, Type mapping and Go any). Go's empty
// interface, `any` or `interface{}`, at the import go boundary: the type of
// a Go parameter, result, field or element that takes any Go value
// (`db.ExecContext(q, args ...any)`, `rdb.Set(k, v any, ttl)`, `rows.Scan(
// dest ...any)`). A value goes in where its type has a Go form that Go reads
// as itself (goAnyForm): Go's own conversion to its empty interface, no
// adapter, nothing more. A borrow `&x` goes in as its Go pointer, so a Go
// function writes x (`rows.Scan([&id, &name])`). Only go.As[T](a) takes a
// value out, as Option[T]. Ergo has no any (D-E2): no operator, comparison
// or match reads a go.Any, and no Ergo record stores one (goAnyStored).

// IsGoAny reports the predeclared go.Any.
func IsGoAny(t Type) bool {
	universe()
	n, ok := t.(*Named)
	return ok && n == universeOnce.any
}

// goAnyText is the rule's statement in the diagnostics.
const goAnyText = "go.Any takes a value Go reads as itself: a basic value, a value of a Go type, a go.Ref, a borrow of one (&x), or a slice, array or Map of them"

// goAnyAs checks go.As[T](a) on a go.Any: Some(the value) when a holds a
// Go value of T's Go form, else None, as Go's `v, ok := a.(T)`. T has a Go
// form, shares no memory with Go and is no go.Any (a borrow is no type
// argument: types.md, Borrows 1).
func (c *checker) goAnyAs(x *syntax.CallExpr, targ Type) operand {
	if targ == nil {
		c.errorf(x, CodeType, "go.As needs the type to take out of the go.Any: go.As[T](a) (docs/spec/interop.md, Go any)")
		return bad(x)
	}
	if isInvalid(targ) {
		return bad(x)
	}
	why := ""
	switch {
	case IsGoAny(targ):
		why = "a go.Any already"
	case goAnyShared(targ):
		// The value rule copies Go's slice or Map at a call's result
		// (interop.md, Lowering 3); an assertion would share it.
		why = fmt.Sprintf("%s would share Go's memory; take it from a Go call that returns it", targ)
	default:
		why = c.goAnyForm(targ)
	}
	if why != "" {
		c.errorf(x, CodeType, "go.As[%s] on a go.Any: %s", targ, why)
		return bad(x)
	}
	return operand{mode: modeValue, typ: c.instantiate(preludeType("Option"), []Type{targ})}
}

// mentionsGoAny reports a type that holds a go.Any at any depth: itself, an
// element, a key, a type argument or a tuple member.
func mentionsGoAny(t Type, seen map[Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch t := t.(type) {
	case *Named:
		if IsGoAny(t) {
			return true
		}
		for _, a := range t.TArgs {
			if mentionsGoAny(a, seen) {
				return true
			}
		}
	case *Slice:
		return mentionsGoAny(t.Elem, seen)
	case *Array:
		return mentionsGoAny(t.Elem, seen)
	case *Map:
		return mentionsGoAny(t.Key, seen) || mentionsGoAny(t.Elem, seen)
	case *Tuple:
		for _, e := range t.Types {
			if mentionsGoAny(e, seen) {
				return true
			}
		}
	}
	return false
}

// goAnyShared reports a slice or a Map, or a Go type over one.
func goAnyShared(t Type) bool {
	switch under(t).(type) {
	case *Slice, *Map:
		return true
	}
	return false
}

// goAnyForm is why a value of type v does not go into a go.Any, or "".
func (c *checker) goAnyForm(v Type) string {
	if c.holdsResource(v) {
		return fmt.Sprintf("%s holds a resource, which has one owner, and Go would keep it unreleased", v)
	}
	var walk func(t Type, depth int) string
	walk = func(t Type, depth int) string {
		switch t := t.(type) {
		case *Basic:
			if t.Kind == Invalid {
				return "invalid"
			}
			return ""
		case *Named:
			if IsGoAny(t) {
				return ""
			}
			if IsGoError(t) {
				return fmt.Sprintf("%s is Ergo's hold of a Go error, not Go's error; pass e.Text, or the value the call gave", t)
			}
			if e := refElem(t); e != nil {
				return walk(e, depth+1)
			}
			if isHostNamed(t) {
				return ""
			}
			return fmt.Sprintf("%s is an Ergo type, whose Go form is Ergo's own; pass its fields", t)
		case *Pointer:
			if depth > 0 {
				return fmt.Sprintf("%s is a borrow inside a value; pass the borrow alone", t)
			}
			return walk(t.Elem, depth+1)
		case *Slice:
			return walk(t.Elem, depth+1)
		case *Array:
			return walk(t.Elem, depth+1)
		case *Map:
			if why := walk(t.Key, depth+1); why != "" {
				return why
			}
			return walk(t.Elem, depth+1)
		case *Signature:
			return fmt.Sprintf("%s is an Ergo function, whose Go shape is Ergo's calling convention", t)
		case *Tuple:
			return fmt.Sprintf("%s is a tuple; pass its parts", t)
		case *TypeParam:
			return fmt.Sprintf("%s is a type parameter, whose Go form the caller decides", t)
		}
		return fmt.Sprintf("%s has no Go form", t)
	}
	if why := walk(v, 0); why != "" {
		return goAnyText + "; " + why
	}
	return ""
}
