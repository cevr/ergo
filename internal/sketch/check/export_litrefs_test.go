package check

import "ergo.local/prototype/internal/sketch/syntax"

// pkgObjectOf is the package object o stands for as a reference edge (a
// package function or a package var), or nil: the objects the reference
// walk records an edge to (RefWalkProblems).
func pkgObjectOf(c *checker, o Object) any {
	switch o := o.(type) {
	case *Func:
		if c.pkg.Scope.names[o.Name] == Object(o) {
			return o
		}
	case *Var:
		if o.pkg {
			if pv := c.pkgVarOf(o); pv != nil {
				return pv
			}
		}
	}
	return nil
}

// RefEdge checks src as one package and reports what typing and the
// reference walk make of the identifier at offset off as an expression
// reference (fuzz-863c). Typing says "edge" when Info.Uses records a
// package function or var, "local" when it records a local, "names" when
// it records another object (a type, a prelude constant), and "none" when
// it records nothing. The walk says "edge" when it resolves the identifier
// to a package function or var, "value" when it walks it as a value that
// resolves to no such object, "names" when it took it in a type or context
// position, "visited" when it only visited it, and "unvisited" when no walk
// reached it. found is false when src does not parse or no identifier
// starts at off.
func RefEdge(src string, off int) (info, walk string, found bool) {
	file, err := syntax.Parse(src)
	if err != nil {
		return "", "", false
	}
	var id *syntax.Ident
	syntax.Inspect(file, func(n syntax.Node) bool {
		if x, ok := n.(*syntax.Ident); ok && int(x.Pos()) == off && id == nil {
			id = x
		}
		return id == nil
	})
	if id == nil {
		return "", "", false
	}
	c := newChecker("main", Std, prelude().scope)
	var walks []*refWalker
	refTrace = func(_ syntax.Node, w *refWalker) {
		if w.c == c {
			walks = append(walks, w)
		}
	}
	defer func() { refTrace = nil }()
	c.run([]*syntax.File{file})
	info = "none"
	if u, ok := c.pkg.Info.Uses[id]; ok {
		switch v, isVar := u.(*Var); {
		case isVar && !v.pkg:
			info = "local"
		case pkgObjectOf(c, u) != nil:
			info = "edge"
		default:
			info = "names"
		}
	}
	walk = "unvisited"
	for _, w := range walks {
		if !w.visited[id] {
			continue
		}
		o, walked := w.idents[id]
		switch {
		case walked && o != nil:
			walk = "edge"
		case walked:
			walk = "value"
		case w.nameIdents[id]:
			walk = "names"
		default:
			walk = "visited"
		}
	}
	return info, walk, true
}
