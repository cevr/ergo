package lower

// IR builder: sites of fallible rewrite laws over values, matched through
// small functions and lets (ENCDEC; LAWFFI plan §4; types.md, Laws 14).
//
// A std inverse law is fallible on its left side and total on its right:
// `law AtoiInvertsItoa(n int) { try Atoi(Itoa(n)) } == { n }`. The pass's
// IR patterns take total sites only (lawFallible), so such a law fired at
// no site, and a program rarely writes the pair in one expression: it
// writes a codec of its own, a function that encodes and one that
// decodes, and keeps the encoded text in a let:
//
//	func encodeCount(n int) string { strconv.Itoa(n) }
//	func decodeCount(s string) int fails … { try strconv.Atoi(s) }
//	…
//	let text = encodeCount(n)
//	store(text)
//	try decodeCount(text)
//
// unfoldAt decides such a site where the builder binds a fallible call
// (try, catch), before the call is lowered, from the source, as fuseAt
// does for laws over function parameters (ir_fusion.go). It walks the
// law's left side (its try's call) and the site together, and where they
// differ it unfolds the site:
//   - a call of a function whose body is one call (a try of one at the
//     site's top) is its body,
//     with the function's parameters standing for the call's operands;
//   - a name a `let` (not `let mut`) bound in the body being built is its
//     value.
//
// A law parameter binds an operand the site evaluates: one reached
// through no let is evaluated once, by the right side, as the left side
// evaluated it once; one reached through a let must be a name that keeps
// its value (a parameter or a let), or a literal, since the let already
// evaluated it. The let itself still runs: only the call the law's left
// side matches is replaced. An unfolded function's body is matched whole,
// so dropping it drops nothing the law's evidence has not judged; the
// law's own functions are pure by its rule 4, and its verdict (the
// ledger) says the left side's failure never happens.
//
// The run build applies it; the test build (shadow) and a canary keep the
// site as written. A site's row goes to ergo inspect rewrites as any
// rewrite's.
//
// Prior art: GHC's RULES fire after the simplifier inlines wrappers
// (worker/wrapper and INLINE pragmas exist so rules see through them);
// OCaml's flambda and ReScript inline small functions before their
// rewrites. Gleam and Elixir leave the round trip to run.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// unfoldDepth bounds the functions one site unfolds through.
const unfoldDepth = 4

// ufView is a site expression in the facts it was checked with: the
// site's own (env nil, depth 0) or a function body unfolded at it, whose
// parameters stand for operands in env.
type ufView struct {
	x     syntax.Expr
	info  *check.Info
	env   map[*check.Var]ufView
	depth int
	// viaLet marks a view reached through a let's value.
	viaLet bool
}

// ufMatch is a law matched at a site: each parameter's operand, in the
// site's facts.
type ufMatch struct {
	ops  map[*check.Var]syntax.Expr
	pure string // why the site keeps its left side, "" to apply it
}

// fallibleLaws are the laws in scope that unfoldAt places: rewrites over
// values whose left side is a try of a call and whose right side cannot
// fail.
func (l *lowerer) fallibleLaws() []*check.Law {
	r := l.canon.funcRw
	if r == nil {
		return nil
	}
	if r.fallible == nil {
		r.fallible = []*check.Law{}
		seen := map[*check.Package]bool{}
		for _, p := range append([]*check.Package{l.pkg}, l.libraries()...) {
			if seen[p] {
				continue
			}
			seen[p] = true
			for _, law := range p.Laws {
				rw := law.FuncRewrite
				if rw == nil || len(law.TParams) > 0 || !lawFallible(law) {
					continue
				}
				try, ok := unparen(rw.Lhs).(*syntax.TryExpr)
				if !ok || !rwTotalSide(rw.Rhs) {
					continue
				}
				if _, isCall := unparen(try.X).(*syntax.CallExpr); isCall {
					r.fallible = append(r.fallible, law)
				}
			}
		}
	}
	return r.fallible
}

// rwTotalSide reports a side with no try in it.
func rwTotalSide(side syntax.Expr) bool {
	total := true
	syntax.Inspect(side, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.TryExpr); ok {
			total = false
		}
		return total
	})
	return total
}

