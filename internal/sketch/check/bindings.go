package check

import "ergo.local/prototype/internal/sketch/syntax"

// Bindings (check-v1 stage D1; #196, 196v amended and the eighth round):
//
//   - D-D1: `=` and `op=` reassign only a `let mut` local, directly or
//     through fields and indexes (Q2); every other binder is immutable; a
//     package-level `let mut` is S108; a `let mut` that is never reassigned
//     is S110 (Q3); a `let` with neither a type nor a value is S108.
//   - D-D2: a binder may not reuse a name bound in an enclosing scope of the
//     same function, function literals included (S108 at the inner binder).
//     Sibling scopes may reuse a name, and a local may reuse a package-level
//     name that is not a prelude name.
//   - D-D3: a prelude name cannot be redeclared at any scope (S108 at the
//     name). The refused binder still binds for the rest of its scope, so
//     its uses add no diagnostic.
//
// Each check runs where the binder binds, so every binder kind goes through
// one of three functions: preludeName (every declaration), shadowed (every
// local binder) and the mutability rules of lhs.

// isPreludeName reports whether name is predeclared for this package
// (predeclared.go): a universe name of D-D3's list, a name of the prelude
// scope, or one of predeclaredNames. The prelude package itself sees only
// the universe above it, so its own declarations are not refused.
func (c *checker) isPreludeName(name string) bool {
	return predeclaredAbove(name, c.pkg.Scope.parent)
}

// preludeTypeName reports a prelude name that names a type: a type of the
// universe or the prelude, or `Map`, which the syntax spells (Parser rule
// 8). A package-level type declaration may take one and hides it inside
// its package, as Go's package scope hides the universe (grammar.md,
// Reserved names 2; L6, owner q30, which reversed the eighth round's item
// 3 for package types). Every other declaration of a prelude name, a
// local's included, stays D-D3's S108.
func (c *checker) preludeTypeName(name string) bool {
	if !c.isPreludeName(name) {
		return false
	}
	if predeclaredNames[name] {
		return true
	}
	o, _ := c.pkg.Scope.parent.lookup(name)
	_, ok := o.(*TypeName)
	return ok
}

// declaredMap is the package's own type named Map, if it declares one
// (preludeTypeName): there `Map[K, V]` names that type, not the map type.
func (c *checker) declaredMap() *TypeName {
	tn, _ := c.pkg.Scope.names["Map"].(*TypeName)
	return tn
}

// mapAsDeclared is mapInstance where the package declares its own Map,
// and nil where `Map[K, V]` is the map type (typeWalker.maps).
func (c *checker) mapAsDeclared(x *syntax.MapType) syntax.Expr {
	if c.declaredMap() == nil {
		return nil
	}
	return c.mapInstance(x)
}

// mapInstance is `Map[K, V]` as the instance of the package's own Map
// (declaredMap) that it spells there: the parser reads the name as the map
// type (Parser rule 8). One node stands for each MapType, so every pass
// that resolves it records the same name.
func (c *checker) mapInstance(x *syntax.MapType) *syntax.IndexExpr {
	if ix, ok := c.mapInstances[x]; ok {
		return ix
	}
	name := made(c, &syntax.Ident{NamePos: x.Map, Name: "Map"}, syntax.Node(x))
	ix := made(c, &syntax.IndexExpr{X: name, Lbrack: x.Lbrack, Indices: []syntax.Expr{x.Key, x.Value}, Rbrack: x.Rbrack}, syntax.Node(x))
	if c.mapInstances == nil {
		c.mapInstances = map[*syntax.MapType]*syntax.IndexExpr{}
	}
	c.mapInstances[x] = ix
	return ix
}

// paramBinder applies the binder rules of a declared function's parameter
// list, an extern's included (D-D3 "parameter"): a name the list already
// holds is S108 and binds nothing; a prelude name is S108 and still binds.
// seen holds the names of the list so far. It reports whether the
// parameter binds and whether D-D3 refused it.
func (c *checker) paramBinder(f *Func, p *Var, seen map[string]bool) (bind, refused bool) {
	if p.Name == "_" {
		return false, false
	}
	if seen[p.Name] {
		c.errorf(p.id, CodeType, "%s is already a parameter of %s", p.Name, f.Name)
		return false, false
	}
	seen[p.Name] = true
	return true, c.preludeName(p.id)
}

