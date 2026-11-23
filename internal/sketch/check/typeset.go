package check

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Term is one term of an operation type's type-set member (types.md,
// Generics 2): `~T` (Tilde) admits every type whose underlying type is T,
// a bare `T` admits T alone, and the term `comparable` (Type is the
// predeclared comparable constraint) admits the comparable types.
type Term struct {
	Tilde bool
	Type  Type
}

func (t *Term) String() string {
	if t.Tilde {
		return "~" + t.Type.String()
	}
	return t.Type.String()
}

// isComparableTerm reports the term `comparable`.
func (t *Term) isComparableTerm() bool {
	it, ok := t.Type.(*Interface)
	return ok && it.Set == SetComparable
}

// termsText spells a type set for a message: its terms joined by `|`, the
// first 8 of a longer set and `, …`.
func termsText(terms []*Term) string {
	var b strings.Builder
	for i, t := range terms {
		if i == 8 {
			b.WriteString(", …")
			break
		}
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(t.String())
	}
	return b.String()
}

// typeTerms resolves the type-set member of an operation type. A `~T` term
// needs T to be its own underlying type (a basic type), as in Go; a bare
// term is a type that is not an operation type, or the word comparable.
// A refined type is no term: a generic body could leave its where through
// + (Refinements 7).
func (c *checker) typeTerms(spec []*syntax.TypeTerm, ctx *typeCtx) []*Term {
	terms := make([]*Term, 0, len(spec))
	for _, s := range spec {
		c.inConstraint++
		t := c.typExpr(s.Type, ctx)
		c.inConstraint--
		if isInvalid(t) {
			continue
		}
		term := &Term{Tilde: s.Tilde.IsValid(), Type: t}
		if namesTypeParam(t) {
			// A set is the same at every instance of its type, so it
			// lowers to one Go constraint (lower/ir_typeset.go).
			c.errorf(s.Type, CodeType, "a type-set term names no type parameter; %s does (types.md, Generics 2)", t)
			continue
		}
		switch {
		case term.isComparableTerm():
			if term.Tilde {
				c.errorf(s, CodeType, "comparable is a term of its own; write comparable without ~ (types.md, Generics 2)")
				continue
			}
		case asInterface(t) != nil:
			c.errorf(s.Type, CodeType, "a type-set term is a type with values; %s is an operation type: include it with ..%s (types.md, Generics 2)", t, t)
			continue
		case RefinementOf(t) != nil:
			c.errorf(s.Type, CodeType, "a refined type is no type-set term: a generic body could leave its where through + (types.md, Refinements 7)")
			continue
		case term.Tilde:
			if _, basic := t.(*Basic); !basic {
				c.errorf(s, CodeType, "~%s needs %s to be its own underlying type; write ~%s (types.md, Generics 2)", t, t, under(t))
				continue
			}
		}
		terms = append(terms, term)
	}
	return terms
}

// termsOf is the type set a constraint names: its type-set member, the
// one term comparable for the predeclared comparable, or nil when it
// admits every type.
func termsOf(it *Interface) []*Term {
	if it == nil {
		return nil
	}
	if it.Terms != nil {
		return it.Terms
	}
	if it.Set == SetComparable {
		return []*Term{{Type: universeComparable()}}
	}
	return nil
}

// universeComparable is the predeclared constraint comparable.
func universeComparable() Type {
	tn, _ := universe().names["comparable"].(*TypeName)
	return tn.Type
}

// paramTerms is the type set of the type parameter t's constraint, nil
// for a type that is no type parameter or a constraint with no set.
func paramTerms(t Type) []*Term {
	tp, ok := t.(*TypeParam)
	if !ok {
		return nil
	}
	return termsOf(asInterface(tp.Constraint))
}

// everyTerm reports whether t is a type parameter with a type set whose
// every term's types satisfy has (Go's rule: an operator applies to a
// type parameter when every type of its set has it). The term comparable
// gives == and != only, which has never asks of it.
func everyTerm(t Type, has func(Type) bool) bool {
	terms := paramTerms(t)
	if terms == nil {
		return false
	}
	for _, term := range terms {
		if term.isComparableTerm() || !has(term.Type) {
			return false
		}
	}
	return true
}

// termAdmits reports whether the term admits the type t, which is no type
// parameter.
func (c *checker) termAdmits(term *Term, t Type) bool {
	switch {
	case term.isComparableTerm():
		return comparable(t)
	case term.Tilde:
		return RefinementOf(t) == nil && c.identical(under(t), term.Type)
	}
	return c.identical(t, term.Type)
}

// termCovers reports whether every type the term a admits, b admits too:
// a type parameter is in a set when each term of its own set is covered.
func (c *checker) termCovers(b, a *Term) bool {
	switch {
	case b.isComparableTerm():
		return a.isComparableTerm() || comparable(a.Type)
	case a.isComparableTerm():
		return false
	case b.Tilde:
		if a.Tilde {
			return c.identical(a.Type, b.Type)
		}
		return c.termAdmits(b, a.Type)
	case a.Tilde:
		return false
	}
	return c.identical(a.Type, b.Type)
}

// inTypeSet reports whether t lies in the type set terms: a type
// parameter when its own set lies within it, any other type when one term
// admits it.
func (c *checker) inTypeSet(t Type, terms []*Term) bool {
	if _, ok := t.(*TypeParam); ok {
		own := paramTerms(t)
		if own == nil {
			return false
		}
		for _, a := range own {
			covered := false
			for _, b := range terms {
				if c.termCovers(b, a) {
					covered = true
					break
				}
			}
			if !covered {
				return false
			}
		}
		return true
	}
	for _, term := range terms {
		if c.termAdmits(term, t) {
			return true
		}
	}
	return false
}

// notInSetText is the reason a type argument is not in a constraint's
// type set (types.md, Generics 2).
func notInSetText(t, cons Type, it *Interface) string {
	name := cons.String()
	if n, ok := cons.(*Named); ok && n.Obj != nil {
		name = n.Obj.Name
	}
	return fmt.Sprintf("%s is not in %s's type set, %s", t, name, termsText(it.Terms))
}

// typeSetOnly reports whether t is a constraint only: an operation type
// with a type-set member, which no value has as its type.
func typeSetOnly(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	// Read on the origin, never forcing an instance's underlying type: an
	// instance made while its origin is being declared would keep an
	// invalid one. The terms name no type parameter, so every instance
	// has its origin's.
	it, ok := n.origin().under.(*Interface)
	return ok && it.Terms != nil
}

// genericOpOnly reports whether t is a constraint only by a generic
// operation (types.md, Interface assignability, Generic operations): no
// Go interface or function field takes a type parameter, so no value has
// it as its type. Read on the origin, as typeSetOnly reads.
func genericOpOnly(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	it, ok := n.origin().under.(*Interface)
	return ok && it.Generic
}
