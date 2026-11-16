package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Kept-argument summaries (KEEP1; types.md, Bindings 2). A by-value
// argument read from a let mut or a borrow root is copied at a call when a
// write through the root can follow the read (values.go, V2), unless the
// call keeps nothing of it. Before this rule the lowering knew that only
// for a pure call (and, with the result-alias summary, alias.go, for the
// part of a pure call's result). So `Apply(s, p.out, u)` followed by
// `p.Clear()` copied the event list, though Apply only reads it.
//
// keeps summarizes, for each parameter of a function with an Ergo body,
// whether the function may keep a Map or slice of the parameter's value
// past the call: whether a use of the parameter, or of a local it flows
// into, reaches a place the walk cannot follow. The walk is alias.go's
// flow (climb) with three changes:
//   - a call of a function with an Ergo body stops the flow at a parameter
//     its own summary keeps nothing of, and passes it on to the call's
//     result where the result-alias summary says the result depends on it;
//     any other call (a function value, an operation, a Go function)
//     keeps it;
//   - a match on it passes it on to the values the cases bind;
//   - a store into a place rooted at a parameter (a borrow) keeps it, and
//     so does a return, a use in a function literal or trailing block, and
//     any statement form the walk does not know.
//
// A recursive function keeps every parameter while it is decided.
//
// At a call (values.go, classify), an argument read from a root keeps
// nothing when the callee keeps nothing of its parameter and its result
// does not depend on it (or holds no Map or slice). The callee can see a
// write to the root during the call only through the call itself, so the
// read is still copied (decide, callWrites) when:
//   - the root is written in a frame other than the read's (a local
//     function literal that writes it may run during the call); or
//   - an operand of the call is a function value or a trailing block (a
//     function a caller passed in may write what a borrow points to), or
//     names the root again (`f(xs, &xs)`, the implicit borrow of a dot
//     call's receiver, a write in another operand, a borrow passed on).
//
// Nothing else reaches the root during the call: a borrow is never stored
// and two borrows of one call never overlap (Borrows 1, 2), an operation
// value converted from a borrow lives only in the call that converted it,
// and an escaping frame never writes a let mut it captures (Function
// values 3).
//
// The reference lowering is keepLowering "copy", and valueLowering "copy"
// (valueOwned) turns it off too: the summary is not read, and such an
// argument is copied as before. TestValueRuleAgrees runs the value
// programs under the rule and the copying reference against the value
// model, counts the unkept arguments of each, and checks the promised
// form (flushS hands its slice to note1 with no copy).
//
// Prior art: Swift passes an argument +0 ("guaranteed") and its optimizer
// removes the caller's retain and copy when the callee's escape summary
// shows the argument does not escape; Rust says it in the signature (a
// shared borrow `&[T]` that the callee cannot keep past its lifetime);
// Mutable value semantics (Hylo) makes every parameter a borrow that
// cannot escape by default. Gleam, Elixir and ReScript have immutable lists
// and maps, so an argument is never copied and needs no summary.
var keepLowering string

// keeps is f's summary (above), indexed as f.Sig.Params; every entry is
// true for a function with no Ergo body.
func (l *lowerer) keeps(f *check.Func) []bool {
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
	if l.kept == nil {
		l.kept = map[*check.Func][]bool{}
	}
	if k, ok := l.kept[f]; ok {
		if k == nil {
			return all() // being decided: recursion keeps everything
		}
		return k
	}
	l.kept[f] = nil
	vars := paramVars(f)
	if len(vars) != len(f.Sig.Params) {
		delete(l.kept, f)
		return all()
	}
	fl := newFlow(l, f.Pkg.Info, d)
	fl.keep = true
	fl.params = map[*check.Var]bool{}
	for _, v := range vars {
		if v != nil {
			fl.params[v] = true
		}
	}
	out := make([]bool, len(vars))
	for i, v := range vars {
		if v != nil {
			out[i] = fl.reachesResult(v)
		}
	}
	l.kept[f] = out
	return out
}

// keepFlow is callFlow for keeps: a call of a function with an Ergo body
// stops the flow at a parameter it keeps nothing of, or passes it on to
// its result; any other call keeps it.
func (fl *flow) keepFlow(x *syntax.CallExpr, c *check.Call, cur syntax.Node) (next syntax.Node, stop, escapes bool) {
	j := -1
	for i, a := range callOperands(x, c) {
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
	if j < 0 || g == nil || g.Decl == nil || g.Decl.Body == nil || g.Decl.Extern != nil {
		return nil, false, true
	}
	if k := fl.l.keeps(g); j >= len(k) || k[j] {
		return nil, false, true
	}
	t, typed := typedIn(fl.info, x)
	if typed && (t == check.NoValue || !fl.carries(t)) {
		return nil, true, false // a result that holds no part of it
	}
	if deps := fl.l.resultDeps(g); j >= len(deps) || deps[j] {
		return x, false, false
	}
	return nil, true, false
}

// matchBinds are the variables the cases of the match s bind.
func (fl *flow) matchBinds(s *syntax.TypeSwitchStmt) []*check.Var {
	var out []*check.Var
	for _, cl := range s.Clauses {
		for _, x := range cl.List {
			syntax.Inspect(x, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok {
					if v, ok := fl.info.Defs[id].(*check.Var); ok {
						out = append(out, v)
					}
				}
				return true
			})
		}
	}
	return out
}

// unkept reports whether the call x, at which child is an operand read
// from a root, keeps nothing of it by the callee's summaries (above).
func (w *valueWalk) unkept(x *syntax.CallExpr, c *check.Call, child syntax.Node) bool {
	if keepLowering == "copy" || !valueOwned() || c.Kind != check.CallFunc || c.Func == nil {
		return false
	}
	j := operandIndex(x, c, child)
	if j < 0 || w.l.ownedAt(c.Func, j) {
		return false
	}
	if k := w.l.keeps(c.Func); j >= len(k) || k[j] {
		return false
	}
	deps := w.l.resultDeps(c.Func)
	return j < len(deps) && (!deps[j] || (!w.l.holdsValue(w.typeOf(x)) && !hidesParts(w.typeOf(x))))
}

// callWrites reports whether the callee of o.call could see a write to the
// root vv during the call (above), so the unkept read o is copied.
func (w *valueWalk) callWrites(vv *vvar, o *vocc) bool {
	for _, wr := range vv.writes {
		if wr.frame != o.frame {
			return true
		}
	}
	if o.call.Trailing != nil {
		return true
	}
	c := w.info.Calls[o.call]
	for _, a := range callOperands(o.call, c) {
		if _, fn := underlyingOf(w.typeOf(a)).(*check.Signature); fn {
			return true
		}
		if w.names(a, vv.v, o.id) {
			return true // another operand names the root: a borrow passed on
		}
	}
	return false
}

// names reports whether x names v anywhere but at the use except.
func (w *valueWalk) names(x syntax.Expr, v *check.Var, except *syntax.Ident) bool {
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && id != except && w.info.Uses[id] == v {
			found = true
		}
		return !found
	})
	return found
}

// underlyingOf is t's underlying type.
func underlyingOf(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		return n.Underlying()
	}
	return t
}
