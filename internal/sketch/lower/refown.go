package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// A Ref owns its value (V6; owner q46, 2026-11-09; stdlib.md, Sync and
// STM 1). The value rule copies a parameter before any write to it, so a
// function given to Ref.Update or sync.Modify writes a copy of the held
// value: a Map of n entries costs O(n) per Update. A function literal
// written at the call is an owned site: its parameter takes the held value
// over, as a let mut local whose value nothing else reaches (freshIdent),
// so its body writes it in place and passes it, unshared, to an owned
// parameter (V5). The call lowers to v1.RefUpdateOwned or
// v1.RefModifyOwned, with two facts of the site:
//
//   - the copy function of T (cloneValue), which the cell calls under its
//     lock when the value it holds may be reached from outside (shared: a
//     value NewRef or Set stored, Get returned, or a plain Update or Modify
//     stored), so no value outside the cell ever sees an in-place write;
//   - fresh, the proof that every value the literal returns is unshared:
//     the stored T and Modify's R reach no Map or slice of each other or
//     of anything else live (valueWalk.fresh). Without it the cell marks
//     the stored value shared, and the next owned site copies it once.
//
// A site is owned only when T holds a Map or slice and is no type
// parameter (holdsValue), the function is a literal written at the call,
// and owned parameters lower this build (valueOwned): valueLowering "copy"
// and "alias" keep the plain call, the reference TestValueRuleAgrees holds
// the owned one to. Every other call (a function value, a generic site, a
// method value) is the plain Update or Modify: its function never writes
// its parameter in place, and the cell marks what it stores shared.
//
// Prior art: Swift's Array and Dictionary copy on write after a uniqueness
// test of their reference count (isKnownUniquelyReferenced), which the
// cell's shared flag stands in for, decided by a static proof where Swift
// counts at run time; Rust's Mutex<T> lends &mut T to the holder of the
// guard, and a panic poisons it; Clojure's transients and Haskell's ST
// write a value in place while one owner holds it. Gleam, Elixir and
// ReScript have no shared mutable cell in their std over mutable data:
// BEAM processes copy messages, and an Agent's state is a persistent map.
type refSite struct {
	lit   *syntax.FuncLit // nil for the mutant hand-nonliteral
	call  *syntax.CallExpr
	elem  check.Type // T
	fresh bool
}

// refCall records call as an owned site when it is one (above): its
// literal's parameter is handed over (handParams).
func (w *valueWalk) refCall(call *syntax.CallExpr) {
	if !valueOwned() {
		return
	}
	c := w.info.Calls[call]
	if c == nil {
		return
	}
	var fn syntax.Expr
	var elem check.Type
	switch {
	case c.Kind == check.CallOp && c.Op != nil && c.Op.Name == "Update" && len(call.Args) == 1:
		n, ok := w.typeOf(c.Recv).(*check.Named)
		if !ok || !isSyncRef(n) || len(n.TArgs) != 1 {
			return
		}
		fn, elem = call.Args[0], n.TArgs[0]
	case c.Kind == check.CallFunc && isSyncModify(c.Func) && c.Recv == nil && len(call.Args) == 2:
		for i, tp := range c.Func.Sig.TParams {
			if tp.Name == "T" && i < len(c.TypeArgs) {
				elem = c.TypeArgs[i]
			}
		}
		fn = call.Args[1]
	default:
		return
	}
	if elem == nil || !w.l.holdsValue(elem) || len(freeTypeParams(elem)) > 0 {
		return
	}
	lit, ok := unparen(fn).(*syntax.FuncLit)
	if !ok {
		if valueMutant == "hand-nonliteral" {
			w.sites = append(w.sites, &refSite{call: call, elem: elem})
		}
		return
	}
	s := &refSite{lit: lit, call: call, elem: elem}
	w.sites = append(w.sites, s)
	if w.handOver == nil {
		w.handOver = map[*syntax.FuncLit]bool{}
	}
	w.handOver[lit] = true
}

func isSyncRef(n *check.Named) bool {
	o := n.Obj
	if n.Orig != nil {
		o = n.Orig.Obj
	}
	return o != nil && o.Pkg != nil && o.Pkg.Path == "sync" && o.Pkg.Std() && o.Name == "Ref"
}

func isSyncModify(f *check.Func) bool {
	return f != nil && f.Pkg != nil && f.Pkg.Path == "sync" && f.Pkg.Std() && f.Name == "Modify" && f.Decl != nil && f.Decl.Extern != nil
}

// handParams makes the parameters of the owned literal lit, whose frame is
// f, locals of f that hold an unshared value (freshIdent).
func (w *valueWalk) handParams(lit *syntax.FuncLit, f *vframe) {
	if !w.handOver[lit] || lit.Type == nil || lit.Type.Params == nil {
		return
	}
	for _, field := range lit.Type.Params.List {
		for _, id := range field.Names {
			if v, ok := w.info.Defs[id].(*check.Var); ok {
				vv := w.varOf(v)
				vv.local, vv.handed, vv.frame, vv.loops = true, true, f, nil
			}
		}
	}
}

// decideSites proves, for each owned site, whether its literal returns
// only unshared values, once every copy of the body is decided.
func (w *valueWalk) decideSites() {
	for _, s := range w.sites {
		switch {
		case s.lit == nil || valueMutant == "store-unfresh":
			s.fresh = true
		default:
			s.fresh = w.frameResultsFresh(w.frameOf[s.lit], s.elem)
		}
		w.vf.refSites[s.call] = s
	}
}

