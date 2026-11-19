package lint

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The law facts of take-one-by-law (SKILL-RULES SR-ERGO-5, SR-P02): a law
// that states one element of a function's result, the first or the last,
// is what another function returns for the same arguments:
//
//	law MaxIsLastSorted(xs []int) {
//		let s = Sort(xs)
//		if len(s) == 0 { return … }
//		Max(xs) == Some[int]{Value: s[len(s)-1]} && Min(xs) == Some[int]{Value: s[0]}
//	}
//
// says Sort(xs)[0] is Min's value, with no sort. The rule keys on the law,
// never on a function's name (STD HAS NO PRIVILEGES).

// takeFact is one such equation: whole's result at index (first, or the
// last when last is set) is one's result, for the same arguments. whole's
// i-th argument is the law's parameter at wholeArgs[i], and one's at
// oneArgs[i]; types are the law's parameter types, the only types the
// law was tested at.
type takeFact struct {
	law       string
	one       *check.Func
	whole     *check.Func
	oneArgs   []int
	wholeArgs []int
	first     bool
	types     []string
}

// lawTakes is the take facts of the laws of pkg, read once per package.
type lawTakes map[*check.Package][]takeFact

func (lt lawTakes) of(pkg *check.Package) []takeFact {
	if pkg == nil {
		return nil
	}
	if facts, ok := lt[pkg]; ok {
		return facts
	}
	var facts []takeFact
	for _, law := range pkg.Laws {
		facts = append(facts, takeFacts(pkg.Info, law)...)
	}
	lt[pkg] = facts
	return facts
}

// takeFacts reads law's body: lets, guards that only rule out an empty
// result (an index of it fails there anyway), then a conjunction of
// equations in its final expression.
func takeFacts(info *check.Info, law *check.Law) []takeFact {
	d := law.Decl
	if info == nil || d.Body == nil || d.Rewrite != nil || law.Owner != nil || law.Machine != nil || len(d.Body.List) == 0 {
		return nil
	}
	params := map[*check.Var]int{}
	var types []string
	for i, v := range law.Params {
		params[v] = i
		types = append(types, v.Type.String())
	}
	lets := map[*check.Var]syntax.Expr{}
	var guards []syntax.Expr
	list := d.Body.List
	for _, s := range list[:len(list)-1] {
		switch s := s.(type) {
		case *syntax.DeclStmt:
			if s.Decl.Mut != syntax.NoPos || len(s.Decl.Specs) != 1 {
				return nil
			}
			sp := s.Decl.Specs[0]
			if len(sp.Names) != 1 || len(sp.Values) != 1 {
				return nil
			}
			v, ok := info.Defs[sp.Names[0]].(*check.Var)
			if !ok {
				return nil
			}
			lets[v] = sp.Values[0]
		case *syntax.IfStmt:
			guards = append(guards, s.Cond)
		default:
			return nil
		}
	}
	tail, ok := list[len(list)-1].(*syntax.ExprStmt)
	if !ok {
		return nil
	}
	lf := lawForm{info: info, params: params, lets: lets}
	var out []takeFact
	for _, eq := range conjuncts(tail.X) {
		b, ok := eq.(*syntax.BinaryExpr)
		if !ok || b.Op != syntax.EQL {
			continue
		}
		for _, sides := range [][2]syntax.Expr{{b.X, b.Y}, {b.Y, b.X}} {
			if f, ok := lf.take(sides[0], sides[1], guards); ok {
				f.law, f.types = law.Name, types
				out = append(out, f)
			}
		}
	}
	return out
}

// conjuncts is e split at &&.
func conjuncts(e syntax.Expr) []syntax.Expr {
	e = unparen(e)
	if b, ok := e.(*syntax.BinaryExpr); ok && b.Op == syntax.LAND {
		return append(conjuncts(b.X), conjuncts(b.Y)...)
	}
	return []syntax.Expr{e}
}

// lawForm reads a law's expressions: its parameters and lets.
type lawForm struct {
	info   *check.Info
	params map[*check.Var]int
	lets   map[*check.Var]syntax.Expr
}

// call is the function e calls with law parameters only, and their
// indexes; a let's name stands for its value.
func (lf lawForm) call(e syntax.Expr) (*check.Func, []int, bool) {
	e = lf.subst(e)
	c, ok := e.(*syntax.CallExpr)
	if !ok {
		return nil, nil, false
	}
	site := lf.info.Calls[c]
	if site == nil || site.Kind != check.CallFunc || site.Func == nil || site.Recv != nil {
		return nil, nil, false
	}
	var args []int
	for _, a := range c.Args {
		id, ok := unparen(a).(*syntax.Ident)
		if !ok {
			return nil, nil, false
		}
		v, _ := lf.info.Uses[id].(*check.Var)
		i, ok := lf.params[v]
		if !ok {
			return nil, nil, false
		}
		args = append(args, i)
	}
	return site.Func, args, true
}

// subst is the let value a name stands for, or e.
func (lf lawForm) subst(e syntax.Expr) syntax.Expr {
	e = unparen(e)
	if id, ok := e.(*syntax.Ident); ok {
		if v, ok := lf.info.Uses[id].(*check.Var); ok {
			if x, ok := lf.lets[v]; ok {
				return unparen(x)
			}
		}
	}
	return e
}