// binderKey names a binder by its file and position.
type binderKey struct {
	file int
	pos  syntax.Pos
}

func (c *checker) binderKey(pos syntax.Pos) binderKey {
	k := binderKey{pos: pos}
	if c.cur != nil {
		k.file = c.cur.index
	}
	return k
}

// refuseBinder records that the binder at pos has its diagnostic, so the
// binder rules add none (D3). Only the reporting pass records.
func (c *checker) refuseBinder(pos syntax.Pos) {
	if c.silent == 0 {
		c.refusedBinders[c.binderKey(pos)] = true
	}
}

// binderRefused reports whether the binder at pos already has its
// diagnostic.
func (c *checker) binderRefused(pos syntax.Pos) bool {
	return c.silent == 0 && c.refusedBinders[c.binderKey(pos)]
}

// abovePackage reports whether sc is a scope above the package: the
// prelude's or the universe.
func (c *checker) abovePackage(sc *Scope) bool {
	for p := c.pkg.Scope.parent; p != nil; p = p.parent {
		if p == sc {
			return true
		}
	}
	return false
}

// preludeName reports a declaration of a prelude name (D-D3) and returns
// true. The caller binds the name anyway.
func (c *checker) preludeName(id *syntax.Ident) bool {
	if id == nil {
		return false
	}
	// A binder that already has its diagnostic counts as refused and adds
	// none.
	if c.binderRefused(id.Pos()) {
		return true
	}
	if !c.isPreludeName(id.Name) {
		return false
	}
	c.errorf(id, CodeType, "%s is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)", id.Name)
	c.refuseBinder(id.Pos())
	return true
}

// shadowed reports a local binder that reuses a name bound in an enclosing
// scope of the same function (D-D2) and returns true. sc is the scope the
// binder enters; the scopes searched are its parents up to, and not
// including, the package scope. The outer binding counts as read, so the
// refusal adds no S110 beside it (D3); the inner binder still binds. A
// refused binder is never a binding a later binder shadows (P2).
func (c *checker) shadowed(id *syntax.Ident, sc *Scope) bool {
	name := id.Name
	if name == "_" || sc == nil {
		return false
	}
	for p := sc.parent; p != nil && p != c.pkg.Scope; p = p.parent {
		prev, ok := p.names[name]
		if !ok || isRefusedBinder(prev) {
			continue
		}
		switch prev := prev.(type) {
		case *capBinding:
			c.errorf(id, CodeType, "%s is in this function's capability row, so an inner binder cannot reuse the name; choose another name (no shadowing, #196 196v rule 2)", name)
		case *Var:
			prev.used = true
			if prev.group != nil {
				prev.group.used = true
			}
			what := "a local"
			if prev.param {
				what = "a parameter"
			}
			c.errorf(id, CodeType, "%s is already %s of an enclosing scope in this function; choose another name (no shadowing, #196 196v rule 2)", name, what)
		default:
			c.errorf(id, CodeType, "%s is already bound in an enclosing scope of this function; choose another name (no shadowing, #196 196v rule 2)", name)
		}
		return true
	}
	return false
}

// localBinder runs the binder rules for a local about to bind the name id
// declares in the current scope: a prelude name (D-D3) first, else
// shadowing (D-D2). It reports at most one diagnostic, and returns true
// when the binder is refused (its Var carries the mark).
func (c *checker) localBinder(id *syntax.Ident) (refused bool) {
	if c.fn == nil || id.Name == "_" {
		return false
	}
	if c.preludeName(id) {
		return true
	}
	c.shadowed(id, c.fn.scope)
	return false
}

// Refused binders (rows.md, Totality 6; diagnostics.md, D3): a refused
// type test's placeholder (declarePlaceholder) and a binder with its own
// diagnostic, D-D3's or a pattern's (refuseBinder), carry Var.refused. A
// refused binder changes no other verdict: a read of the name in a value
// position finds it (P1, lookup), and every other lookup sees through it
// to what the name means without it (P2, lookupPast): the pattern
// classifier, the shadow rule and type positions. The reference walk marks
// the same binders (localBinding.refused) and sees through them the same
// way (P3).

