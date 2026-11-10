package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Result-alias summaries (OPT1, F2; types.md, Bindings, Result aliasing).
// A by-value argument read from a let mut root is copied at a call when a
// write through the root can follow the read (values.go, V2), unless the
// call keeps nothing of it. Before this rule the lowering knew that only
// for a pure call whose result type reaches no Map or slice type the
// argument's type reaches (mayShare); a type parameter hides its parts, so
// a generic call such as arena.Get(store, r) copied the whole store to
// read one element.
//
// resultDeps summarizes, for each parameter of a function with an Ergo
// body, whether its result can share a Map or slice with that parameter's
// value: whether a use of the parameter, or of a local or parameter it
// flows into, reaches a return. The flow follows paths (x.f, x[i],
// x[a:b], *x), conversions, try and catch, literals and tuples, lets,
// assignments (which taint the place's root, a parameter included), for
// loops over it, and calls: a call of a function with an Ergo body passes
// the flow on only at a parameter its own summary marks; a pure std body
// or a prelude intrinsic passes it to its result; any other call, a use in
// a function literal, a match on it, and a statement form the walk does
// not know count as reaching the result. A use whose value reaches no Map
// or slice type (a basic value, by its type) carries nothing. A recursive
// function is summarized as "every parameter" while it is being decided.
//
// At a pure call (values.go, classify) the summary decides each argument
// read from a root:
//   - the result does not depend on it: the read keeps nothing, so it is
//     not copied;
//   - it does: the result, not the argument, is copied, when a write
//     through the root can follow. A pure call has no other output, so a
//     copy of its result is as unshared as the result of a call on a copy
//     of the argument, and costs the result's size, not the argument's;
//     a result that holds no Map or slice at its instance costs nothing.
//
// Either way a write through the root that runs between the read and the
// call (in a later argument, f(c, bump(&c))) keeps the copy of the
// argument, since the callee would otherwise see the write (inFlight).
//
// A borrow passed to a generic function whose result type holds its type
// parameters: the callee copies what it returns through the borrow only
// at its own types, and a type parameter's parts it cannot copy. So the
// caller copies the result where its instance holds a Map or slice and the
// summary says it depends on the borrow (borrowResult).
//
// The reference lowering is aliasLowering "type": the summary is not read,
// and a pure call keeps nothing only by its types (mayShare), as before.
// Set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.aliasLowering=type.
// TestResultAliasAgrees runs its programs under both and against a model.
//
// Prior art: MLton and OCaml's flambda compute what a function's result
// can alias for unboxing and escape analysis; Swift's optimizer removes a
// copy of an array argument when the callee's effects summary shows it is
// only read (its "guaranteed" parameter convention) and copies on write
// otherwise; Rust's borrow checker expresses the dependency in the
// signature (a returned reference's lifetime names the parameter it
// borrows from). Gleam, Elixir and ReScript have immutable maps and lists,
// so a returned part is never written and no summary is needed.
var aliasLowering string

// resultDeps is f's summary (above), indexed as f.Sig.Params, a method's
// receiver first; every entry is true for a function with no Ergo body.
func (l *lowerer) resultDeps(f *check.Func) []bool {
	all := func() []bool {
		out := make([]bool, len(f.Sig.Params))
		for i := range out {
			out[i] = true
		}
		return out
	}
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		return all()
	}
	if l.deps == nil {
		l.deps = map[*check.Func][]bool{}
	}
	if deps, ok := l.deps[f]; ok {
		if deps == nil {
			return all() // being decided: recursion depends on everything
		}
		return deps
	}
	l.deps[f] = nil
	vars := paramVars(f)
	if len(vars) != len(f.Sig.Params) {
		delete(l.deps, f)
		return all()
	}
	fl := newFlow(l, f.Pkg.Info, d)
	out := make([]bool, len(vars))
	for i, v := range vars {
		if v != nil {
			out[i] = fl.reachesResult(v)
		}
	}
	l.deps[f] = out
	return out
}

// flow is the walk of one body for resultDeps.
type flow struct {
	l      *lowerer
	info   *check.Info
	parent map[syntax.Node]syntax.Node
	uses   map[*check.Var][]*syntax.Ident
	inLit  map[*syntax.Ident]bool // uses inside a function literal or trailing block
}

