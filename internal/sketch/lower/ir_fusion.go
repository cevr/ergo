package lower

// IR builder: sites of laws over function parameters (FUSION; owner q234,
// "Extend laws"; types.md, Laws 14, Over function parameters). See
// ir_rewrite.go for the engine this extends.
//
// Such a law (`law TransformFuses[T, U, V comparable](xs []T, f func(x T)
// U, g func(x U) V) { Transform(Transform(xs, f), g) } == { Transform(xs,
// func(x T) V { g(f(x)) }) }`) is decided at the call, before the call is
// lowered (fuseAt), and not by the pass's IR patterns: the IR of a call
// with a function operand depends on the operand (a known callback's call
// goes to its known variant, ir_kcb.go), and a law with type parameters of
// its own has no one IR at all. bindAt walks the law's left side and the
// call's source together, the same functions called at the same shape,
// and unifies their types, which binds the law's type parameters; the
// operands are lowered once, in the order the left side evaluates them,
// and the sides are lowered with the type parameters bound in the body's
// type context (withBinding).
//
// The fused form runs the law's functions in another order: f, then g,
// per element, where the staged form runs f over every element first.
// The law's functions are pure (rule 4), so the values agree; two things
// still differ, and the site keeps the staged form's:
//   - a defect: the right side runs under v1.FuseOr, which on a defect
//     runs the left side again from the start, so the defect raised is
//     the staged form's own, the first in stage order;
//   - an effect: an operand function that writes a variable it captures,
//     or has a nonempty footprint, would show the order, and an operand
//     the staged form evaluates after a stage ran would be evaluated
//     before it; such a site keeps its left side (sitePure).