// isRefusedBinder reports whether o is a refused binder.
func isRefusedBinder(o Object) bool {
	v, ok := o.(*Var)
	return ok && v.refused
}

// scopeLookupPast is Scope.lookup that sees through refused binders.
func scopeLookupPast(s *Scope, name string) (Object, *Scope) {
	for sc := s; sc != nil; sc = sc.parent {
		if o, ok := sc.names[name]; ok && !isRefusedBinder(o) {
			return o, sc
		}
	}
	return nil, nil
}

// scopeLookupType is scopeLookupPast for a type position: it also sees
// through a local value binder to the type of its name in an outer scope
// (types.md, Scope 1; L5). A name only a local binds resolves to that
// local, so the position reports it as no type.
func scopeLookupType(s *Scope, name string) (Object, *Scope) {
	var local Object
	var localAt *Scope
	for sc := s; sc != nil; sc = sc.parent {
		o, ok := sc.names[name]
		if !ok || isRefusedBinder(o) {
			continue
		}
		if v, isVar := o.(*Var); isVar && !v.pkg {
			if local == nil {
				local, localAt = o, sc
			}
			continue
		}
		return o, sc
	}
	return local, localAt
}

// lookupPast is lookup outside a value position: it sees through refused
// binders (P2).
func (c *checker) lookupPast(name string) Object { return c.lookupWith(name, scopeLookupPast) }

// untypedLet reports `let x` with neither a type nor a value (D-D1): x
// would have no type. The name binds with the invalid type, so its uses
// add nothing (D3).
func (c *checker) untypedLet(id *syntax.Ident) {
	c.errorf(id, CodeType, "let %s needs a type or a value: write let %s T or let %s = e (#196, 196v)", id.Name, id.Name, id.Name)
}

// mutPackageLet reports a package-level `let mut` at its `mut` (D-D1).
func (c *checker) mutPackageLet(d *syntax.VarDecl) {
	c.errorIn(d, d.Mut, d.Mut+3, CodeType, "a package-level let cannot be mut: Ergo has no mutable package state; pass the value as a parameter, or keep it in a let mut local (#196, 196v)")
}

// mutPattern reports `let mut` over a pattern at its `mut` (D-D1): a
// pattern's bindings are immutable.
func (c *checker) mutPattern(d *syntax.VarDecl) {
	c.errorIn(d, d.Mut, d.Mut+3, CodeType, "let mut binds names, and a pattern's bindings are immutable: write let mut v = e, then destructure v with let or match (#196, 196v)")
}

// rootKind is what an assignment target or a borrow is rooted at.
type rootKind int

const (
	rootName    rootKind = iota // an identifier, through fields and indexes
	rootPointer                 // a write through a pointer
	rootOther                   // a call's result, a literal, a conversion
)

// assignRoot returns the identifier an assignment target or a borrow is
// rooted at, through parentheses, fields and indexes (rootName). A target
// that goes through a pointer is rootPointer: a write through a pointer
// changes the pointee, not the value the root names, and taking that
// pointer was the borrow the rule checks (D-D1; pointers are #222's). A
// target rooted at any other expression is rootOther (Q2). It reads the
// types typing recorded, so the caller types x first; a pass that records
// no types (a silent one) walks to the identifier and reports nothing.
func (c *checker) assignRoot(x syntax.Expr) (*syntax.Ident, rootKind) {
	throughPointer := func(e syntax.Expr) bool {
		_, ok := c.pkg.Info.Types[e].(*Pointer)
		return ok
	}
	for {
		switch e := x.(type) {
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.SelectorExpr:
			if throughPointer(e.X) {
				return nil, rootPointer
			}
			x = e.X
		case *syntax.IndexExpr:
			if throughPointer(e.X) {
				return nil, rootPointer
			}
			x = e.X
		case *syntax.StarExpr:
			return nil, rootPointer
		case *syntax.Ident:
			return e, rootName
		default:
			return nil, rootOther
		}
	}
}

// mutableRoot checks that an assignment target is rooted at a `let mut`
// local (D-D1, Q2) and records the reassignment (Q3). It reports S108 for
// any other root it can name: a `let`, a parameter, a pattern, `as`, `case
// err:` or loop binding, or a package-level `let`. A refused package-level
// `let mut` accepts assignments silently: its declaration carries the one
// diagnostic (D3). A root that is not a variable (a package name, an
// unknown name, an invalid local) is left to typing. The verdict never
// changes how the assignment is typed, so a silent pass and the reporting
// pass type it alike.
func (c *checker) mutableRoot(x syntax.Expr) {
	c.checkRoot(x, x, "")
}