func newFlow(l *lowerer, info *check.Info, d *syntax.FuncDecl) *flow {
	fl := &flow{l: l, info: info, parent: map[syntax.Node]syntax.Node{}, uses: map[*check.Var][]*syntax.Ident{}, inLit: map[*syntax.Ident]bool{}}
	var visit func(n syntax.Node, lit bool)
	visit = func(n syntax.Node, lit bool) {
		switch x := n.(type) {
		case *syntax.FuncLit:
			lit = true
		case *syntax.CallExpr:
			if x.Trailing != nil {
				fl.parent[x.Trailing] = x
				visit(x.Trailing, true)
			}
		case *syntax.Ident:
			if v, ok := info.Uses[x].(*check.Var); ok {
				fl.uses[v] = append(fl.uses[v], x)
				fl.inLit[x] = lit
			}
		}
		for _, c := range children(n) {
			if call, ok := n.(*syntax.CallExpr); ok && c == syntax.Node(call.Trailing) {
				continue
			}
			fl.parent[c] = n
			visit(c, lit)
		}
	}
	visit(d.Body, false)
	return fl
}

// reachesResult reports whether the value of v, or of what it flows into,
// can reach a returned value.
func (fl *flow) reachesResult(v *check.Var) bool {
	seen := map[*check.Var]bool{v: true}
	work := []*check.Var{v}
	for len(work) > 0 {
		u := work[0]
		work = work[1:]
		for _, id := range fl.uses[u] {
			if fl.inLit[id] {
				return true
			}
			taints, escapes := fl.climb(id)
			if escapes {
				return true
			}
			for _, t := range taints {
				if !seen[t] {
					seen[t] = true
					work = append(work, t)
				}
			}
		}
	}
	return false
}

// carries reports whether a value of type t can hold a Map or slice: t
// reaches one, or has a part that hides its parts (a type parameter).
func (fl *flow) carries(t check.Type) bool {
	if t == nil {
		return true
	}
	set := map[string]bool{}
	return fl.l.reaches(t, set, map[check.Type]bool{}) || len(set) > 0
}

// climb follows the value of the use id up its expression: it reports the
// variables the value flows into, or that it can reach the result.
func (fl *flow) climb(id *syntax.Ident) (taints []*check.Var, escapes bool) {
	var cur syntax.Node = id
	for {
		if x, ok := cur.(syntax.Expr); ok {
			if t, typed := typedIn(fl.info, x); typed && !fl.carries(t) {
				return nil, false
			}
		}
		p := fl.parent[cur]
		switch p := p.(type) {
		case *syntax.ParenExpr, *syntax.StarExpr, *syntax.TryExpr, *syntax.CompositeLit, *syntax.KeyValueExpr, *syntax.TupleExpr, *syntax.BinaryExpr, *syntax.InterpolatedString:
			cur = p
			continue
		case *syntax.CatchExpr:
			if p.X != cur {
				return nil, true
			}
			cur = p
			continue
		case *syntax.SelectorExpr:
			if p.X != cur {
				return nil, true
			}
			if call, ok := fl.parent[p].(*syntax.CallExpr); ok && call.Fun == syntax.Expr(p) {
				if c := fl.info.Calls[call]; c != nil && c.Recv != nil && syntax.Node(c.Recv) == cur {
					next, stop, esc := fl.callFlow(call, c, cur)
					if stop || esc {
						return nil, esc
					}
					cur = next
					continue
				}
			}
			cur = p
			continue
		case *syntax.IndexExpr:
			if p.X != cur {
				return nil, false // an index or a Map key: a read reads no part of it
			}
			cur = p
			continue
		case *syntax.SliceExpr:
			if p.X != cur {
				return nil, false
			}
			cur = p
			continue
		case *syntax.CallExpr:
			c := fl.info.Calls[p]
			if c == nil {
				return nil, true
			}
			if p.Fun == cur {
				// A function value called: its result may be anything it
				// was made from.
				return nil, true
			}
			next, stop, esc := fl.callFlow(p, c, cur)
			if stop || esc {
				return nil, esc
			}
			cur = next
			continue
		case *syntax.VarSpec:
			for i, x := range p.Values {
				if syntax.Node(x) != cur {
					continue
				}
				if p.Pattern == nil && len(p.Names) == len(p.Values) {
					if v, ok := fl.info.Defs[p.Names[i]].(*check.Var); ok {
						return []*check.Var{v}, false
					}
					return nil, false // `let _ = x`
				}
				return fl.defsIn(p), false
			}
			return nil, true
		case *syntax.AssignStmt:
			for _, x := range p.Rhs {
				if syntax.Node(x) != cur {
					continue
				}
				for _, lhs := range p.Lhs {
					root := pathRoot(lhs)
					if root == nil {
						return nil, true
					}
					v, ok := fl.info.Uses[root].(*check.Var)
					if !ok {
						return nil, true
					}
					if _, local := fl.uses[v]; !local && !fl.declaredHere(v) {
						return nil, true // a package let, or a variable of another frame
					}
					taints = append(taints, v)
				}
				return taints, false
			}
			return nil, true
		case *syntax.RangeStmt:
			if p.X != cur {
				return nil, true
			}
			if lp := fl.info.Loops[p]; lp != nil {
				return lp.Bindings, false
			}
			return nil, true
		case *syntax.ExprStmt:
			if b, ok := fl.parent[p].(*syntax.BlockStmt); ok && len(b.List) > 0 && b.List[len(b.List)-1] != syntax.Stmt(p) {
				return nil, false // a discarded value
			}
			return nil, true // a block's value, which may be the result
		case *syntax.ReturnStmt:
			return nil, true
		default:
			return nil, true
		}
	}
}

