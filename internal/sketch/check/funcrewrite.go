package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// FuncRewrite is the checked form of a top-level rewrite law over
// functions (types.md, Laws 14; LAWFFI r2 §3.2, owner q194 and q195):
// `law L(params) { lhs } == { rhs }`, in scope as a rewrite. Root is the
// function the left side is about: the first declared function the left
// side calls, outermost first. LhsCalls and RhsCalls count each side's
// calls (functions, conversions and len).
//
// A rewrite over function parameters (owner q234, "Extend laws"; FUSION)
// has type parameters of its own, a parameter of a function type, or a
// function literal on its right side: `law TransformFuses[T, U, V](xs []T,
// f func(x T) U, g func(x U) V) { Transform(Transform(xs, f), g) } == {
// Transform(xs, func(x T) V { g(f(x)) }) }`. Such a law takes
// Law.FuncsRewrite, not Law.FuncRewrite: its sites are matched from the
// call's source (lower's ir_fusion.go), not by the pass's IR patterns.
// Law.TopRewrite reads either.
type FuncRewrite struct {
	Lhs, Rhs           syntax.Expr
	Root               *Func
	LhsCalls, RhsCalls int
	// OverFuncs reports a rewrite over function parameters.
	OverFuncs bool
}

// TopRewrite is law's rewrite: over values (FuncRewrite) or over function
// parameters (FuncsRewrite); nil when the law is no rewrite in scope. The
// licence run, the ledger and the build read every rewrite through it.
func (law *Law) TopRewrite() *FuncRewrite {
	if law.FuncRewrite != nil {
		return law.FuncRewrite
	}
	return law.FuncsRewrite
}

// checkFuncRewrites checks the scope of every top-level rewrite law of the
// package, after the footprints (a side's functions are pure): the law
// takes Law.FuncRewrite, or S108 names the first rule it breaks. A law
// over Ergo bodies only is in scope as one over Go functions is (owner
// q195). The rules, in the order the diagnostic reads them (LAWFFI r2
// §3.2):
//  1. shape: the left side is a call tree of declared functions,
//     conversions and len over the parameters and constants, with try and
//     parentheses, and every parameter occurs in it; the right side uses
//     only the parameters, constants, such calls and operators, and
//     function literals of one expression over their own parameters and
//     the law's, which call a function parameter;
//  2. fewer rewritable calls: the pair (calls of Ergo bodies, calls) is
//     smaller on the right, compared in order (LAWFFI LF2m), so rewriting
//     ends and f(a, b) == f(b, a) is no rewrite; a Go body is a leaf; a
//     call of a function parameter is no declared function's and is not
//     counted;
//  3. no guard: no parameter takes a where (no given in v1);
//  4. pure: each function either side calls is an Ergo function with an
//     empty footprint and an empty needs clause; else a Go body in the
//     reviewed pure set, or an effect-free body of the module's own Go
//     (LAWFFI LF2m), which only the law's validated entry makes pure; a
//     function parameter's type fails nothing and needs nothing;
//  5. value class: the sides' type has key equality and holds no pointer,
//     operation type, function, type parameter or Go type, with each of
//     the law's own type parameters at int (a site's types are checked
//     where it is matched);
//  6. failures: the right side fails nothing the left side does not;
//  7. no orphan: the law is declared in the package of its root function.
//
// The law stays a property either way: it is checked, run and reported as
// lhs == rhs. Only its use as a rewrite is refused.
func (c *checker) checkFuncRewrites() {
	for _, law := range c.pkg.Laws {
		if law.Owner != nil || law.Machine != nil || law.Decl == nil || law.Decl.Equation == nil {
			continue
		}
		c.cur = law.file
		if rw, at, why := c.funcRewrite(law); rw != nil && rw.OverFuncs {
			law.FuncsRewrite = rw
		} else if rw != nil {
			law.FuncRewrite = rw
		} else {
			c.errorf(at, CodeType, "law %s is no rewrite: %s; write { lhs == rhs } for a property", law.Name, why)
		}
	}
	c.checkClaimRewrites()
}