import (
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// rwBinding is the type a site gives each of a law's type parameters.
type rwBinding map[*check.TypeParam]check.Type

// bindAt unifies law's left side with the site's source call x: the
// binding of the law's type parameters, and each parameter's operand in
// the site's source; ok false when they do not unify or a type parameter
// stays unbound.
func (b *irBuilder) bindAt(law *check.Law, x *syntax.CallExpr) (rwBinding, map[*check.Var]syntax.Expr, bool) {
	bind := rwBinding{}
	ops := map[*check.Var]syntax.Expr{}
	tps := map[*check.TypeParam]bool{}
	for _, tp := range law.TParams {
		tps[tp] = true
	}
	linfo, sinfo := law.RewriteInfo(), b.l.info
	param := map[*check.Var]bool{}
	for _, v := range law.Params {
		param[v] = true
	}
	var walk func(p, s syntax.Expr) bool
	walk = func(p, s syntax.Expr) bool {
		p, s = unparen(p), unparen(s)
		if id, ok := p.(*syntax.Ident); ok {
			if v, isVar := linfo.Uses[id].(*check.Var); isVar && param[v] {
				if prev, seen := ops[v]; seen && prev != s {
					return false
				}
				ops[v] = s
				return b.unify(law.RewriteType(v.Type), sinfo.Types[s], tps, bind)
			}
		}
		pc, ok1 := p.(*syntax.CallExpr)
		sc, ok2 := s.(*syntax.CallExpr)
		if !ok1 || !ok2 {
			return false
		}
		lc, scall := linfo.Calls[pc], sinfo.Calls[sc]
		if lc == nil || scall == nil || lc.Kind != check.CallFunc || scall.Kind != check.CallFunc || lc.Func == nil || scall.Func != lc.Func {
			return false
		}
		pa, sa := lc.Operands(pc), scall.Operands(sc)
		if len(pa) != len(sa) {
			return false
		}
		if !b.unify(linfo.Types[pc], sinfo.Types[sc], tps, bind) {
			return false
		}
		for i := range pa {
			if !walk(pa[i], sa[i]) {
				return false
			}
		}
		return true
	}
	if !walk(law.TopRewrite().Lhs, x) {
		return nil, nil, false
	}
	for _, tp := range law.TParams {
		if bind[tp] == nil {
			return nil, nil, false
		}
	}
	return bind, ops, true
}

// unify reports whether the law's type p, in the type parameters tps,
// is the site's type s, binding each type parameter it meets. Rows are not
// compared: a law's function parameters have empty ones (rule 4), and the
// site's are checked by sitePure.
func (b *irBuilder) unify(p, s check.Type, tps map[*check.TypeParam]bool, bind rwBinding) bool {
	if p == nil || s == nil {
		return false
	}
	if tp, ok := p.(*check.TypeParam); ok && tps[tp] {
		if prev := bind[tp]; prev != nil {
			return b.l.typeKey(prev) == b.l.typeKey(s)
		}
		if _, isParam := s.(*check.TypeParam); isParam {
			return false // a site in a generic body: its Go type is a Go type parameter
		}
		bind[tp] = s
		return true
	}
	switch u := p.(type) {
	case *check.Slice:
		v, ok := s.(*check.Slice)
		return ok && b.unify(u.Elem, v.Elem, tps, bind)
	case *check.Array:
		v, ok := s.(*check.Array)
		return ok && u.Len == v.Len && b.unify(u.Elem, v.Elem, tps, bind)
	case *check.Map:
		v, ok := s.(*check.Map)
		return ok && b.unify(u.Key, v.Key, tps, bind) && b.unify(u.Elem, v.Elem, tps, bind)
	case *check.Pointer:
		v, ok := s.(*check.Pointer)
		return ok && b.unify(u.Elem, v.Elem, tps, bind)
	case *check.Signature:
		v, ok := s.(*check.Signature)
		if !ok || len(u.Params) != len(v.Params) || len(u.Results) != len(v.Results) {
			return false
		}
		for i := range u.Params {
			if !b.unify(u.Params[i].Type, v.Params[i].Type, tps, bind) {
				return false
			}
		}
		for i := range u.Results {
			if !b.unify(u.Results[i], v.Results[i], tps, bind) {
				return false
			}
		}
		return true
	case *check.Named:
		v, ok := s.(*check.Named)
		if !ok || len(u.TArgs) != len(v.TArgs) || u.Obj != v.Obj {
			return false
		}
		if len(u.TArgs) == 0 {
			return b.l.typeKey(u) == b.l.typeKey(v)
		}
		for i := range u.TArgs {
			if !b.unify(u.TArgs[i], v.TArgs[i], tps, bind) {
				return false
			}
		}
		return true
	}
	return b.l.typeKey(p) == b.l.typeKey(s)
}

// withBinding binds the law's type parameters to the site's types in the
// body's type context, for the sides' lowering; the result restores it.
func (b *irBuilder) withBinding(bind rwBinding) func() {
	if len(bind) == 0 {
		return func() {}
	}
	tc := &b.fc.typeCtx
	oldParams, oldBound := tc.params, tc.bound
	params := map[*check.TypeParam]string{}
	for k, v := range oldParams {
		params[k] = v
	}
	bound := map[*check.TypeParam]check.Type{}
	for k, v := range oldBound {
		bound[k] = v
	}
	for tp, t := range bind {
		params[tp], bound[tp] = tc.goType(t), t
	}
	oldRw := b.rwBound
	rw := rwBinding{}
	for k, v := range oldRw {
		rw[k] = v
	}
	for tp, t := range bind {
		rw[tp] = t
	}
	tc.params, tc.bound, b.rwBound = params, bound, rw
	return func() { tc.params, tc.bound, b.rwBound = oldParams, oldBound, oldRw }
}

// fuseAt is the call x as a site of a law over function parameters, if
// one in the rewrite's scope matches its source (bindAt) and the build
// applies it (rwDecide): its operands, each lowered once, in the order
// the left side evaluates them, then the law's right side under
// v1.FuseOr (rewriteAt). ok false lowers x as written. Such a site is
// decided here, before x is lowered, and not by the pass's IR patterns:
// the IR of a call with a function operand depends on the operand (a
// known callback's call goes to its known variant, ir_kcb.go), so no one
// pattern stands for the left side.
func (b *irBuilder) fuseAt(x *syntax.CallExpr) ([]*irStmt, *irExpr, bool) {
	if b.lawDepth > 0 || b.law != nil || b.st.census != nil || !b.rwFuncs() {
		return nil, nil, false
	}
	for _, law := range b.l.scopeLaws() {
		if !law.TopRewrite().OverFuncs {
			continue
		}
		bind, ops, ok := b.bindAt(law, x)
		if !ok {
			continue
		}
		return b.fuseSite(law, bind, ops, x)
	}
	return nil, nil, false
}

// fuseSite is fuseAt's site x of law at the binding bind.
func (b *irBuilder) fuseSite(law *check.Law, bind rwBinding, ops map[*check.Var]syntax.Expr, x *syntax.CallExpr) ([]*irStmt, *irExpr, bool) {
	r := b.l.canon.funcRw
	m := rwMatch{law: law, site: x, bind: bind, ops: ops, keep: b.keep > 0}
	if r.test {
		// The shadow evaluates the operands before either side too: a
		// site the run build keeps for its operands keeps them here.
		if rewriteLowering == "reference" || m.keep || b.sitePure(m) != "" {
			return nil, nil, false
		}
	} else if v, why := b.rwDecide(m); why != "" {
		b.siteRow(x, v, false, why)
		return nil, nil, false
	}
	defer b.withBinding(bind)()
	var pre []*irStmt
	for _, v := range lawOrder(law) {
		opre, e := b.value(ops[v])
		pre = append(pre, opre...)
		j := slices.Index(law.Params, v)
		for len(m.holes) <= j {
			m.holes, m.vals = append(m.holes, nil), append(m.vals, nil)
		}
		m.holes[j] = &irExpr{K: "hole", I: j, T: b.goType(law.RewriteType(v.Type), law.TopRewrite().Lhs)}
		m.vals[j] = e
		m.order = append(m.order, j)
	}
	var slot []*irStmt
	spre, e, ok := b.rewriteAt(m, &rwAt{slot: &slot, total: true})
	if !ok {
		irNo("a fusion site refused after its admission")
	}
	return append(append(pre, slot...), spre...), e, true
}

// lawOrder is law's parameters in the order its left side evaluates
// them: the order they occur in it (a call's operands, left to right,
// each before the call).
func lawOrder(law *check.Law) []*check.Var {
	param := map[*check.Var]bool{}
	for _, v := range law.Params {
		param[v] = true
	}
	var out []*check.Var
	syntax.Inspect(law.TopRewrite().Lhs, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if v, isVar := law.RewriteInfo().Uses[id].(*check.Var); isVar && param[v] && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return true
	})
	return out
}

