package check

import "ergo.local/prototype/internal/sketch/syntax"

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
	refused := false
	if !placeholder {
		refused = c.localBinder(name)
	}
	g := &switchGroup{}
	v := c.bindLocal(&Var{Name: name.Name, Type: t, Pos: name.Pos(), id: name, group: g, used: true, refused: refused})
	if isInvalid(t) {
		g.used = true // an invalid pattern adds no S110 (D3)
	}
	c.def(name, v)
	c.fn.scope.insert(name.Name, v)
	return asBinding{name: name, group: g}, true
}

// catchAll reports whether a catch arm's lone name binds the whole failure
// (`case err:`, rows.md Catch 7).
func (c *checker) catchAll(id *syntax.Ident) bool {
	// A type parameter first, as in lookupType; then the name as a pattern
	// sees it, so a local hides a package type here (types.md, Scope 1).
	if tp := c.lookupType(id.Name, c.localTypeCtx()); tp != nil {
		if tn, ok := tp.(*TypeName); ok {
			if _, isParam := tn.Type.(*TypeParam); isParam {
				return catchAllObject(tp)
			}
		}
	}
	return catchAllObject(c.lookupPast(id.Name))
}

// catchAllObject is catchAll's rule, shared with the reference walk
// (refWalker.catchPattern; fuzz-17d6): the lone name resolves to o where
// the arm stands. The arms are match arms over the failure (rows.md, Catch
// 7), so Totality 1 decides (patternNameTests), whatever the name's case:
// a type, a type parameter, a constant or a capability tests; any other
// name binds the rest. The binder rules then judge it: an enclosing local
// or a law's operation is D-D2's S108, a prelude name D-D3's, and a
// package function, an imported package or a package value Totality 1's
// (refusedRest); each still binds.
func catchAllObject(o Object) bool {
	return !patternNameTests(o, false, false, false)
}

// refusedRest reports Totality 1's S108 at a catch arm's rest name that
// names a package function, an imported package or a package value
// (rows.md Totality 6: the rule holds in catch arms), and reports whether
// it did. The name still binds, with no type, so its reads add nothing
// (D3). A prelude name is D-D3's, at declareAs.
func (c *checker) refusedRest(id *syntax.Ident) bool {
	if id.Name == "_" || c.isPreludeName(id.Name) {
		return false
	}
	o := c.lookupPast(id.Name)
	switch o := o.(type) {
	case *Func, *PkgName:
		if p, ok := o.(*PkgName); ok {
			p.used = true // The refused occurrence must not add an unused import.
		}
	case *Var:
		if !o.pkg {
			return false
		}
	default:
		return false
	}
	c.errorf(id, CodeType, "a pattern name binds; %s already names %s: compare with if, or choose another name (rows.md, Totality 1)", id.Name, objectNoun(o))
	c.refuseBinder(id.Pos())
	return true
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
