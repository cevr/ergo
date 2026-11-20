package check

import (
	"reflect"

	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Maps keyed through a provider (CONSTRAINTS C5; types.md, Maps). A Map
// type has a third type argument H, bound by the prelude's operation type
// Hashing[K]: the hash and the equality by which the Map finds its keys.
//
//   - Map[K, V] is Map[K, V, Structural[K]]: one type with two spellings.
//     The checker keeps it as the Map type (*Map), whose rules and readers
//     are unchanged.
//   - Map[K, V, H] for any other H is the prelude's record hashTable[K, H,
//     V], an open-addressing table in Ergo alone. Its operations are the
//     record's functions, reached by the dot form as a Map's are. The
//     forms the syntax gives a Map, len(m), m[k] = v, `for k, v in m` and
//     a literal with entries, are checked once as a Map's, recorded as
//     sites, then written as the calls m.Len(), m = m.Put(k, v),
//     `for k, v in m.Stored()` and Map[K, V, H]{}.Put(k, v)… in the
//     package's syntax, which check checks again (rewriteMapSites). So
//     every later reader of the package, the lowering and the evaluator
//     included, sees ordinary calls of a record's functions. A type
//     prints as the Map it is.
//
// This file is the one place that reads the names Structural and
// hashTable.

// mapSite is one syntax form of a Map with a provider that check rewrites:
// a len call, an index store, a for over the Map, or a literal with
// entries.
type mapSite struct {
	node syntax.Node // *syntax.CallExpr, *syntax.AssignStmt, *syntax.RangeStmt or *syntax.CompositeLit
}

// providerStore records s, m[k] = v on a Map with a provider, as a site;
// a target it cannot write twice is S108.
func (c *checker) providerStore(s *syntax.AssignStmt) {
	ix, ok := unparen(s.Lhs[0]).(*syntax.IndexExpr)
	if !ok || len(ix.Indices) != 1 || !IsProviderMap(c.pkg.Info.Types[ix.X]) {
		return
	}
	if clonePlace(ix.X) == nil {
		c.errorf(ix.X, CodeType, "a store into a Map with a provider names its Map by a variable, a field or an element at a name or a literal; write m = m.Put(k, v) (types.md, Maps)")
		return
	}
	c.mapSites = append(c.mapSites, mapSite{node: s})
}

// providerLit checks a literal of a Map with a provider: its entries as a
// Map literal's, and its provider's default, which an empty Map holds.
// One with entries is a site (Map[K, V, H]{}.Put(k, v)…).
func (c *checker) providerLit(x *syntax.CompositeLit, t Type, mark int, lt func() litType) operand {
	k, v, h, _ := c.mapParts(t)
	if !defaultable(h) {
		c.errorf(x, CodeType, "%s has no default, so Map[%s, %s, %s]{} has no provider; write maps.Using[%s, %s, %s](by) (types.md, Maps 3)", h, k, v, h, k, v, h)
		c.orphanElements(x, mark, lt())
		return operand{mode: modeValue, typ: t}
	}
	if x.Base != nil {
		c.errorf(x, CodeType, "a Map literal has no ..base")
	}
	c.mapElements(x, k, v, mark, lt)
	switch {
	case len(x.Elts) > 0 && c.opts.mapsRewritten:
		// rewriteMapSites found no node that holds the literal.
		c.errorf(x, CodeType, "this literal of %s is not rewritten to its table; write %s{}.Put(k, v) (types.md, Maps)", t, t)
	case len(x.Elts) > 0:
		c.mapSites = append(c.mapSites, mapSite{node: x})
	}
	return operand{mode: modeValue, typ: t}
}

// providerEqual checks a == b or a != b of two Maps of the provider Map
// type t outside the prelude: the same keys under the provider's Equal,
// each with an == value (types.md, Maps 3), the table's Equal. It
// records the site and reports true; the prelude compares its records
// as the records they are.
func (c *checker) providerEqual(x *syntax.BinaryExpr, t Type) bool {
	if c.pkg.Path == stdlib.PreludePath {
		return false
	}
	_, v, _, _ := c.mapParts(t)
	switch {
	case !comparable(v):
		c.errorf(x, CodeType, "two Maps with a provider are == when they hold the same keys, each with an == value; %s values are not comparable (types.md, Maps 3)", v)
	case c.opts.mapsRewritten:
		c.errorf(x, CodeType, "this comparison of %s is not rewritten to its table; write a.Equal(b) (types.md, Maps)", t)
	default:
		c.mapSites = append(c.mapSites, mapSite{node: x})
	}
	return true
}

// rewriteMapSites writes each site as the calls of the table's functions,
// in the files' syntax. A literal whose parent it does not find stays, and
// the second check refuses it (providerLit).
func rewriteMapSites(sites []mapSite, files []*syntax.File) {
	ident := func(pos syntax.Pos, name string) *syntax.Ident { return &syntax.Ident{NamePos: pos, Name: name} }
	call := func(recv syntax.Expr, name string, pos syntax.Pos, args ...syntax.Expr) *syntax.CallExpr {
		return &syntax.CallExpr{Fun: &syntax.SelectorExpr{X: recv, Sel: ident(pos, name)}, Lparen: pos, Args: args, Rparen: pos}
	}
	for _, s := range sites {
		switch n := s.node.(type) {
		case *syntax.CallExpr: // len(m)
			m := n.Args[0]
			n.Fun, n.Args = &syntax.SelectorExpr{X: m, Sel: ident(n.Fun.Pos(), "Len")}, nil
		case *syntax.RangeStmt: // for k, v in m
			n.X = call(n.X, "Stored", n.X.End())
		case *syntax.AssignStmt: // m[k] = v
			ix := unparen(n.Lhs[0]).(*syntax.IndexExpr)
			n.Rhs[0] = call(clonePlace(ix.X), "Put", ix.Lbrack, ix.Indices[0], n.Rhs[0])
			n.Lhs[0] = ix.X
		case *syntax.BinaryExpr: // a == b, a != b
			var out syntax.Expr = call(n.X, "Equal", n.OpPos, n.Y)
			if n.Op == syntax.NEQ {
				out = &syntax.UnaryExpr{OpPos: n.OpPos, Op: syntax.NOT, X: out}
			}
			for _, f := range files {
				if replaceNode(reflect.ValueOf(f), n, out, map[uintptr]bool{}) {
					break
				}
			}
		case *syntax.CompositeLit: // Map[K, V, H]{k: v, …}
			var out syntax.Expr = n
			for _, e := range n.Elts {
				kv := e.(*syntax.KeyValueExpr)
				out = call(out, "Put", kv.Colon, kv.Key, kv.Value)
			}
			for _, f := range files {
				if replaceNode(reflect.ValueOf(f), n, out, map[uintptr]bool{}) {
					n.Elts = nil
					break
				}
			}
		}
	}
}

// replaceNode replaces the child old of a node under v with new: the one
// field or element that holds it. A literal's parent can be any node that
// holds an expression, so it walks the syntax by reflection.
func replaceNode(v reflect.Value, old, new syntax.Expr, seen map[uintptr]bool) bool {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || seen[v.Pointer()] {
			return false
		}
		seen[v.Pointer()] = true
		return replaceNode(v.Elem(), old, new, seen)
	case reflect.Interface:
		if v.IsNil() {
			return false
		}
		return replaceNode(v.Elem(), old, new, seen)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if replaceChild(v.Field(i), old, new) || replaceNode(v.Field(i), old, new, seen) {
				return true
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if replaceChild(v.Index(i), old, new) || replaceNode(v.Index(i), old, new, seen) {
				return true
			}
		}
	}
	return false
}

