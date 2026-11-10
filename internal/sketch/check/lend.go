package check

import (
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// lendPoint is a release position, not an evaluation position. A path is
// in declaration order, even for a keyed literal written in another order.
// A nil scope denotes an unknown owner outside this frame.
type lendPoint struct {
	scope    syntax.Node
	order    int
	path     []int
	v        *Var
	external bool
}

type resourceLoan struct {
	root *Var
	end  lendPoint
	// Internal loans travel with a whole aggregate. Their root and keeper
	// paths are relative to that aggregate, and guard partial field takes.
	internal bool
	keeper   []int
}

type lendWalk struct {
	parent map[syntax.Node]syntax.Node
	points map[*Var]lendPoint
	loans  map[syntax.Node][]resourceLoan
}

func (w *ownWalk) initLends(body *syntax.BlockStmt) {
	if w.lending != nil {
		return
	}
	l := &lendWalk{parent: map[syntax.Node]syntax.Node{}, points: map[*Var]lendPoint{}, loans: map[syntax.Node][]resourceLoan{}}
	var walk func(syntax.Node, syntax.Node, syntax.Node)
	walk = func(n, parent, scope syntax.Node) {
		if n == nil {
			return
		}
		l.parent[n] = parent
		switch n.(type) {
		case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CatchArm:
			scope = n
		}
		if id, ok := n.(*syntax.Ident); ok {
			if v, ok := w.info.Defs[id].(*Var); ok {
				l.points[v] = lendPoint{scope: scope, order: int(id.Pos()), v: v}
			}
		}
		syntax.Inspect(n, func(child syntax.Node) bool {
			if child == n {
				return true
			}
			if child != nil {
				walk(child, n, scope)
			}
			return false
		})
	}
	walk(body, nil, body)
	w.lending = l
}

func (w *ownWalk) inside(scope, outer syntax.Node) bool {
	for n := scope; n != nil; n = w.lending.parent[n] {
		if n == outer {
			return true
		}
	}
	return false
}

func (w *ownWalk) releasesBefore(keeper, root lendPoint) bool {
	if keeper.scope == nil {
		return false
	}
	if root.scope == nil {
		return false
	}
	if keeper.external && keeper.scope != root.scope {
		return false
	}
	if keeper.scope != root.scope {
		return w.inside(keeper.scope, root.scope)
	}
	if keeper.order != root.order {
		return keeper.order > root.order
	}
	for i, k := range keeper.path {
		if i >= len(root.path) {
			return false
		}
		if k != root.path[i] {
			return k > root.path[i]
		}
	}
	return false
}

// placeRoot follows fields, indexes and iteration/match bindings back to
// their root. The binding's source slot is kept even through nested frames.
func (w *ownWalk) placeRoot(x syntax.Expr) (*Var, int, bool) {
	switch x := unparen(x).(type) {
	case *syntax.SelectorExpr:
		return w.placeRoot(x.X)
	case *syntax.IndexExpr:
		return w.placeRoot(x.X)
	case *syntax.Ident:
		v, _ := w.info.Uses[x].(*Var)
		i, ok := w.slot[v]
		for ok && w.vars[i].root != nil {
			v = w.vars[i].root
			i, ok = w.slot[v]
		}
		return v, i, ok
	}
	return nil, 0, false
}

func (w *ownWalk) point(v *Var, i int) lendPoint {
	if end := w.vars[i].lendEnd; end != nil {
		return *end
	}
	p := w.lending.points[v]
	if w.vars[i].lent || w.vars[i].depth != w.depth {
		return lendPoint{scope: w.lendFrame, order: -1, v: v}
	}
	if v != nil && v.param {
		return lendPoint{scope: w.lendFrame, order: 0, v: v}
	}
	return p
}

func (w *ownWalk) placePoint(x syntax.Expr) lendPoint {
	v, _, ok := w.placeRoot(x)
	if !ok {
		return lendPoint{}
	}
	p := w.lending.points[v]
	if sel, ok := unparen(x).(*syntax.SelectorExpr); ok {
		if k := w.fieldIndex(sel); k >= 0 {
			p.path = append(slices.Clone(p.path), k)
		}
	}
	return p
}

func (w *ownWalk) argument(x *syntax.CallExpr, index int) syntax.Expr {
	c := w.info.Calls[x]
	if c != nil && c.Kind == CallFunc && c.Recv != nil {
		if index == 0 {
			return c.Recv
		}
		index--
	}
	if index >= 0 && index < len(x.Args) {
		return x.Args[index]
	}
	return nil
}

// destination follows value transport only. An ordinary call does not
// promise to keep a callback in a particular owner; unknown transport is
// refused rather than treating a syntactic enclosing let as its owner.
func (w *ownWalk) destination(n syntax.Node) lendPoint {
	p := w.lending.parent[n]
	switch p := p.(type) {
	case *syntax.ParenExpr, *syntax.TryExpr, *syntax.KeyValueExpr:
		return w.destination(p)
	case *syntax.VarSpec:
		for i, x := range p.Values {
			if x == n && i < len(p.Names) {
				v, _ := w.info.Defs[p.Names[i]].(*Var)
				return w.lending.points[v]
			}
		}
	case *syntax.AssignStmt:
		for i, x := range p.Rhs {
			if x == n && i < len(p.Lhs) {
				return w.placePoint(p.Lhs[i])
			}
		}
	case *syntax.CompositeLit:
		d := w.destination(p)
		if d.scope == nil {
			d = lendPoint{scope: p, external: true}
		} // the aggregate's internal release order
		for i, e := range p.Elts {
			if e != n {
				continue
			}
			k := i
			if kv, ok := e.(*syntax.KeyValueExpr); ok {
				if id, ok := kv.Key.(*syntax.Ident); ok {
					if s, ok := under(w.info.Types[p]).(*Struct); ok {
						for j, f := range s.Fields {
							if f.Name == id.Name {
								k = j
								break
							}
						}
					}
				}
			}
			d.path = append(slices.Clone(d.path), k)
			return d
		}
	case *syntax.TupleExpr:
		d := w.destination(p)
		if d.scope == nil {
			d = lendPoint{scope: p, external: true}
		}
		for i, e := range p.Elts {
			if e == n {
				d.path = append(slices.Clone(d.path), i)
				return d
			}
		}
	case *syntax.CallExpr:
		c := w.info.Calls[p]
		if c != nil {
			if _, _, ok := movingBody(c); ok {
				if k, ok := callbackKeeper(c); ok {
					if k < 0 {
						return w.destination(p)
					}
					if a := w.argument(p, k); a != nil {
						return w.placePoint(a)
					}
				}
			}
			if c.Kind == CallWith && p.Trailing == n {
				return w.destination(p)
			}
		}
	case *syntax.ExprStmt:
		if _, ok := w.info.Tails[p]; ok {
			return w.destination(w.lending.parent[p])
		}
	}
	return lendPoint{}
}

// placedRoot admits R29 only after the root has been evaluated into an
// earlier literal element. Declaration order is checked separately by the
// release relation. A future element cannot lend an unacquired field.
func (w *ownWalk) placedRoot(key syntax.Node, v *Var) (lendPoint, bool) {
	return w.literalRoot(key, v, true)
}

func (w *ownWalk) literalRoot(key syntax.Node, v *Var, before bool) (lendPoint, bool) {
	for n := key; n != nil; n = w.lending.parent[n] {
		lit, ok := n.(*syntax.CompositeLit)
		if !ok {
			continue
		}
		for _, e := range lit.Elts {
			x := e
			if kv, ok := e.(*syntax.KeyValueExpr); ok {
				x = kv.Value
			}
			id, ok := unparen(x).(*syntax.Ident)
			if !ok || w.info.Uses[id] != v || before && id.End() > key.Pos() {
				continue
			}
			return w.destination(id), true
		}
	}
	return lendPoint{}, false
}

func callbackKeeper(c *Call) (int, bool) {
	if c == nil || c.Kind != CallFunc || c.Func == nil || c.Func.Pkg == nil || !c.Func.Pkg.Std() || c.Func.Decl == nil || c.Func.Decl.Extern == nil {
		return 0, false
	}
	path, name := c.Func.Decl.Extern.Split()
	if path != runtimeModule+"/v1" {
		return 0, false
	}
	return v1.CallbackKeeper(name)
}

func (w *ownWalk) literalEscapes(n syntax.Node) bool {
	var path []syntax.Node
	for p := w.lending.parent[n]; p != nil; p = w.lending.parent[p] {
		path = append(path, p)
	}
	slices.Reverse(path)
	e := &escWalk{c: w.c, info: w.info, stack: path}
	_, escapes := e.frame(n)
	return escapes
}

func (w *ownWalk) keepCallbackLoans(key syntax.Node) {
	for p := w.lending.parent[key]; p != nil; p = w.lending.parent[p] {
		call, ok := p.(*syntax.CallExpr)
		if !ok {
			continue
		}
		k, ok := callbackKeeper(w.info.Calls[call])
		if !ok {
			return
		}
		if k >= 0 && w.st.reached {
			if _, i, ok := w.placeRoot(w.argument(call, k)); ok {
				w.st.slots[i].loans = mergeLoans(w.st.slots[i].loans, w.lending.loans[key])
			}
		}
		return
	}
}

func (w *ownWalk) drainsLends(c *Call) bool {
	if c == nil || c.Op == nil || c.Recv == nil {
		return false
	}
	n, ok := w.info.Types[c.Recv].(*Named)
	return ok && n.origin().Obj != nil && n.origin().Obj.Pkg.Std() && v1.DrainsLends(n.origin().Obj.Name+"."+c.Op.Name)
}

func (w *ownWalk) addLoans(n syntax.Node, loans []resourceLoan) {
	w.lending.loans[n] = mergeLoans(w.lending.loans[n], loans)
}

func mergeLoans(a, b []resourceLoan) []resourceLoan {
	out := slices.Clone(a)
	for _, x := range b {
		found := false
		for _, y := range out {
			if x.root == y.root && x.internal == y.internal && slices.Equal(x.end.path, y.end.path) && slices.Equal(x.keeper, y.keeper) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, x)
		}
	}
	return out
}

