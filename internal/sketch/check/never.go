package check

import (
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Never (docs/spec/types.md, Never; owner, 2026-11-13; FKG2) is the
// prelude's union of no variants: the type with no value. No source form
// declares an empty union (a union lists at least one variant, and `{}` is
// an empty record), so the prelude's `type Never {}` is read as one here,
// and only there.
//
// Prior art: Elm's Never (a type whose one constructor needs a Never) and
// never; Rust's Infallible (an enum of no variants) and the empty match on
// it; TypeScript's never, which is a bottom type, as this is not. Gleam
// and ReScript have none and pay with arms that cannot run.

// emptyUnion reports whether n, declared with the record spec, is the
// prelude's Never.
func (c *checker) emptyUnion(n *Named, spec *syntax.StructType) bool {
	return c.pkg.Path == std.PreludePath && n.Obj.Name == "Never" && len(n.TParams) == 0 &&
		(spec.Fields == nil || len(spec.Fields.List) == 0)
}

// IsNever reports whether t is the prelude's Never.
func IsNever(t Type) bool {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Name != "Never" || n.Obj.Pkg == nil || n.Obj.Pkg.Path != std.PreludePath {
		return false
	}
	u, ok := n.under.(*Union)
	return ok && len(u.Variants) == 0
}

// uninhabited reports whether no value of t can be made: t is Never, a
// record with a field of such a type, or a tuple with such an element. A
// union is not: its other variants may have values (a match leaves out
// the variants that have none, Totality). A type parameter is not, since
// its instance may have values; a slice, map, option of Never and a
// function type have a value each (empty, None, a function).
func (c *checker) uninhabited(t Type) bool {
	return uninhabitedIn(t, map[Type]bool{})
}

// mayHoldNever reports whether a variant of s may have no value: a
// variant of the origin holds a Never, or a type argument names one. Only
// then are the variants substituted to ask (switchUnion: a switch over n
// instances of a k-variant union does O(n + k) work, and a default-only
// switch instantiates no variant; the origin's answer is computed once).
func (s *switchUnion) mayHoldNever() bool {
	c := s.c
	if c.neverOrigins == nil {
		c.neverOrigins = map[*Union]bool{}
	}
	held, ok := c.neverOrigins[s.orig]
	if !ok {
		for _, v := range s.orig.Variants {
			if c.uninhabited(v) {
				held = true
				break
			}
		}
		c.neverOrigins[s.orig] = held
	}
	if held {
		return true
	}
	for _, tp := range s.tps {
		if namesNever(s.m[tp], 0) {
			return true
		}
	}
	return false
}

// namesNever reports whether t is or holds a Never as far as its written
// form shows: Never itself, a record or tuple with no value, or a type
// argument of t that names one. It substitutes nothing.
func namesNever(t Type, depth int) bool {
	if t == nil || depth > 8 {
		return false
	}
	if IsNever(t) {
		return true
	}
	switch t := t.(type) {
	case *Named:
		if t.Orig == nil {
			return uninhabitedIn(t, map[Type]bool{})
		}
		for _, a := range t.TArgs {
			if namesNever(a, depth+1) {
				return true
			}
		}
	case *Tuple:
		for _, e := range t.Types {
			if namesNever(e, depth+1) {
				return true
			}
		}
	}
	return false
}

func uninhabitedIn(t Type, seen map[Type]bool) bool {
	if IsNever(t) {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch ut := under(t).(type) {
	case *Struct:
		for _, f := range ut.Fields {
			if uninhabitedIn(f.Type, seen) {
				return true
			}
		}
	case *Tuple:
		for _, e := range ut.Types {
			if uninhabitedIn(e, seen) {
				return true
			}
		}
	}
	return false
}