// callFlow follows a value at the operand cur of the call x: next is the
// call, whose result carries it on; stop means the call keeps nothing of
// it; escapes means the call may keep it where the walk cannot follow.
func (fl *flow) callFlow(x *syntax.CallExpr, c *check.Call, cur syntax.Node) (next syntax.Node, stop, escapes bool) {
	list := callOperands(x, c)
	j := -1
	for i, a := range list {
		if syntax.Node(a) == cur {
			j = i
		}
	}
	switch c.Kind {
	case check.CallLen:
		return nil, true, false
	case check.CallConversion:
		return x, false, false
	case check.CallFunc:
	default:
		return nil, false, true
	}
	g := c.Func
	switch {
	case j < 0 || g == nil:
		return nil, false, true
	case g.Decl != nil && g.Decl.Body != nil && g.Decl.Extern == nil:
		deps := fl.l.resultDeps(g)
		if j >= len(deps) || deps[j] {
			return x, false, false
		}
		return nil, true, false
	case pureStdBody(g):
		// A reviewed pure Go body keeps nothing but its result.
		return x, false, false
	}
	return nil, false, true
}

// borrowResult reports whether the result of the call x is copied at the
// caller (above): the callee's declared result holds one of its type
// parameters, the result's instance here holds a Map or slice, and the
// summary says it depends on an operand that is a borrow. In both
// lowerings: it is a rule of values, not a choice of speed.
func (w *valueWalk) borrowResult(x *syntax.CallExpr) bool {
	c := w.info.Calls[x]
	if c == nil || c.Func == nil || len(c.Func.Sig.Results) != 1 || len(freeTypeParams(c.Func.Sig.Results[0])) == 0 {
		return false
	}
	if !w.l.holdsValue(w.typeOf(x)) {
		return false
	}
	deps := w.l.resultDeps(c.Func)
	for i, a := range callOperands(x, c) {
		if i >= len(deps) || !deps[i] {
			continue
		}
		if _, ptr := w.typeOf(a).(*check.Pointer); ptr {
			return true
		}
		if u, ok := unparen(a).(*syntax.UnaryExpr); ok && u.Op == syntax.AND {
			return true
		}
	}
	return false
}

// declaredHere reports whether v is bound in the walked body or is one of
// its parameters: a variable fl.uses names is one the body reads.
func (fl *flow) declaredHere(v *check.Var) bool {
	for _, n := range fl.parent {
		if s, ok := n.(*syntax.VarSpec); ok {
			for _, id := range s.Names {
				if fl.info.Defs[id] == v {
					return true
				}
			}
		}
	}
	return false
}

// defsIn are the variables a let with a pattern binds.
func (fl *flow) defsIn(s *syntax.VarSpec) []*check.Var {
	var out []*check.Var
	syntax.Inspect(s, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if v, ok := fl.info.Defs[id].(*check.Var); ok {
				out = append(out, v)
			}
		}
		return true
	})
	return out
}