// recordLet keeps a let's value for unfoldAt: v, a name that keeps its
// value, bound to x in the body being built.
func (b *irBuilder) recordLet(v *check.Var, x syntax.Expr) {
	if v.Mut() || b.lawDepth > 0 {
		return
	}
	if b.letInits == nil {
		b.letInits = map[*check.Var]syntax.Expr{}
	}
	b.letInits[v] = x
}

// unfoldAt is the fallible call x as a fallible law's right side: the
// statements and the value, ok false to bind x as written.
func (b *irBuilder) unfoldAt(x *syntax.CallExpr) ([]*irStmt, *irExpr, bool) {
	if b.lawDepth > 0 || b.law != nil || b.st.census != nil {
		return nil, nil, false
	}
	r := b.l.canon.funcRw
	if r == nil || r.test || r.every > 0 {
		return nil, nil, false
	}
	for _, law := range b.l.fallibleLaws() {
		um, ok := b.unfoldMatch(law, x)
		if !ok {
			continue
		}
		m := rwMatch{law: law, site: x, keep: b.keep > 0}
		v, why := b.rwDecide(m)
		if why == "" {
			why = um.pure
		}
		b.siteRow(x, v, why == "", why)
		if why != "" {
			return nil, nil, false
		}
		var pre []*irStmt
		vals := map[*check.Var]*irExpr{}
		for _, p := range lawOrder(law) {
			opre, e := b.value(um.ops[p])
			pre = append(pre, opre...)
			if e.K != "const" && e.K != "local" {
				t := b.temp("arg", b.goType(p.Type, x), e.Pos)
				pre = append(pre, &irStmt{K: "def", Pos: e.Pos, Local: t, X: e})
				e = &irExpr{K: "local", Pos: e.Pos, I: t}
			}
			vals[p] = e
		}
		rpre, right := b.lawSide(law, law.FuncRewrite.Rhs, vals, false)
		b.rwLevel(v.Verdict)
		return append(pre, rpre...), right, true
	}
	return nil, nil, false
}

// unfoldMatch walks law's left side (its try's call) and the site x
// together, unfolding the site where they differ.
func (b *irBuilder) unfoldMatch(law *check.Law, x *syntax.CallExpr) (ufMatch, bool) {
	linfo := law.Pkg.Info
	param := map[*check.Var]bool{}
	for _, v := range law.Params {
		param[v] = true
	}
	m := ufMatch{ops: map[*check.Var]syntax.Expr{}}
	var walk func(p syntax.Expr, s ufView, top bool) bool
	walk = func(p syntax.Expr, s ufView, top bool) bool {
		p = unparen(p)
		if id, ok := p.(*syntax.Ident); ok {
			if v, isVar := linfo.Uses[id].(*check.Var); isVar && param[v] {
				return b.ufBind(&m, v, s)
			}
		}
		if lit, ok := p.(*syntax.BasicLit); ok {
			s = b.ufResolve(s, true)
			sl, ok := unparen(s.x).(*syntax.BasicLit)
			return ok && sl.Kind == lit.Kind && sl.Value == lit.Value
		}
		pc, ok := p.(*syntax.CallExpr)
		if !ok {
			return false
		}
		lc := linfo.Calls[pc]
		if lc == nil || lc.Kind != check.CallFunc || lc.Func == nil || lc.Recv != nil {
			return false
		}
		for {
			s = b.ufResolve(s, true)
			sc, ok := unparen(s.x).(*syntax.CallExpr)
			if !ok {
				return false
			}
			c := s.info.Calls[sc]
			if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Recv != nil {
				return false
			}
			if c.Func == lc.Func {
				if len(pc.Args) != len(sc.Args) {
					return false
				}
				for i := range pc.Args {
					if !walk(pc.Args[i], ufView{x: sc.Args[i], info: s.info, env: s.env, depth: s.depth, viaLet: s.viaLet}, false) {
						return false
					}
				}
				return true
			}
			body, ok := b.ufBody(c.Func, top)
			if !ok || s.depth >= unfoldDepth {
				return false
			}
			params := ufParams(c.Func)
			if params == nil || len(params) != len(sc.Args) {
				return false
			}
			env := map[*check.Var]ufView{}
			for i, pv := range params {
				env[pv] = ufView{x: sc.Args[i], info: s.info, env: s.env, depth: s.depth, viaLet: s.viaLet}
			}
			s = ufView{x: body, info: c.Func.Pkg.Info, env: env, depth: s.depth + 1, viaLet: s.viaLet}
		}
	}
	try := unparen(law.FuncRewrite.Lhs).(*syntax.TryExpr)
	if !walk(try.X, ufView{x: x, info: b.l.info}, true) {
		return ufMatch{}, false
	}
	return m, len(m.ops) == len(law.Params)
}

