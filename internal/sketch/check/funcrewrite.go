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
type FuncRewrite struct {
	Lhs, Rhs           syntax.Expr
	Root               *Func
	LhsCalls, RhsCalls int
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
//     only the parameters, constants, such calls and operators;
//  2. fewer calls: the right side makes fewer calls than the left, so
//     rewriting ends and f(a, b) == f(b, a) is no rewrite;
//  3. no guard: no parameter takes a where (no given in v1);
//  4. pure: each function either side calls is an Ergo function with an
//     empty footprint and an empty needs clause; else a Go body in the
//     reviewed pure set;
//  5. value class: the sides' type has key equality and holds no pointer,
//     operation type, function, type parameter or Go type;
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
		if rw, at, why := c.funcRewrite(law); rw != nil {
			law.FuncRewrite = rw
		} else {
			c.errorf(at, CodeType, "law %s is no rewrite: %s; write { lhs == rhs } for a property", law.Name, why)
		}
	}
}

// funcRewrite is law's checked form, or the node and the reason of the
// first rule it breaks.
func (c *checker) funcRewrite(law *Law) (*FuncRewrite, syntax.Node, string) {
	info := c.pkg.Info
	lhs, rhs, _ := law.Decl.EquationSides()
	param := map[*Var]bool{}
	for _, v := range law.Params {
		param[v] = true
	}
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
			if v, ok := info.Uses[e].(*Var); ok && param[v] {
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
		case *syntax.CallExpr:
			call := info.Calls[e]
			if call == nil || call.Kind == CallFunc && call.Func == nil {
				return e, "a call of its sides has no function"
			}
			switch call.Kind {
			case CallFunc, CallConversion, CallLen:
			default:
				return e, "its sides call declared functions, conversions and len only, not an operation, a function value or another built-in form"
			}
			calls = append(calls, e)
			if call.Recv != nil {
				if bad, why := side(call.Recv, left); bad != nil {
					return bad, why
				}
			}
			for _, a := range e.Args {
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
	rw := &FuncRewrite{Lhs: lhs, Rhs: rhs, LhsCalls: len(lhsCalls), RhsCalls: len(rhsCalls)}
	for _, e := range lhsCalls {
		if call := info.Calls[e]; call.Kind == CallFunc {
			rw.Root = call.Func
			break
		}
	}
	if rw.Root == nil {
		return nil, lhs, "its left side calls no declared function"
	}
	// 2. Fewer calls.
	if rw.RhsCalls >= rw.LhsCalls {
		return nil, rhs, fmt.Sprintf("its right side makes %d calls and its left side %d; a rewrite makes fewer calls, so rewriting ends", rw.RhsCalls, rw.LhsCalls)
	}
	// 3. No guard.
	for i, w := range law.Where {
		if w != nil {
			return nil, law.Params[i].id, fmt.Sprintf("parameter %s takes a where, and a rewrite over functions has no guard in v1: a law true only under a guard stays a property", law.Params[i].Name)
		}
	}
	// 4. Pure.
	for _, e := range calls {
		if call := info.Calls[e]; call.Kind == CallFunc {
			if why := rewriteImpure(call); why != "" {
				return nil, e, why
			}
		}
	}
	// 5. Value class.
	if t := info.Types[unwrapTry(lhs)]; t != nil {
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
