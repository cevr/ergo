package check

import (
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The `as` patterns of #196 (ninth round) in a catch arm: `case T as e:`
// matches T and binds the caught error, with T's type, to e. It replaces
// the old catch binding `catch e { … }`. A match's patterns, `as` included
// at any depth, are match.go's; a catch's arms are stage C's (196r), which
// checks this file's top-level form and rule 6 of the pattern language
// over several types.

// asParts splits `P as name` into P and name; any other pattern is itself
// with no name.
func asParts(e syntax.Expr) (syntax.Expr, *syntax.Ident) {
	if a, ok := e.(*syntax.AsPattern); ok {
		return a.X, a.Name
	}
	return e, nil
}

// asBinding is one `as` name of an arm, with the group that records a read.
type asBinding struct {
	name  *syntax.Ident
	group *switchGroup
}

// declareAs declares one `as` name with type t; `_` binds nothing. A
// placeholder (the name of a pattern typing skipped) binds without the
// binder rules: its pattern carries the one diagnostic (D3).
func (c *checker) declareAs(name *syntax.Ident, t Type, placeholder bool) (asBinding, bool) {
	if name.Name == "_" {
		return asBinding{}, false
	}
	if !placeholder {
		c.localBinder(name)
	}
	g := &switchGroup{}
	v := c.bindLocal(&Var{Name: name.Name, Type: t, Pos: name.Pos(), id: name, group: g, used: true})
	if isInvalid(t) {
		g.used = true // an invalid pattern adds no S110 (D3)
	}
	c.def(name, v)
	c.fn.scope.insert(name.Name, v)
	return asBinding{name: name, group: g}, true
}

// catchAll reports whether a catch arm's lone name binds the whole failure
// (`case err:`, rows.md Catch 7): a lower-case name that names no type.
func (c *checker) catchAll(id *syntax.Ident) bool {
	if r, _ := utf8.DecodeRuneInString(id.Name); !unicode.IsLower(r) {
		return false
	}
	switch o := c.lookupType(id.Name, c.localTypeCtx()).(type) {
	case nil:
		return true
	case *Var:
		// A local of an enclosing scope: the arm still binds the rest, and
		// the reuse is D-D2's S108 at the binder, not a type test. A local
		// hides no package object, so the reference walk agrees.
		return !o.pkg
	case *Method:
		// An operation of the law around the arm (laws.go rule 3): its
		// scope encloses the law's body as an enclosing local's does, so
		// the arm binds the rest and D-D2's S108 at the binder is its one
		// diagnostic. The reference walk reads no law body.
		return true
	case *Builtin, *Func:
		// A prelude function or built-in names no error type: the arm
		// binds the rest, and D-D3's S108 at the name is its one
		// diagnostic. A prelude name hides no package object, so the
		// reference walk agrees.
		return c.isPreludeName(id.Name)
	}
	return false
}

// unusedAs reports each `as` name that its arm never reads (S110), as the
// old switch binding was reported (rows.md, Totality 1).
func (c *checker) unusedAs(list []asBinding) {
	for _, b := range list {
		if !b.group.used {
			c.errorf(b.name, CodeUnused, "the binding %s is read nowhere in its arm; drop `as %s` (rows.md, Totality 1)", b.name.Name, b.name.Name)
		}
	}
}