func (w *ownWalk) valueLoans(n syntax.Node) []resourceLoan {
	if n == nil {
		return nil
	}
	if id, ok := n.(*syntax.Ident); ok {
		if v, _ := w.info.Uses[id].(*Var); v != nil {
			if i, ok := w.slot[v]; ok && w.st.reached {
				return w.st.slots[i].loans
			}
		}
	}
	out := w.lending.loans[n]
	switch x := n.(type) {
	case *syntax.ParenExpr:
		return w.valueLoans(x.X)
	case *syntax.TryExpr:
		return w.valueLoans(x.X)
	case *syntax.SelectorExpr:
		return w.selectedLoans(x.X, w.fieldIndex(x))
	case *syntax.IndexExpr:
		return w.selectedLoans(x.X, -1)
	case *syntax.CompositeLit:
		for _, e := range x.Elts {
			if kv, ok := e.(*syntax.KeyValueExpr); ok {
				e = kv.Value
			}
			out = mergeLoans(out, w.valueLoans(e))
		}
	case *syntax.TupleExpr:
		for _, e := range x.Elts {
			out = mergeLoans(out, w.valueLoans(e))
		}
	case *syntax.CallExpr:
		for _, a := range x.Args {
			if k, ok := callbackKeeper(w.info.Calls[x]); ok && k < 0 {
				out = mergeLoans(out, w.valueLoans(a))
			}
		}
		if x.Trailing != nil {
			out = mergeLoans(out, w.lending.loans[x.Trailing])
		}
	}
	return out
}