// funcRewrite is law's checked form, or the node and the reason of the
// first rule it breaks.
func (c *checker) funcRewrite(law *Law) (*FuncRewrite, syntax.Node, string) {
	info := law.RewriteInfo()
	lhs, rhs := law.rewriteSides()
	if lhs == nil || rhs == nil {
		return nil, law.Decl, "each side is one expression"
	}
	param := map[*Var]bool{}
	for _, v := range law.Params {
		param[v] = true
	}
	local := map[*Var]bool{} // the parameters of a right side's literals
	literal := false
	var calls []*syntax.CallExpr
	var side func(x syntax.Expr, left bool) (syntax.Node, string)
	side = func(x syntax.Expr, left bool) (syntax.Node, string) {
		switch e := x.(type) {
		case *syntax.ParenExpr:
			return side(e.X, left)
		case *syntax.TryExpr:
			return side(e.X, left)
		case *syntax.BasicLit:
			return nil, ""
		case *syntax.Ident:
			if v, ok := info.Uses[e].(*Var); ok && (param[v] || local[v]) {
				return nil, ""
			}
			return e, fmt.Sprintf("its sides use only the parameters, constants and calls of functions, and %s is none of them", e.Name)
		case *syntax.UnaryExpr:
			if left {
				return e, "its left side is a call tree, and an operator is not a call"
			}
			return side(e.X, left)
		case *syntax.BinaryExpr:
			if left {
				return e, "its left side is a call tree, and an operator is not a call"
			}
			if bad, why := side(e.X, left); bad != nil {
				return bad, why
			}
			return side(e.Y, left)
		case *syntax.FuncLit:
			// A function literal on the right side (owner q234): one
			// expression over its own parameters and the law's, which a
			// fused loop runs per element.
			if left {
				return e, "its left side is a call tree, and a function literal is not a call"
			}
			literal = true
			body := e.Body
			if body == nil || len(body.List) != 1 {
				return e, "a function literal of its right side is one expression"
			}
			s, ok := body.List[0].(*syntax.ExprStmt)
			if !ok {
				return e, "a function literal of its right side is one expression"
			}
			if e.Type != nil && e.Type.Params != nil {
				for _, f := range e.Type.Params.List {
					for _, id := range f.Names {
						if v, ok := info.Defs[id].(*Var); ok {
							local[v] = true
						}
					}
				}
			}
			return side(s.X, left)
		case *syntax.CallExpr:
			call := info.Calls[e]
			if call == nil || call.Kind == CallFunc && call.Func == nil {
				return e, "a call of its sides has no function"
			}
			if call.Kind == CallFuncValue && !left {
				// A call of a function parameter, in a literal of the
				// right side: no declared function, so no call the
				// rewrite counts.
				id, ok := unparen(e.Fun).(*syntax.Ident)
				if v, isVar := info.Uses[id].(*Var); !ok || !isVar || !param[v] {
					return e, "its right side calls a function value only when it is a parameter of the law"
				}
				for _, a := range e.Args {
					if bad, why := side(a, left); bad != nil {
						return bad, why
					}
				}
				return nil, ""
			}
			switch call.Kind {
			case CallFunc, CallConversion, CallLen:
			default:
				return e, "its sides call declared functions, conversions and len only, not an operation, a function value or another built-in form"
			}
			calls = append(calls, e)
			for _, a := range call.Operands(e) {
				if bad, why := side(a, left); bad != nil {
					return bad, why
				}
			}
			return nil, ""
		}
		return x, "its sides are calls of functions over the parameters and constants"
	}
	// 1. Shape.
	if bad, why := side(lhs, true); bad != nil {
		return nil, bad, why
	}
	lhsCalls := calls
	if bad, why := side(rhs, false); bad != nil {
		return nil, bad, why
	}
	rhsCalls := calls[len(lhsCalls):]
	for _, v := range law.Params {
		if !usesVar(info, lhs, v) {
			return nil, v.id, fmt.Sprintf("parameter %s does not occur in its left side, which binds every parameter", v.Name)
		}
	}
	rw := &FuncRewrite{Lhs: lhs, Rhs: rhs, LhsCalls: len(lhsCalls), RhsCalls: len(rhsCalls), OverFuncs: law.ClaimEquation() || len(law.TParams) > 0 || len(local) > 0 || literal}
	for _, v := range law.Params {
		if _, ok := under(v.Type).(*Signature); ok {
			rw.OverFuncs = true
		}
	}
	for _, e := range lhsCalls {
		if call := info.Calls[e]; call.Kind == CallFunc {
			rw.Root = call.Func
			break
		}
	}
	if rw.Root == nil {
		return nil, lhs, "its left side calls no declared function"
	}
	// 2. Fewer rewritable calls (LAWFFI LF2m): the pair (calls of Ergo
	// bodies, calls) is smaller on the right, compared in order. A Go
	// body is a leaf the rewrite never unfolds, so { ref(s) } == { body(s) }
	// is a rewrite, and so is { Unquote(Quote(s)) } == { s } over Go
	// functions; each rewrite makes the pair smaller, so rewriting ends.
	lhsErgo, rhsErgo := ergoCalls(info, lhsCalls), ergoCalls(info, rhsCalls)
	if rhsErgo > lhsErgo || rhsErgo == lhsErgo && rw.RhsCalls >= rw.LhsCalls {
		return nil, rhs, fmt.Sprintf("its right side makes %d calls, %d of Ergo bodies, and its left side %d, %d of Ergo bodies; a rewrite makes fewer calls of Ergo bodies, or as many and fewer calls, so rewriting ends", rw.RhsCalls, rhsErgo, rw.LhsCalls, lhsErgo)
	}
	// 3. No guard.
	for i, w := range law.Where {
		if w != nil {
			return nil, law.Params[i].id, fmt.Sprintf("parameter %s takes a where, and a rewrite over functions has no guard in v1: a law true only under a guard stays a property", law.Params[i].Name)
		}
	}
	// 4. Pure: a function parameter's type fails nothing and needs nothing
	// (owner q234), so a site's function is pure and a fused loop may run
	// it in another order.
	for _, v := range law.Params {
		if sig, ok := under(law.RewriteType(v.Type)).(*Signature); ok && (sig.Errors != nil && !sig.Errors.Empty() || sig.Uses != nil && !sig.Uses.Empty()) {
			return nil, v.id, fmt.Sprintf("parameter %s is a function that fails or needs, and a rewrite's functions are pure: write its type with no fails and no needs", v.Name)
		}
	}
	for _, e := range calls {
		if call := info.Calls[e]; call.Kind == CallFunc {
			if rw.OverFuncs && pureOverArgs(info, call, e) {
				continue
			}
			if why := rewriteImpure(call); why != "" {
				return nil, e, why
			}
		}
	}
	// 5. Value class.
	if t := info.Types[unwrapTry(lhs)]; t != nil {
		// A law's own type parameter is the type a site gives it: the
		// class is the law's at value types (int here), and the site's
		// types are checked where it is matched.
		if claim := law.ClaimRewriteBinding(); claim != nil {
			// Read the original law's own parameters before closing Self.
			original := *law
			original.TParams = law.TParams[len(claim.Type.TParams):]
			read, why := claim.Subst(&original)
			if why != "" {
				return nil, lhs, why
			}
			t = read(law.Pkg.Info.Types[unwrapTry(lhs)])
			if r := read(law.Pkg.Info.Types[unwrapTry(rhs)]); !c.identical(t, r) {
				return nil, rhs, fmt.Sprintf("its sides have different types %s and %s", t, r)
			}
		} else if len(law.TParams) > 0 {
			m := map[*TypeParam]Type{}
			for _, tp := range law.TParams {
				m[tp] = Typ[Int]
			}
			t = c.subst(t, m)
		}
		if why := rewriteValueWhy(t, map[Type]bool{}); why != "" {
			return nil, lhs, fmt.Sprintf("its sides are values of %s, which %s; a rewrite is of values with key equality", t, why)
		}
	}
	// 6. Failures.
	row := func(calls []*syntax.CallExpr) *Row {
		r := newRow()
		for _, e := range calls {
			if f := info.Calls[e].Fails; f != nil {
				r = r.union(c.types, f)
			}
		}
		return c.types.resolve(r).known(c.types)
	}
	if left, right := row(lhsCalls), row(rhsCalls); !right.subsetOf(c.types, left) {
		return nil, rhs, fmt.Sprintf("its right side fails %s and its left side %s; a rewrite never adds a failure", rowText(right), rowText(left))
	}
	// 7. No orphan.
	if rw.Root.Pkg != c.pkg {
		return nil, law.Decl.Name, fmt.Sprintf("its root function %s is declared in package %s; state the law there, beside it (no orphan rewrite)", rw.Root.Name, rw.Root.Pkg.Path)
	}
	return rw, nil, ""
}

