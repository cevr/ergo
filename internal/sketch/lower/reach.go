package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Frontend reachability (binsize BS5; plan design (a)). A build lowers a
// declaration of a sourced package (the root package and the local
// packages it imports) only when it is reachable from the build's entry:
// main for ergo build and ergo run. The test build of ergo test lowers
// every declaration of the root package, since a law runs over the
// providers of its operation type, which no syntax names, and reaches
// into the imported packages from there. A package let that nothing
// reachable reads is not lowered, so its initializer does not run (owner
// question 1); a machine type that nothing reachable names computes no
// initial value and registers no graph. The checker still checks
// every declaration: reachability changes what is lowered, never what is
// checked.
//
// The edges are read from the checker's Info over the syntax of each
// reached declaration: every identifier's Uses object (a call, a
// function value, a let read, a type, an embedded context), every
// expression's type, the object a pkg.Name selector names, and every
// with's contexts. A reached type
// reaches each function of its package whose first parameter is that type
// (or a pointer to it): its dot calls and Go methods, the operations it
// provides to an operation type or a witness, and the context-binding
// protocol's functions. A type's inline variants share
// its declaration. The set may hold more than the lowering uses, never
// less: a declaration the set missed would have no Go declaration, and
// the Go build would fail on its name (the property type-checks every
// program it lowers).
//
// reachLowering selects the lowering: "" (reach, the default) or "all",
// the reference that lowers every declaration of the root package and
// every let and machine of a sourced library, set by a test or by a build
// with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.reachLowering=all.
// TestReachLowersNoUnreachableDeclaration is its property; the examples
// give the same output under both.
//
// Prior art: Gleam and ReScript emit every module function and leave dead
// code to the host (the JS bundler's tree shaking, the BEAM keeps all);
// Elm's compiler emits only what main reaches, across packages, which is
// the model here; Go's linker removes unreached functions but runs every
// package initializer, which is why a let and a registration must be cut
// before Go sees them.
var reachLowering string

// declInfo is a top-level declaration of a sourced package and its package.
type declInfo struct {
	decl syntax.Decl
	pkg  *check.Package
}

// reach is the reachability of one lowering.
type reach struct {
	l       *lowerer
	byObj   map[check.Object]declInfo
	reached map[syntax.Decl]bool
	types   map[*check.TypeName]bool
	work    []declInfo
	// firsts are the functions of each package by the type their first
	// parameter names, built once per package.
	firsts map[*check.Package]map[*check.TypeName][]*check.Func
}

// computeReach sets l.reached, the declarations of the sourced packages
// that the entry reaches; it leaves it nil, which lowers every
// declaration, under reachLowering "all" and for a library-shaped lowering
// (noEntry), which has no entry.
func (l *lowerer) computeReach() {
	if reachLowering == "all" || l.noEntry {
		return
	}
	r := &reach{l: l, byObj: map[check.Object]declInfo{}, reached: map[syntax.Decl]bool{},
		types: map[*check.TypeName]bool{}, firsts: map[*check.Package]map[*check.TypeName][]*check.Func{}}
	for _, p := range l.sourcedPkgs() {
		for _, s := range l.srcs {
			if s.pkg != p {
				continue
			}
			for _, d := range s.file.Decls {
				di := declInfo{decl: d, pkg: p}
				syntax.Inspect(d, func(n syntax.Node) bool {
					if id, ok := n.(*syntax.Ident); ok {
						if o := p.Info.Defs[id]; o != nil {
							if _, seen := r.byObj[o]; !seen {
								r.byObj[o] = di
							}
						}
					}
					return true
				})
				if p == l.pkg && l.tests != nil {
					r.add(di) // the test build: every root declaration
				}
			}
		}
	}
	if main := l.mainFunc(); main != nil && l.tests == nil {
		r.object(main)
	}
	for len(r.work) > 0 {
		di := r.work[len(r.work)-1]
		r.work = r.work[:len(r.work)-1]
		r.visit(di)
	}
	l.reached = r.reached
}