// selectedLoans keeps function-valued fields and elements from erasing
// the lifetime of their captures. An internal field lend becomes a borrow
// of the aggregate when a callable is read out of it.
func (w *ownWalk) selectedLoans(base syntax.Expr, field int) []resourceLoan {
	v, i, ok := w.placeRoot(base)
	if !ok || !w.st.reached {
		return nil
	}
	var out []resourceLoan
	for _, loan := range w.st.slots[i].loans {
		if field >= 0 && len(loan.keeper) > 0 && loan.keeper[0] != field {
			continue
		}
		if loan.internal {
			loan.internal, loan.root, loan.end = false, v, w.point(v, i)
		}
		loan.keeper = nil
		out = mergeLoans(out, []resourceLoan{loan})
	}
	return out
}

func (w *ownWalk) checkLoanValue(x syntax.Expr) {
	if w.tracked(w.info.Types[x]) {
		return
	} // resource hand-offs use handLoans
	dest := w.destination(x)
	for _, loan := range w.valueLoans(x) {
		if !loan.internal && !w.releasesBefore(dest, loan.end) {
			w.refuseLoan(loan, x, "a callable outlives its captured root", "this function value holds a lend of %s and this use may outlive it (resources R31, R33); call it here or keep it inside the root's lifetime", loan.root.Name)
		}
	}
}