// replaceChild sets f to new when f holds old and can hold an expression.
func replaceChild(f reflect.Value, old, new syntax.Expr) bool {
	if f.Kind() != reflect.Interface || f.IsNil() || !f.CanSet() {
		return false
	}
	if e, ok := f.Interface().(syntax.Expr); !ok || e != old || !reflect.TypeOf(new).AssignableTo(f.Type()) {
		return false
	}
	f.Set(reflect.ValueOf(new))
	return true
}

// clonePlace is a fresh copy of the place x, a variable, a field or an
// element at a name or a literal, or nil for any other expression: a
// store reads its Map at the copy, so nothing in it may run twice.
func clonePlace(x syntax.Expr) syntax.Expr {
	switch x := x.(type) {
	case *syntax.Ident:
		return &syntax.Ident{NamePos: x.NamePos, Name: x.Name}
	case *syntax.ParenExpr:
		if in := clonePlace(x.X); in != nil {
			return &syntax.ParenExpr{Lparen: x.Lparen, X: in, Rparen: x.Rparen}
		}
	case *syntax.SelectorExpr:
		if in := clonePlace(x.X); in != nil {
			return &syntax.SelectorExpr{X: in, Sel: &syntax.Ident{NamePos: x.Sel.NamePos, Name: x.Sel.Name}}
		}
	case *syntax.StarExpr:
		if in := clonePlace(x.X); in != nil {
			return &syntax.StarExpr{Star: x.Star, X: in}
		}
	case *syntax.IndexExpr:
		if len(x.Indices) != 1 {
			return nil
		}
		in := clonePlace(x.X)
		var at syntax.Expr
		switch i := x.Indices[0].(type) {
		case *syntax.Ident:
			at = &syntax.Ident{NamePos: i.NamePos, Name: i.Name}
		case *syntax.BasicLit:
			at = &syntax.BasicLit{ValuePos: i.ValuePos, Kind: i.Kind, Value: i.Value, ValueEnd: i.ValueEnd}
		}
		if in != nil && at != nil {
			return &syntax.IndexExpr{X: in, Lbrack: x.Lbrack, Indices: []syntax.Expr{at}, Rbrack: x.Rbrack}
		}
	}
	return nil
}