// add marks a declaration reached and queues its syntax.
func (r *reach) add(di declInfo) {
	if r.reached[di.decl] {
		return
	}
	r.reached[di.decl] = true
	r.work = append(r.work, di)
}

// visit follows the edges of a reached declaration's syntax: each
// identifier's Uses object (a call, a function value, a let read, a type,
// a context named by a with or embedded in another) and each with's
// contexts (a with names its context through Info.Withs, not Uses).
func (r *reach) visit(di declInfo) {
	info := di.pkg.Info
	syntax.Inspect(di.decl, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Ident:
			r.object(info.Uses[n])
		case *syntax.SelectorExpr:
			// pkg.Name: the checker records no Use of the selected name;
			// the package's exported object of that name is the one, as
			// the lowering reads it (selector).
			if id, ok := n.X.(*syntax.Ident); ok {
				if pn, ok := info.Uses[id].(*check.PkgName); ok {
					for _, o := range pn.Pkg.Exported() {
						if objectName(o) == n.Sel.Name {
							r.object(o)
						}
					}
				}
			}
		case *syntax.CallExpr:
			for w := info.Withs[n]; w != nil; w = w.Next {
				for _, ctx := range w.Contexts {
					r.object(ctx)
				}
			}
		}
		if x, ok := n.(syntax.Expr); ok {
			if t := info.Types[x]; t != nil {
				r.typ(t)
			}
		}
		return true
	})
}

// typ reaches every named type that t names. An expression's type is an
// edge of its own: the name of a package type that shadows a prelude one
// (Map[K, V]{…}) has the prelude's Uses entry, and only the expression's
// type names the package's.
func (r *reach) typ(t check.Type) {
	walkType(t, func(t check.Type, _ bool) bool {
		if n, ok := t.(*check.Named); ok {
			r.typeName(n.Obj)
		}
		return false
	})
}

// objectName is the declared name of a package-level object, "" for a
// kind that no selector reaches.
func objectName(o check.Object) string {
	switch o := o.(type) {
	case *check.Func:
		return o.Name
	case *check.Var:
		return o.Name
	case *check.TypeName:
		return o.Name
	case *check.Context:
		return o.Name
	}
	return ""
}

// object reaches o's declaration when a sourced package declares it.
func (r *reach) object(o check.Object) {
	if tn, ok := o.(*check.TypeName); ok {
		r.typeName(tn)
		return
	}
	if di, ok := r.byObj[o]; ok {
		r.add(di)
	}
}

// typeName reaches a type's declaration and the functions of its package
// whose first parameter is the type.
func (r *reach) typeName(tn *check.TypeName) {
	if tn == nil || r.types[tn] {
		return
	}
	r.types[tn] = true
	if di, ok := r.byObj[tn]; ok {
		r.add(di)
	}
	if tn.Pkg == nil || !r.l.isSourced(tn.Pkg) {
		return
	}
	for _, f := range r.firstsOf(tn.Pkg)[tn] {
		r.object(f)
	}
}

// firstsOf indexes p's functions by the type their first parameter names.
func (r *reach) firstsOf(p *check.Package) map[*check.TypeName][]*check.Func {
	if m, ok := r.firsts[p]; ok {
		return m
	}
	m := map[*check.TypeName][]*check.Func{}
	for _, f := range r.l.canon.funcs(p) {
		if f.Sig == nil || len(f.Sig.Params) == 0 {
			continue
		}
		t := f.Sig.Params[0].Type
		if ptr, ok := t.(*check.Pointer); ok {
			t = ptr.Elem
		}
		if n, ok := t.(*check.Named); ok {
			m[n.Obj] = append(m[n.Obj], f)
		}
	}
	r.firsts[p] = m
	return m
}

// declReached reports whether the lowering lowers d.
func (l *lowerer) declReached(d syntax.Decl) bool {
	return l.reached == nil || l.reached[d]
}