// take reads `one(params) == [Some{Value:] whole(params)[first or last]`,
// with every guard `len(whole's value) == 0`.
func (lf lawForm) take(oneSide, elemSide syntax.Expr, guards []syntax.Expr) (takeFact, bool) {
	one, oneArgs, ok := lf.call(oneSide)
	if !ok {
		return takeFact{}, false
	}
	elemSide = unparen(elemSide)
	if lit, ok := elemSide.(*syntax.CompositeLit); ok && len(lit.Elts) == 1 {
		kv, ok := lit.Elts[0].(*syntax.KeyValueExpr)
		if !ok {
			return takeFact{}, false
		}
		elemSide = unparen(kv.Value)
	}
	ix, ok := elemSide.(*syntax.IndexExpr)
	if !ok || len(ix.Indices) != 1 {
		return takeFact{}, false
	}
	whole, wholeArgs, ok := lf.call(ix.X)
	if !ok || whole == one {
		return takeFact{}, false
	}
	first, ok := lf.end(ix.Indices[0], whole, wholeArgs)
	if !ok {
		return takeFact{}, false
	}
	for _, g := range guards {
		if !lf.emptyOf(g, whole, wholeArgs) {
			return takeFact{}, false
		}
	}
	return takeFact{one: one, whole: whole, oneArgs: oneArgs, wholeArgs: wholeArgs, first: first}, true
}

// end reports an index that is 0 (first) or len(whole(args))-1.
func (lf lawForm) end(k syntax.Expr, whole *check.Func, args []int) (bool, bool) {
	if v := lf.info.Values[k]; v != nil {
		n, exact := constant.Int64Val(constant.ToInt(v))
		return true, exact && n == 0
	}
	b, ok := unparen(k).(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.SUB {
		return false, false
	}
	if v := lf.info.Values[b.Y]; v == nil {
		return false, false
	} else if n, exact := constant.Int64Val(constant.ToInt(v)); !exact || n != 1 {
		return false, false
	}
	return false, lf.lenOf(b.X, whole, args)
}

// lenOf reports len(whole(args)).
func (lf lawForm) lenOf(e syntax.Expr, whole *check.Func, args []int) bool {
	c, ok := unparen(e).(*syntax.CallExpr)
	if !ok || len(c.Args) != 1 || !builtinLen(lf.info, c) {
		return false
	}
	f, a, ok := lf.call(c.Args[0])
	return ok && f == whole && sameInts(a, args)
}

// emptyOf reports a guard `len(whole(args)) == 0`.
func (lf lawForm) emptyOf(g syntax.Expr, whole *check.Func, args []int) bool {
	b, ok := unparen(g).(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.EQL {
		return false
	}
	v := lf.info.Values[b.Y]
	if v == nil {
		return false
	}
	n, exact := constant.Int64Val(constant.ToInt(v))
	return exact && n == 0 && lf.lenOf(b.X, whole, args)
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// builtinLen reports a call of the builtin len.
func builtinLen(info *check.Info, c *syntax.CallExpr) bool {
	site := info.Calls[c]
	return site != nil && site.Kind == check.CallLen
}

// takeSite is a program index of a call's result, first or last, that a
// take fact covers: the fact, and the call's arguments.
func takeSite(info *check.Info, lt lawTakes, ix *syntax.IndexExpr, value syntax.Expr) (takeFact, bool) {
	c, ok := unparen(value).(*syntax.CallExpr)
	if !ok || len(ix.Indices) != 1 {
		return takeFact{}, false
	}
	site := info.Calls[c]
	if site == nil || site.Kind != check.CallFunc || site.Func == nil || site.Recv != nil {
		return takeFact{}, false
	}
	first, ok := siteEnd(info, ix.Indices[0], ix.X, c)
	if !ok {
		return takeFact{}, false
	}
	for _, f := range lt.of(site.Func.Pkg) {
		if f.whole != site.Func || f.first != first || len(c.Args) != len(f.wholeArgs) {
			continue
		}
		fits := true
		for i, a := range c.Args {
			t := info.Types[a]
			if t == nil || t.String() != f.types[f.wholeArgs[i]] {
				fits = false
			}
		}
		if fits {
			return f, true
		}
	}
	return takeFact{}, false
}

// siteEnd is whether k is 0 (first) or len(x)-1, x the indexed value or
// the call c itself.
func siteEnd(info *check.Info, k, x syntax.Expr, c *syntax.CallExpr) (bool, bool) {
	if v := info.Values[k]; v != nil {
		n, exact := constant.Int64Val(constant.ToInt(v))
		return true, exact && n == 0
	}
	b, ok := unparen(k).(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.SUB {
		return false, false
	}
	if v := info.Values[b.Y]; v == nil {
		return false, false
	} else if n, exact := constant.Int64Val(constant.ToInt(v)); !exact || n != 1 {
		return false, false
	}
	l, ok := unparen(b.X).(*syntax.CallExpr)
	if !ok || len(l.Args) != 1 || !builtinLen(info, l) {
		return false, false
	}
	return false, sameName(info, l.Args[0], x)
}

// markReads marks every name ix reads (the let it indexes, the len of it)
// as covered by a fact.
func markReads(ix *syntax.IndexExpr, covered map[*syntax.Ident]bool) {
	syntax.Inspect(ix, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			covered[id] = true
		}
		return true
	})
}

// sameName reports two reads of one variable.
func sameName(info *check.Info, a, b syntax.Expr) bool {
	x, ok := unparen(a).(*syntax.Ident)
	y, ok2 := unparen(b).(*syntax.Ident)
	return ok && ok2 && info.Uses[x] != nil && info.Uses[x] == info.Uses[y]
}