// keepArgumentLoans handles named callback values, whose literal may have
// been checked in an earlier statement, and both dot and ordinary calls.
func (w *ownWalk) keepArgumentLoans(x *syntax.CallExpr) {
	c := w.info.Calls[x]
	p, _, moving := movingBody(c)
	k, known := callbackKeeper(c)
	if !moving || !known || k < 0 || !w.st.reached {
		return
	}
	a := w.argument(x, p)
	if a == nil {
		return
	} // trailing literals already attach at taskFrame
	if _, i, ok := w.placeRoot(w.argument(x, k)); ok {
		w.st.slots[i].loans = mergeLoans(w.st.slots[i].loans, w.valueLoans(a))
	}
}

// loanTake guards an early root release, including assignment and partial
// field release. A keeper drained on every incoming path no longer lends.
func (w *ownWalk) loanTake(i int, at syntax.Node, field int) bool {
	if !w.st.reached || w.lending == nil {
		return false
	}
	v := w.vars[i].v
	for k, s := range w.st.slots {
		if !s.live() {
			continue
		}
		for _, loan := range s.loans {
			blocks := !loan.internal && loan.root == v
			if loan.internal && k == i && field >= 0 && len(loan.end.path) > 0 && loan.end.path[0] == field {
				blocks = len(loan.keeper) == 0 || s.fields&(1<<loan.keeper[0]) == 0
			}
			if blocks && field < 0 && w.together(at, w.vars[k].v, v) {
				blocks = false
			}
			if blocks {
				w.refuse(i, at, "a resource is taken while lent", "%s is lent to a task or closure that may still run (resources R28, R32); stop or wait for its keeper before taking or replacing it", v.Name)
				return true
			}
		}
	}
	return false
}

func (w *ownWalk) handLoans(i int, x *syntax.Ident, h hand) {
	if !w.st.reached || w.lending == nil || h.endLoans {
		return
	}
	d := w.destination(x)
	updated := slices.Clone(w.st.slots[i].loans)
	for k, loan := range updated {
		if loan.internal {
			continue
		} // a whole aggregate keeps its internal order
		if end, placed := w.literalRoot(x, loan.root, false); placed && w.together(x, w.vars[i].v, loan.root) {
			loan.end, loan.internal, loan.keeper = end, true, slices.Clone(d.path)
			updated[k] = loan
		}
		if !w.releasesBefore(d, loan.end) {
			w.refuseLoan(loan, x, "a keeper outlives its borrow", "%s holds a structured lend of %s; this hand-off would outlive that borrow (resources R27, R31); drain it here instead", x.Name, loan.root.Name)
		}
	}
	w.st.slots[i].loans = updated
}

// together checks the whole-literal move before either source is handed
// off. Both values must be evaluated into that one literal, in a safe
// declaration order. A hypothetical later move in another statement does
// not qualify.
func (w *ownWalk) together(at syntax.Node, keeper, root *Var) bool {
	if keeper == nil || root == nil {
		return false
	}
	for n := at; n != nil; n = w.lending.parent[n] {
		lit, ok := n.(*syntax.CompositeLit)
		if !ok {
			continue
		}
		var kp, rp lendPoint
		for _, e := range lit.Elts {
			x := e
			if kv, ok := x.(*syntax.KeyValueExpr); ok {
				x = kv.Value
			}
			if id, ok := unparen(x).(*syntax.Ident); ok {
				if w.info.Uses[id] == keeper {
					kp = w.destination(id)
				}
				if w.info.Uses[id] == root {
					rp = w.destination(id)
				}
			}
		}
		if kp.scope != nil && rp.scope != nil {
			return w.releasesBefore(kp, rp)
		}
	}
	return false
}