// ufResolve follows s through the parameters of the functions it is
// unfolded in, and with lets set, through the lets of the body being
// built.
func (b *irBuilder) ufResolve(s ufView, lets bool) ufView {
	for {
		id, ok := unparen(s.x).(*syntax.Ident)
		if !ok {
			return s
		}
		v, isVar := s.info.Uses[id].(*check.Var)
		if !isVar {
			return s
		}
		if op, ok := s.env[v]; ok {
			s = op
			continue
		}
		if lets && s.depth == 0 {
			if init, ok := b.letInits[v]; ok {
				s = ufView{x: init, info: s.info, viaLet: true}
				continue
			}
		}
		return s
	}
}

// ufBody is f's body as one expression, when unfoldAt may unfold a call
// of f: an Ergo function, not generic, whose body is one call (at the
// site's top, a try of one). Its footprint needs no check: the walk
// matches the body whole, each call a law's function or a body unfolded
// in turn, each leaf a law parameter bound to an operand of the site's
// own, so the body runs nothing the law's left side does not.
func (b *irBuilder) ufBody(f *check.Func, top bool) (syntax.Expr, bool) {
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || d.Recv != nil || f.Sig == nil || len(f.Sig.TParams) > 0 || f.Pkg == nil || f.Pkg.Info == nil {
		return nil, false
	}
	if len(d.Body.List) != 1 {
		return nil, false
	}
	es, ok := d.Body.List[0].(*syntax.ExprStmt)
	if !ok {
		return nil, false
	}
	x := unparen(es.X)
	if t, isTry := x.(*syntax.TryExpr); isTry {
		if !top {
			return nil, false
		}
		x = unparen(t.X)
	}
	if _, isCall := x.(*syntax.CallExpr); !isCall {
		return nil, false
	}
	return x, true
}

// ufParams are the variables f's body reads for its parameters, in
// order (the body's, which the checker defines at the declaration's
// names, not the signature's); nil when a parameter has no name.
func ufParams(f *check.Func) []*check.Var {
	var out []*check.Var
	for _, field := range f.Decl.Type.Params.List {
		if len(field.Names) == 0 {
			return nil
		}
		for _, id := range field.Names {
			v, ok := f.Pkg.Info.Defs[id].(*check.Var)
			if !ok {
				return nil
			}
			out = append(out, v)
		}
	}
	return out
}

// ufBind binds the law parameter v to the site operand s: an operand in
// the site's own facts, of v's type, the same expression at a second
// occurrence, and through a let only a name that keeps its value or a
// literal.
func (b *irBuilder) ufBind(m *ufMatch, v *check.Var, s ufView) bool {
	s = b.ufResolve(s, false)
	if s.depth != 0 || s.info != b.l.info {
		return false
	}
	t := s.info.Types[s.x]
	if t == nil || b.l.typeKey(t) != b.l.typeKey(v.Type) {
		return false
	}
	if prev, seen := m.ops[v]; seen {
		return prev == s.x
	}
	if s.viaLet {
		switch x := unparen(s.x).(type) {
		case *syntax.BasicLit:
		case *syntax.Ident:
			w, isVar := s.info.Uses[x].(*check.Var)
			if !isVar || w.Mut() {
				m.pure = "not applied: the operand of " + v.Name + " is a let mut, which may change between the let and the site"
			}
		default:
			m.pure = "not applied: the operand of " + v.Name + " is reached through a let, and the right side would evaluate it again"
		}
	}
	m.ops[v] = s.x
	return true
}
