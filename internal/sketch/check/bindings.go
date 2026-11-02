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

// predeclaredNames are the names the decided language predeclares that the
// checker spells as syntax (`Map[K, V]`). Every other predeclared name is
// found in the scopes above the package: the universe and the prelude's
// exported names.
var predeclaredNames = map[string]bool{"Map": true}

// notPreludeNames are universe names outside D-D3's decided list: `error`
// is only the head of an error row (D-E2), `uintptr`, `complex64` and
// `complex128` are not v1 types, `with` is a keyword and `go` names the
// extern go package. Each keeps the treatment it had before D-D3.
var notPreludeNames = map[string]bool{"error": true, "uintptr": true, "complex64": true, "complex128": true, "with": true, "go": true}

// isPreludeName reports whether name is predeclared for this package: a
// universe name of D-D3's list, an exported prelude name, or one of
// predeclaredNames. The prelude package itself sees only the universe
// above it, so its own declarations are not refused.
func (c *checker) isPreludeName(name string) bool {
	if name == "_" || notPreludeNames[name] {
		return false
	}
	if predeclaredNames[name] {
		return true
	}
	for sc := c.pkg.Scope.parent; sc != nil; sc = sc.parent {
		if _, ok := sc.names[name]; ok {
			return true
		}
	}
	return false
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
	return c.preludeNameAt(id.Name, id.Pos())
}

// preludeNameAt is preludeName for a binder known by its name and
// position. A binder that already has its diagnostic counts as refused
// and adds none.
func (c *checker) preludeNameAt(name string, pos syntax.Pos) bool {
	if c.binderRefused(pos) {
		return true
	}
	if !c.isPreludeName(name) {
		return false
	}
	c.errorAt(pos, pos+syntax.Pos(len(name)), CodeType, "%s is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)", name)
	c.refuseBinder(pos)
	return true
}

// shadowed reports a local binder that reuses a name bound in an enclosing
// scope of the same function (D-D2) and returns true. sc is the scope the
// binder enters; the scopes searched are its parents up to, and not
// including, the package scope. The outer binding counts as read, so the
// refusal adds no S110 beside it (D3); the inner binder still binds.
func (c *checker) shadowed(name string, pos syntax.Pos, sc *Scope) bool {
	if name == "_" || sc == nil {
		return false
	}
	for p := sc.parent; p != nil && p != c.pkg.Scope; p = p.parent {
		prev, ok := p.names[name]
		if !ok {
			continue
		}
		end := pos + syntax.Pos(len(name))
		switch prev := prev.(type) {
		case *capBinding:
			c.errorAt(pos, end, CodeType, "%s is in this function's capability row, so an inner binder cannot reuse the name; choose another name (no shadowing, #196 196v rule 2)", name)
		case *Var:
			prev.used = true
			if prev.group != nil {
				prev.group.used = true
			}
			what := "a local"
			if prev.param {
				what = "a parameter"
			}
			c.errorAt(pos, end, CodeType, "%s is already %s of an enclosing scope in this function; choose another name (no shadowing, #196 196v rule 2)", name, what)
		default:
			c.errorAt(pos, end, CodeType, "%s is already bound in an enclosing scope of this function; choose another name (no shadowing, #196 196v rule 2)", name)
		}
		return true
	}
	return false
}

// localBinder runs the binder rules for a local about to bind name at pos
// in the current scope: a prelude name (D-D3) first, else shadowing
// (D-D2). It reports at most one diagnostic.
func (c *checker) localBinder(name string, pos syntax.Pos) {
	if c.fn == nil || name == "_" {
		return
	}
	if c.preludeNameAt(name, pos) {
		return
	}
	c.shadowed(name, pos, c.fn.scope)
}

// untypedLet reports `let x` with neither a type nor a value (D-D1): x
// would have no type. The name binds with the invalid type, so its uses
// add nothing (D3).
func (c *checker) untypedLet(id *syntax.Ident) {
	c.errorf(id, CodeType, "let %s needs a type or a value: write let %s T or let %s = e (#196, 196v)", id.Name, id.Name, id.Name)
}

// mutPackageLet reports a package-level `let mut` at its `mut` (D-D1).
func (c *checker) mutPackageLet(d *syntax.VarDecl) {
	c.errorAt(d.Mut, d.Mut+3, CodeType, "a package-level let cannot be mut: Ergo has no mutable package state; pass the value as a parameter, or keep it in a let mut local (#196, 196v)")
}

// mutPattern reports `let mut` over a pattern at its `mut` (D-D1): a
// pattern's bindings are immutable.
func (c *checker) mutPattern(d *syntax.VarDecl) {
	c.errorAt(d.Mut, d.Mut+3, CodeType, "let mut binds names, and a pattern's bindings are immutable: write let mut v = e, then destructure v with let or match (#196, 196v)")
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
		c.errorAt(v.Pos, v.Pos+syntax.Pos(len(v.Name)), CodeUnused, "%s is never reassigned; write let %s (#196, 196v)", v.Name, v.Name)
	}
}