// ergoCalls counts the calls of calls whose function has an Ergo body:
// not an extern, not a Go function, not a conversion or len.
func ergoCalls(info *Info, calls []*syntax.CallExpr) int {
	n := 0
	for _, e := range calls {
		c := info.Calls[e]
		if c.Kind == CallFunc && c.Func != nil && c.Func.Decl != nil && c.Func.Decl.Extern == nil && c.Func.Decl.Body != nil && !c.Func.Pkg.Host() {
			n++
		}
	}
	return n
}

// usesVar reports whether x names v.
func usesVar(info *Info, x syntax.Expr, v *Var) bool {
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && info.Uses[id] == v {
			found = true
		}
		return !found
	})
	return found
}

// unwrapTry is x without its parentheses and a leading try.
func unwrapTry(x syntax.Expr) syntax.Expr {
	for {
		switch e := x.(type) {
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.TryExpr:
			x = e.X
		default:
			return x
		}
	}
}

// rewriteImpure is why the function call calls is not pure for a
// rewrite, "" when it is: an Ergo function with an empty footprint and an
// empty needs clause; else a Go body that the reviewed pure set holds
// (pureStd).
func rewriteImpure(call *Call) string {
	f := call.Func
	if call.Needs != nil && !call.Needs.Empty() {
		return fmt.Sprintf("%s needs %s, and a rewrite's functions are pure", f.Name, rowText(call.Needs))
	}
	if f.Decl != nil && f.Decl.Extern != nil || f.Pkg.Host() {
		if _, own := ModuleBody(f); own && !pureStd(f) {
			// A module's own Go body (LAWFFI LF2m) with an empty needs
			// row: the checker bound it as effect-free (moduleDoor; one
			// with effects declares go.Foreign, refused above). The
			// lowering admits the rewrite only on the law's current
			// validated entry, so the law makes the body pure, never a
			// review.
			return ""
		}
		if !pureStd(f) {
			return fmt.Sprintf("%s is a Go function outside the reviewed pure set, and a rewrite's functions are pure", f.Name)
		}
		return ""
	}
	if f.Pkg == nil || f.Pkg.Info == nil || f.Decl == nil {
		return fmt.Sprintf("%s has no body the checker sees, and a rewrite's functions are pure", f.Name)
	}
	fp, ok := f.Pkg.Info.Footprints[f.Decl]
	if !ok || !fp.Empty() {
		return impureCall("function", f, fp) + ", and a rewrite's functions are pure"
	}
	return ""
}