// sitePure reports why the site m of a law over function parameters keeps
// its left side, "" when its function operands cannot show the fused
// order: each is a declared function or a function literal whose
// footprint is empty and which writes no variable it captures; and every
// operand but the first the left side evaluates is a name or a literal,
// whose evaluation no stage's defect can skip unseen.
func (b *irBuilder) sitePure(m rwMatch) string {
	info := b.l.info
	for i, v := range lawOrder(m.law) {
		if _, ok := v.Type.(*check.Signature); !ok {
			switch unparen(m.ops[v]).(type) {
			case *syntax.Ident, *syntax.BasicLit:
				continue
			}
			if i == 0 {
				continue
			}
			return "not applied: the operand of " + v.Name + " runs after a stage in the left side, and the fused form would run it first"
		}
		op := unparen(m.ops[v])
		switch x := op.(type) {
		case *syntax.Ident:
			if f, ok := info.Uses[x].(*check.Func); ok && f.Decl != nil && f.Pkg != nil && f.Pkg.Info != nil {
				if fp, ok := f.Pkg.Info.Footprints[f.Decl]; ok && fp.Empty() {
					continue
				}
			}
		case *syntax.FuncLit:
			if fp, ok := info.Footprints[x]; ok && fp.Empty() && !writesCaptured(info, x) {
				continue
			}
		}
		return "not applied: the operand of " + v.Name + " is not a pure function literal or function, so the fused order could show"
	}
	return ""
}

// writesCaptured reports a function literal that assigns a variable
// declared outside it (a captured let mut, or through one).
func writesCaptured(info *check.Info, lit *syntax.FuncLit) bool {
	found := false
	syntax.Inspect(lit.Body, func(n syntax.Node) bool {
		as, ok := n.(*syntax.AssignStmt)
		if !ok || found {
			return !found
		}
		for _, l := range as.Lhs {
			root := l
			for {
				switch y := root.(type) {
				case *syntax.SelectorExpr:
					root = y.X
					continue
				case *syntax.IndexExpr:
					root = y.X
					continue
				case *syntax.ParenExpr:
					root = y.X
					continue
				}
				break
			}
			id, ok := root.(*syntax.Ident)
			if !ok {
				found = true
				continue
			}
			if v, isVar := info.Uses[id].(*check.Var); isVar && (v.Pos < lit.Pos() || v.Pos >= lit.End()) {
				found = true
			}
		}
		return !found
	})
	return found
}

// fuseOr wraps a law over function parameters' right side at a site: the
// right side's statements and value under v1.FuseOr, whose second
// function is the left side at the same operands, run again on a defect.
func (b *irBuilder) fuseOr(m rwMatch, x *syntax.CallExpr, gt string, vals map[*check.Var]*irExpr, rpre []*irStmt, right *irExpr) *irExpr {
	p := b.pos(x, x.Pos())
	lpre, left := b.lawSide(m.law, m.law.TopRewrite().Lhs, vals, true)
	end := p
	typ := "func() " + gt
	rlit := &irExpr{K: "func", Pos: p, T: typ, Body: append(rpre, &irStmt{K: "ret", Pos: p, X: right}), End: &end}
	llit := &irExpr{K: "func", Pos: p, T: typ, Body: append(lpre, &irStmt{K: "ret", Pos: p, X: left}), End: &end}
	b.keepImport("var _ = " + b.l.rtRef("FuseOr") + "[" + gt + "]")
	return &irExpr{K: "pcall", Pos: p, T: v1.PackageOf("FuseOr"), Name: "FuseOr", V: gt, Args: []*irExpr{rlit, llit}}
}