// borrowRoot checks a borrow of x: `&x`, or the implicit one of a call to
// a function whose first parameter is *T (fn names it). A borrow can
// change the value, so x must be rooted at a `let mut` local, which the
// borrow counts as reassigned (D-D1, Q3). at is where to report.
func (c *checker) borrowRoot(at syntax.Node, x syntax.Expr, fn string) {
	c.checkRoot(at, x, fn)
}

// markReassigned records an assignment to a target refused for another
// reason: its let mut root counts as reassigned, with no diagnostic.
func (c *checker) markReassigned(x syntax.Expr) {
	if id, _ := c.assignRoot(x); id != nil {
		if v, ok := c.lookup(id.Name).(*Var); ok && v.mut {
			v.reassigned = true
		}
	}
}

func (c *checker) checkRoot(at syntax.Node, x syntax.Expr, borrow string) {
	id, kind := c.assignRoot(x)
	if kind == rootOther && borrow == "" {
		// `xs()[0] = 5`, `[]int{1, 2}[0] = 3`: the write is lost or
		// changes a value no let mut names (Q2). A borrow of a literal
		// (`&R{N: 1}`) allocates, and typing judges a borrow's operand.
		c.errorf(at, CodeType, "an assignment target is rooted at a let mut local, directly or through fields and indexes; this one is rooted at %s (#196, 196v)", rootText(x))
		return
	}
	if id == nil || id.Name == "_" {
		return
	}
	v, ok := c.lookup(id.Name).(*Var)
	if !ok {
		return
	}
	if v.mut {
		v.reassigned = true
		return
	}
	if v.pkg {
		// A package-level let's type may not be resolved yet; it is
		// immutable whatever its type.
		c.errorf(at, CodeType, "the package-level let %s is immutable; Ergo has no mutable package state (#196, 196v)", id.Name)
		return
	}
	if v.Type == nil || isInvalid(v.Type) {
		return
	}
	whole := id == unparen(x)
	how := "cannot be reassigned"
	switch {
	case borrow == "&":
		how = "cannot be borrowed with &, which could change it"
	case borrow != "":
		how = "cannot be passed to " + borrow + ", which takes a pointer and could change it"
	case !whole:
		how = "is immutable, and so are its fields and elements"
	}
	switch {
	case v.param:
		c.errorf(at, CodeType, "the parameter %s %s; copy it into a local: let mut %s = %s (#196, 196v)", id.Name, how, renamed(id.Name), id.Name)
	case v.let:
		c.errorf(at, CodeType, "%s %s; declare %s with let mut (#196, 196v)", id.Name, how, id.Name)
	default:
		c.errorf(at, CodeType, "the binding %s %s; copy it into a local: let mut %s = %s (#196, 196v)", id.Name, how, renamed(id.Name), id.Name)
	}
}

// rootText names the expression a rootOther target starts from.
func rootText(x syntax.Expr) string {
	for {
		switch e := x.(type) {
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.SelectorExpr:
			x = e.X
		case *syntax.IndexExpr:
			x = e.X
		case *syntax.CallExpr:
			return "a call's result"
		case *syntax.CompositeLit:
			return "a literal"
		default:
			return "an expression that is not a local"
		}
	}
}

// renamed suggests a fresh local name for a copy of an immutable binder:
// the copy cannot reuse the name (no shadowing, D-D2).
func renamed(name string) string { return name + "2" }

// reportNeverReassigned reports each `let mut` local of fc that is read but
// never reassigned (S110, Q3). An unread one already has its unused S110,
// and one of an invalid type has its own diagnostic (D3).
func (c *checker) reportNeverReassigned(fc *funcCtx) {
	for _, v := range fc.locals {
		if !v.mut || v.reassigned || !v.used || v.Name == "_" || isInvalid(v.Type) || v.undiagnosed {
			continue
		}
		c.errorf(v.id, CodeUnused, "%s is never reassigned; write let %s (#196, 196v)", v.Name, v.Name)
	}
}