// pureOverArgs reports an Ergo function whose only unknown code is its
// function parameters (Footprint.Params, rows.md Footprints 3), called in
// a rewrite over functions with an argument at each of them whose type
// fails nothing and needs nothing: a call with such arguments is pure, as
// the footprint of a call with known arguments joins theirs (owner q234).
func pureOverArgs(info *Info, call *Call, e *syntax.CallExpr) bool {
	f := call.Func
	if call.Needs != nil && !call.Needs.Empty() || f.Pkg == nil || f.Pkg.Info == nil || f.Decl == nil || f.Decl.Extern != nil || f.Pkg.Host() {
		return false
	}
	fp, ok := f.Pkg.Info.Footprints[f.Decl]
	if !ok || len(fp.Caps) > 0 || fp.Tasks || fp.OpaqueRest {
		return false
	}
	args := call.Operands(e)
	for _, i := range fp.Params {
		if i >= len(args) {
			return false
		}
		sig, ok := under(info.Types[args[i]]).(*Signature)
		if !ok || sig.Errors != nil && !sig.Errors.Empty() || sig.Uses != nil && !sig.Uses.Empty() {
			return false
		}
	}
	return true
}

// rewriteValueWhy is why t is not of the value class a rewrite compares
// (key equality, no identity, nothing foreign), "" when it is.
func rewriteValueWhy(t Type, seen map[Type]bool) string {
	if !Equatable(t) {
		return "has no equality"
	}
	if n, ok := t.(*Named); ok && n.Obj != nil && n.Obj.Pkg.Host() {
		return "is a Go type"
	}
	if seen[t] {
		return ""
	}
	seen[t] = true
	switch u := under(t).(type) {
	case *Pointer:
		return "holds a pointer"
	case *TypeParam:
		return "is a type parameter"
	case *Interface:
		return "holds an operation type"
	case *Signature:
		return "holds a function"
	case *Slice:
		return rewriteValueWhy(u.Elem, seen)
	case *Array:
		return rewriteValueWhy(u.Elem, seen)
	case *Map:
		if why := rewriteValueWhy(u.Key, seen); why != "" {
			return why
		}
		return rewriteValueWhy(u.Elem, seen)
	case *Struct:
		for _, f := range u.Fields {
			if why := rewriteValueWhy(f.Type, seen); why != "" {
				return why
			}
		}
	case *Tuple:
		for _, e := range u.Types {
			if why := rewriteValueWhy(e, seen); why != "" {
				return why
			}
		}
	case *Union:
		for _, v := range u.Variants {
			if why := rewriteValueWhy(v, seen); why != "" {
				return why
			}
		}
	}
	return ""
}