// preludeTypeName is the prelude's type named name, exported or not; in
// the prelude's own check, its package scope's.
func (c *checker) preludeTypeObj(name string) *TypeName {
	if c.pkg.Path == stdlib.PreludePath {
		tn, _ := c.pkg.Scope.names[name].(*TypeName)
		return tn
	}
	p := prelude().pkg
	if p == nil {
		return nil
	}
	tn, _ := p.Scope.names[name].(*TypeName)
	return tn
}

// isStructural reports whether h is the prelude's Structural[k].
func (c *checker) isStructural(h, k Type) bool {
	n, ok := h.(*Named)
	if !ok || len(n.TArgs) != 1 {
		return false
	}
	o := n.origin().Obj
	return o != nil && o.Name == "Structural" && o.Pkg != nil && o.Pkg.Path == stdlib.PreludePath && c.identical(n.TArgs[0], k)
}

// mapOf is the type Map[k, v, h]: the Map type when h is nil or
// Structural[k], else hashTable[k, h, v], which stores its keys by h's
// NormalForm (mapshape.go).
func (c *checker) mapOf(k, v, h Type) Type {
	if h == nil || c.isStructural(h, k) {
		return &Map{Key: k, Elem: v}
	}
	tn := c.preludeTypeObj("hashTable")
	if tn == nil {
		return invalid
	}
	return c.instantiate(tn.Type.(*Named), []Type{k, h, v}) // hashTable[K, H, V]
}

// IsProviderMap reports whether t is a Map with a provider other than
// Structural: an instance of the prelude's hashTable.
func IsProviderMap(t Type) bool {
	_, _, _, ok := providerParts(t)
	return ok
}

// providerParts are the key, provider and value types of t, an instance
// of the prelude's hashTable[K, H, V].
func providerParts(t Type) (k, h, v Type, ok bool) {
	n, isNamed := t.(*Named)
	if !isNamed {
		return nil, nil, nil, false
	}
	o := n.origin().Obj
	if o == nil || o.Pkg == nil || o.Pkg.Path != stdlib.PreludePath {
		return nil, nil, nil, false
	}
	if o.Name == "hashTable" && len(n.TArgs) == 3 {
		return n.TArgs[0], n.TArgs[1], n.TArgs[2], true
	}
	return nil, nil, nil, false
}

// mapParts are the key, value and provider types of a Map type t, with
// Structural[K] written out, and whether t is a Map.
func (c *checker) mapParts(t Type) (k, v, h Type, ok bool) {
	switch t := t.(type) {
	case *Map:
		tn := c.preludeTypeObj("Structural")
		if tn == nil {
			return nil, nil, nil, false
		}
		return t.Key, t.Elem, c.instantiate(tn.Type.(*Named), []Type{t.Key}), true
	case *Named:
		if k, h, v, ok := providerParts(t); ok {
			return k, v, h, true
		}
	}
	return nil, nil, nil, false
}

// mapProviderMismatch is the S108 text of a Map used where a Map of the
// same key and value types but another provider is expected, or "": two
// such Maps find their keys in different ways, so neither stands for the
// other.
func (c *checker) mapProviderMismatch(from, to Type) string {
	k1, v1, h1, ok1 := c.mapParts(from)
	k2, v2, h2, ok2 := c.mapParts(to)
	if !ok1 || !ok2 || !c.identical(k1, k2) || !c.identical(v1, v2) || c.identical(h1, h2) {
		return ""
	}
	return "cannot use " + from.String() + " as " + to.String() + ": the two Maps find their keys through different providers, " +
		h1.String() + " and " + h2.String() + "; build one from the other's entries (types.md, Maps)"
}

// mapTypeString is the Map spelling of a provider Map's type.
func mapTypeString(n *Named) string {
	k, h, v, _ := providerParts(n)
	return "Map[" + k.String() + ", " + v.String() + ", " + h.String() + "]"
}

// hashCall checks hash(k): k of a comparable type, the result a uint64
// such that a == b gives hash(a) == hash(b). It is generated from k's
// declared type as == is (types.md, Operators 1), so it is a language
// operation, open to every package, not a std body.
func (c *checker) hashCall(x *syntax.CallExpr) operand {
	if len(x.Args) != 1 || x.Trailing != nil {
		c.errorf(x, CodeType, "hash takes one argument, a value of a comparable type")
		c.silentArgs(x)
		return bad(x)
	}
	op := c.valueExpr(x.Args[0], nil)
	if op.invalid() {
		return bad(x)
	}
	if !comparable(op.typ) {
		c.errorf(x.Args[0], CodeType, "hash takes a value of a comparable type, not %s (types.md, Maps)", op.typ)
		return bad(x)
	}
	return operand{mode: modeValue, typ: Typ[Uint64]}
}

// hashingOf is the prelude's operation type Hashing.
func (c *checker) hashingOf() *TypeName { return c.preludeTypeObj("Hashing") }