// literalCaptures shares the capture inventory between a moving task and
// an ordinary escaping closure. A non-escaping frame needs no stored loan.
func (w *ownWalk) literalCaptures(key syntax.Node, body *syntax.BlockStmt, capture *TaskCapture, moving bool) {
	delete(w.lending.loans, key)
	delete(w.info.TaskCaptures, key)
	dest := w.destination(key)
	seen := map[*Var]bool{}
	syntax.Inspect(body, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Ident)
		if !ok {
			return true
		}
		v, _ := w.info.Uses[id].(*Var)
		i, ok := w.slot[v]
		if !ok || seen[v] {
			return true
		}
		seen[v] = true
		if w.vars[i].loanOnly {
			if !w.st.reached || len(w.st.slots[i].loans) == 0 {
				return true
			}
			for _, loan := range w.st.slots[i].loans {
				if !w.releasesBefore(dest, loan.end) {
					w.refuseLoan(loan, id, "a captured function outlives its loans", "%s holds a structured lend which this capture would outlive (resources R31, R33); keep the callback inside the borrowed root's lifetime", v.Name)
				}
			}
			capture.Borrowed = append(capture.Borrowed, v)
			w.addLoans(key, w.st.slots[i].loans)
			return true
		}
		root, ri, found := w.placeRoot(id)
		if !found {
			return true
		}
		end := w.point(root, ri)
		placed, inLiteral := w.placedRoot(key, root)
		if inLiteral {
			end = placed
		}
		if w.releasesBefore(dest, end) {
			if !inLiteral {
				w.use(i, id)
			}
			capture.Borrowed = append(capture.Borrowed, v)
			loan := resourceLoan{root: root, end: end, keeper: slices.Clone(dest.path)}
			if inLiteral && end.scope == dest.scope && end.order == dest.order {
				loan.internal = true
			}
			w.addLoans(key, []resourceLoan{loan})
		} else if moving && !w.borrowedPlaceUse(id) {
			w.move(i, id, hand{at: key, how: "it is captured by a moving task (resources R12)"})
			delete(w.info.Handoffs, id)
			capture.Vars = append(capture.Vars, v)
		} else {
			w.refuse(i, id, "an escaping closure outlives a resource", "%s is captured by an escaping closure with no keeper released before its borrow ends (resources R31, R33); keep it in a later local owner or pass a non-escaping callback", v.Name)
		}
		return true
	})
	if len(capture.Vars)+len(capture.Borrowed) > 0 {
		w.info.TaskCaptures[key] = capture
	}
}

func (w *ownWalk) refuseLoan(loan resourceLoan, at syntax.Node, what, format string, args ...any) {
	if w.genericOnly(loan.root.Type) {
		w.copySite(loan.root.Type, at, what, false)
		return
	}
	w.report(at, format, args...)
}

// A resource reached through an index or field is borrowed from its root.
// A task whose keeper does not qualify cannot turn that element borrow
// into an implicit move of the entire container or aggregate (R27/R35).
func (w *ownWalk) borrowedPlaceUse(id *syntax.Ident) bool {
	var n syntax.Node = id
	for {
		p := w.lending.parent[n]
		switch x := p.(type) {
		case *syntax.ParenExpr:
		case *syntax.SelectorExpr:
			if x.X != n {
				return false
			}
			if w.fieldIndex(x) >= 0 && w.tracked(w.info.Types[x]) {
				return true
			}
		case *syntax.IndexExpr:
			if x.X != n {
				return false
			}
			if w.tracked(w.info.Types[x]) {
				return true
			}
		default:
			return false
		}
		n = p
	}
}

// aggregateLoans is the structural lifetime contract at a value boundary.
// A task field can borrow earlier resource fields, so a caller must keep
// them alive until that field drains. This remains true when a constructor
// hides the task's body or comes from another package. No body is inspected
// across the boundary, and moving the whole value preserves the contract.
func (w *ownWalk) aggregateLoans(v *Var) []resourceLoan {
	if v == nil {
		return nil
	}
	s, ok := under(v.Type).(*Struct)
	if !ok {
		return nil
	}
	var loans []resourceLoan
	for i, keeper := range s.Fields {
		_, callable := under(keeper.Type).(*Signature)
		if w.c.running(keeper.Type) == nil && !callable {
			continue
		}
		for j, root := range s.Fields[:i] {
			if w.tracked(root.Type) {
				loans = append(loans, resourceLoan{root: v, end: lendPoint{path: []int{j}}, internal: true, keeper: []int{i}})
			}
		}
	}
	return loans
}

func (w *ownWalk) drainLoans(x syntax.Expr) {
	_, i, ok := w.placeRoot(x)
	if !ok || !w.st.reached {
		return
	}
	field := -1
	if sel, ok := unparen(x).(*syntax.SelectorExpr); ok {
		field = w.fieldIndex(sel)
	}
	var kept []resourceLoan
	for _, loan := range w.st.slots[i].loans {
		if field >= 0 && (len(loan.keeper) == 0 || loan.keeper[0] != field) {
			kept = append(kept, loan)
		}
	}
	w.st.slots[i].loans = kept
}