// frameResultsFresh reports whether every value the frame f of an owned
// literal over the cell type elem returns, by its tails and its return
// statements, is fresh (resultFresh).
func (w *valueWalk) frameResultsFresh(f *vframe, elem check.Type) bool {
	if f == nil {
		return false
	}
	ok := true
	for x := range f.tails {
		if !w.resultFresh(x, elem) {
			ok = false
		}
	}
	syntax.Inspect(f.node, func(n syntax.Node) bool {
		if g := w.frameOf[n]; g != nil && g != f {
			return false
		}
		if r, isRet := n.(*syntax.ReturnStmt); isRet {
			for _, x := range r.Results {
				if !w.resultFresh(x, elem) {
					ok = false
				}
			}
		}
		return true
	})
	return ok
}

// resultFresh reports whether x, a value an owned literal returns, keeps
// the cell's value unshared: x is fresh, or x is Modify's tuple (R, T)
// whose T is fresh and whose R is fresh or of a type that reaches no Map
// or slice type of T (elem). R leaves to the caller, which treats the
// result of Modify as shared, so only a part of the stored T in R matters.
func (w *valueWalk) resultFresh(x syntax.Expr, elem check.Type) bool {
	if w.fresh(x) {
		return true
	}
	t, ok := unparen(x).(*syntax.TupleExpr)
	if !ok || len(t.Elts) != 2 {
		return false
	}
	r := t.Elts[0]
	return w.fresh(t.Elts[1]) && (w.fresh(r) || !w.l.mayShare(w.typeOf(r), elem))
}

// returnNode is the return statement or the tail of o's frame whose value
// the expression start, o's identifier or path, is a part of (through
// parentheses, tuples, record, slice and Map literals), or nil.
func (w *valueWalk) returnNode(o *vocc, start syntax.Node) syntax.Node {
	cur := start
	for {
		parent, child := w.unparenParent(cur)
		switch p := parent.(type) {
		case *syntax.TupleExpr, *syntax.CompositeLit, *syntax.KeyValueExpr:
			if kv, ok := p.(*syntax.KeyValueExpr); ok && kv.Value != child {
				return nil // a Map key keeps nothing past the literal
			}
			cur = p
			continue
		case *syntax.ReturnStmt:
			if w.frameOfReturn(p) == o.frame {
				return p
			}
		case *syntax.ExprStmt:
			if x, ok := child.(syntax.Expr); ok && w.frameOfTail(x) == o.frame {
				return x
			}
		}
		return nil
	}
}

// mayShare reports whether a value of type a can reach a Map or slice that
// a value of type b reaches: a Map or slice type, by its underlying type,
// that both reach, or a part of either that hides its parts (a function,
// an operation type, a type parameter, a borrow).
func (l *lowerer) mayShare(a, b check.Type) bool {
	sa, sb := map[string]bool{}, map[string]bool{}
	if l.reaches(a, sa, map[check.Type]bool{}) || l.reaches(b, sb, map[check.Type]bool{}) {
		return true
	}
	for k := range sa {
		if sb[k] {
			return true
		}
	}
	return false
}

// reaches adds to set the key of each Map and slice type a value of type t
// reaches, and reports whether t has a part that hides its parts.
func (l *lowerer) reaches(t check.Type, set map[string]bool, seen map[check.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch u := underEqual(t).(type) {
	case *check.Basic:
		return false
	case *check.Slice:
		set[l.typeKey(u)] = true
		return l.reaches(u.Elem, set, seen)
	case *check.Map:
		set[l.typeKey(u)] = true
		return l.reaches(u.Key, set, seen) || l.reaches(u.Elem, set, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if l.reaches(e, set, seen) {
				return true
			}
		}
		return false
	case *check.Struct:
		for _, f := range u.Fields {
			if l.reaches(f.Type, set, seen) {
				return true
			}
		}
		return false
	case *check.Union:
		for _, v := range u.Variants {
			if l.reaches(v, set, seen) {
				return true
			}
		}
		return false
	}
	return true
}

// ownedRefCall is the Go call of the owned site s (above): name is
// RefUpdateOwned or RefModifyOwned, recv the Ref operand and fn the
// function operand, typeArgs the Go type arguments.
func (fc *funcCtx) ownedRefCall(x *syntax.CallExpr, c *check.Call, s *refSite, name string, recv, fn syntax.Expr, typeArgs string, result bool) (string, bool, bool) {
	body, err := v1.BodyOf(name)
	if err != nil || len(body.Params) != 4 || body.Params[1].Func == nil {
		ice(x, "the owned Ref body %s is not readable: %v", name, err)
	}
	vals := fc.callArgs([]syntax.Expr{recv, fn}, x)
	vals[1] = fc.adaptFunc(vals[1], body.Params[1].Func, fc.argSig(fn, x), x)
	goT := fc.goType(s.elem)
	v := fc.scope.alloc("v")
	clone := "func(" + v + " " + goT + ") " + goT + " { return " + fc.cloneValue(s.elem, v, x) + " }"
	vals = append(vals, clone, strconv.FormatBool(s.fresh))
	return fc.runtimeCall(name, body, typeArgs, "", vals, fc.envExpr(x), x, c, x), result, false
}

// refSiteOf is the owned site of the call x, or nil.
func (fc *funcCtx) refSiteOf(x *syntax.CallExpr) *refSite {
	if fc.l.vals == nil || x == nil {
		return nil
	}
	return fc.l.vals.refSites[x]
}
