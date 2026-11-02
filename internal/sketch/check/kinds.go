package check

import "ergo.local/prototype/internal/sketch/syntax"

// declKinds holds the solved kinds of the unconstrained bracket parameters
// of the package's generic type declarations.
type declKinds struct {
	kinds     map[*TypeName][]Kind
	conflicts map[*TypeName]map[string]bool
}

// kindVar is one bracket parameter of one declaration: a variable of the
// kind constraint system.
type kindVar struct {
	tn *TypeName
	i  int
}

// solveDeclKinds finds the kind of every unconstrained bracket parameter of
// every generic type declaration of the package before any declaration
// resolves (D6; round-2 M4). A parameter's kind follows its positions: in
// error[…] an error row, after the capability-row keyword a capability row,
// in a plain type position a type, and as the argument of a generic type
// whatever that type's parameter is. The last makes the kinds of recursive
// and mutually recursive declarations a system of equalities, solved here
// with union-find over all declarations at once; a parameter that no
// position constrains is a type. A parameter whose positions disagree is a
// conflict (S108 when the declaration resolves).
//
// Prior art: Haskell's kind inference solves a binding group's kinds
// together before checking it; Go has no kind inference, and Koka infers
// row kinds of type parameters from their use the same way.
func (c *checker) solveDeclKinds() {
	parent := map[kindVar]kindVar{}
	var find func(v kindVar) kindVar
	find = func(v kindVar) kindVar {
		p, ok := parent[v]
		if !ok || p == v {
			return v
		}
		r := find(p)
		parent[v] = r
		return r
	}
	seen := map[kindVar]map[Kind]bool{}
	observe := func(v kindVar, k Kind) {
		r := find(v)
		if seen[r] == nil {
			seen[r] = map[Kind]bool{}
		}
		seen[r][k] = true
	}
	union := func(a, b kindVar) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		parent[ra] = rb
		for k := range seen[ra] {
			if seen[rb] == nil {
				seen[rb] = map[Kind]bool{}
			}
			seen[rb][k] = true
		}
		delete(seen, ra)
	}
	// params returns the bracket parameter names of a local generic
	// declaration, in order.
	params := func(tn *TypeName) []string {
		var out []string
		if tn.Decl == nil || tn.Decl.TypeParams == nil {
			return nil
		}
		for _, f := range tn.Decl.TypeParams.List {
			for _, n := range f.Names {
				out = append(out, n.Name)
			}
		}
		return out
	}
	// index resolves an index expression's head: a local generic
	// declaration links each bare parameter argument to the head's
	// parameter; any other generic type gives its resolved kinds.
	linkHead := func(w *typeWalker, self func(syntax.Expr) (kindVar, bool)) func(*syntax.IndexExpr) bool {
		return func(ix *syntax.IndexExpr) bool {
			htn, hn := c.namedHead(ix.X)
			if hn == nil || htn.Pkg != c.pkg || htn.Decl == nil || htn.Decl.TypeParams == nil {
				return false
			}
			w.walk(ix.X, KindType)
			for i, a := range ix.Indices {
				slot := kindVar{htn, i}
				if _, ok := parent[slot]; !ok {
					w.walk(a, KindType)
					continue
				}
				args := orLeaves(a)
				for _, arg := range args {
					if v, ok := self(arg); ok {
						union(v, slot)
					} else if len(args) == 1 {
						w.walk(arg, KindType)
					}
				}
			}
			return true
		}
	}
	var decls []*TypeName
	for _, tn := range c.pkg.Types {
		if tn.Decl == nil || tn.Decl.TypeParams == nil {
			continue
		}
		decls = append(decls, tn)
		i := 0
		for _, f := range tn.Decl.TypeParams.List {
			for range f.Names {
				v := kindVar{tn, i}
				parent[v] = v
				// `[E error]` is refused (constraintExpr): E's kind is
				// solved by position, as `[E]`'s is.
				if f.Type != nil && !isErrorName(f.Type) {
					observe(v, KindType)
				}
				i++
			}
		}
	}
	for _, tn := range decls {
		saved := c.cur
		// Defensive: this walk only observes and unifies parameter kinds.
		c.cur = tn.file
		names := params(tn)
		index := map[string]int{}
		for i, n := range names {
			index[n] = i
		}
		self := func(x syntax.Expr) (kindVar, bool) {
			id, ok := unparen(x).(*syntax.Ident)
			if !ok {
				return kindVar{}, false
			}
			i, ok := index[id.Name]
			return kindVar{tn, i}, ok
		}
		var w typeWalker
		w.f = func(id *syntax.Ident, k Kind) {
			if i, ok := index[id.Name]; ok {
				observe(kindVar{tn, i}, k)
			}
		}
		w.index = linkHead(&w, self)
		w.slots = c.slotKinds
		w.walk(tn.Decl.Spec, KindType)
		c.cur = saved
	}
	// A method binds its receiver's parameters by position, and its
	// signature constrains them like the declaration's body does (D6:
	// "body and methods"; round-3 M3). Under 196m a method is a function
	// whose first parameter is the type, so `func Run[E](j Job[E]) fails E`
	// makes Job's parameter an error row, as `func (Job[E]) Run() error[E]`
	// did: the first parameter's arguments that are the function's own
	// bracket parameters bind by position. An operation type's functions
	// are not its methods (selfOwner), so its first parameter binds nothing.
	isDecl := map[*TypeName]bool{}
	for _, tn := range decls {
		isDecl[tn] = true
	}
	for _, f := range c.funcs {
		d := f.Decl
		if d == nil || d.Type == nil {
			continue
		}
		if d.TypeParams == nil || d.Type.Params == nil || len(d.Type.Params.List) == 0 || f.Name == "main" {
			continue
		}
		rt := d.Type.Params.List[0].Type
		own := map[string]bool{}
		for _, fld := range d.TypeParams.List {
			for _, n := range fld.Names {
				own[n.Name] = true
			}
		}
		rt = unparen(rt)
		if st, ok := rt.(*syntax.StarExpr); ok {
			rt = unparen(st.X)
		}
		ix, ok := rt.(*syntax.IndexExpr)
		if !ok {
			continue
		}
		htn, _ := c.namedHead(ix.X)
		if htn == nil || !isDecl[htn] {
			continue
		}
		if _, op := htn.Decl.Spec.(*syntax.InterfaceType); op {
			continue
		}
		index := map[string]int{}
		for i, a := range ix.Indices {
			if id, ok := unparen(a).(*syntax.Ident); ok && id.Name != "_" && own[id.Name] {
				index[id.Name] = i
			}
		}
		saved := c.cur
		// Defensive: receiver kind collection only observes and unifies kinds.
		c.cur = f.file
		self := func(x syntax.Expr) (kindVar, bool) {
			id, ok := unparen(x).(*syntax.Ident)
			if !ok {
				return kindVar{}, false
			}
			i, ok := index[id.Name]
			if _, known := parent[kindVar{htn, i}]; !ok || !known {
				return kindVar{}, false
			}
			return kindVar{htn, i}, true
		}
		var w typeWalker
		w.f = func(id *syntax.Ident, k Kind) {
			if v, ok := self(id); ok {
				observe(v, k)
			}
		}
		w.index = linkHead(&w, self)
		w.slots = c.slotKinds
		w.walk(d.Type, KindType)
		c.cur = saved
	}
	c.kinds = declKinds{kinds: map[*TypeName][]Kind{}, conflicts: map[*TypeName]map[string]bool{}}
	for _, tn := range decls {
		names := params(tn)
		ks := make([]Kind, len(names))
		for i, name := range names {
			s := seen[find(kindVar{tn, i})]
			switch {
			case len(s) > 1:
				if c.kinds.conflicts[tn] == nil {
					c.kinds.conflicts[tn] = map[string]bool{}
				}
				c.kinds.conflicts[tn][name] = true
			case len(s) == 1:
				for k := range s {
					ks[i] = k
				}
			default:
				ks[i] = KindType
			}
		}
		c.kinds.kinds[tn] = ks
	}
}

// orLeaves returns the operands of a row argument written as a union,
// E | F | G, at any nesting; any other expression is its own leaf.
func orLeaves(x syntax.Expr) []syntax.Expr {
	if b, ok := unparen(x).(*syntax.BinaryExpr); ok && b.Op == syntax.OR {
		return append(orLeaves(b.X), orLeaves(b.Y)...)
	}
	return []syntax.Expr{x}
}
