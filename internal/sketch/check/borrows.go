package check

import (
	"fmt"
	"go/constant"
	"go/token"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Overlapping borrows (C2; types.md, Borrows 2): the borrows one call
// takes (each `&x`, the implicit borrow of a dot call whose function takes
// *T, and a borrow passed on) name places that do not overlap. A place is a
// root and a path of fields and indexes; two places overlap when they have
// one root and one path is a prefix of the other, step by step: two fields
// are one step when they have one name, two indexes when they are one
// constant or one binding. Two different constants, or two different
// fields, keep the places apart. Two indexes that are neither need a
// dominating guard (guarded): the call is in the body of an `if a != b`,
// in the else of an `if a == b`, or after an `if a == b { … return }`
// (or break or continue), where a and b are the two indexes, each a
// constant or a field path of an immutable binding read through no borrow
// (a let mut or a borrow could be written between the guard and the
// call). Anything else is S108 at the second borrow.
//
// Lender exclusivity across callbacks (NOU-LIVE-LENDER-EXCLUSIVITY; types.md,
// Borrows 2): while a call holds a mutable borrow of a place P and passes a
// borrow (a parameter whose type holds a borrow, or a capability the callee
// provides to the callback) into a callback F — or calls F directly with a
// borrow — F (its body, nested closures, and any local frame it references or
// calls) cannot also access any place overlapping P (including through local
// borrow aliases or borrow-converted operation types), nor use a non-borrowing,
// non-escaping closure parameter that could have captured P's root from outside.
// Re-entrant callbacks that receive no borrow and no callee-provided capability
// (e.g. UI frame / printer block builders) stay admitted.
//
// Prior art: Swift's law of exclusivity refuses overlapping accesses statically
// where it can and checks the rest at run time; Rust's borrow checker refuses
// capturing the lender while an `&mut` lent from it is live, and refuses
// `f(&mut xs[0], &mut xs[1])` outright (offering `split_at_mut` and
// `get_many_mut`, which check indexes at run time); Ergo decides constants and
// disjoint fields statically and asks for a dominating guard otherwise, so no
// run-time check is emitted. Gleam, Elixir and ReScript have no mutable borrows.

const (
	borrowOverlapText         = "%s and %s are borrows of one place; a call takes no two overlapping borrows: pass one, or two places apart (types.md, Borrows 2)"
	borrowMaybeText           = "%s and %s may be borrows of one place, since %s may equal %s; guard the call with if %s != %s, with each index a constant or a let (types.md, Borrows 2)"
	borrowCallbackOverlapText = "%s is borrowed by this call, so the callback cannot also access %s: two overlapping paths to one place break borrow exclusivity (types.md, Borrows 2)"
	borrowCallbackMaybeText   = "%s is borrowed by this call and the callback may access the same place through %s, since %s may equal %s; guard the call with if %s != %s, with each index a constant or a let (types.md, Borrows 2)"
	borrowCallbackClosureText = "%s is borrowed by this call, and %s is a non-escaping closure that may also capture it; pass only a closure that cannot capture %s (types.md, Borrows 2)"
)

// placeStep is one field or index of a place.
type placeStep struct {
	field string
	index syntax.Expr // nil for a field
}

// place is the root and path a borrow names.
type place struct {
	expr  syntax.Expr
	root  Object
	steps []placeStep
}

// checkBorrows applies the overlap and lender-exclusivity rules to the file f.
func (c *checker) checkBorrows(f *fileInfo) {
	w := &borrowWalk{c: c, info: c.pkg.Info}
	for _, d := range f.syntax.Decls {
		w.collectDecl(d)
		w.visit(d)
	}
}

type borrowWalk struct {
	c             *checker
	info          *Info
	stack         []syntax.Node
	varFrame      map[*Var]int
	paramSig      map[*Var]*Signature
	aliasPlaces   map[*Var][]place
	localLits     map[*Var][]*syntax.FuncLit
	nonEscOrigins map[*Var][]*Var
}

// collectDecl gathers per-declaration frame depths, enclosing signatures for
// parameters, local borrow/interface aliases, local function literals, and
// non-escaping function parameter origins before visit checks each call.
func (w *borrowWalk) collectDecl(d syntax.Node) {
	w.varFrame = map[*Var]int{}
	w.paramSig = map[*Var]*Signature{}
	w.aliasPlaces = map[*Var][]place{}
	w.localLits = map[*Var][]*syntax.FuncLit{}
	w.nonEscOrigins = map[*Var][]*Var{}

	type litEdge struct {
		to   *Var
		lits []*syntax.FuncLit
		via  []*Var
	}
	type nonEscEdge struct {
		to  *Var
		via []*Var
	}
	var litEdges []litEdge
	var nonEscEdges []nonEscEdge

	recordAlias := func(v *Var, rhs syntax.Expr) {
		if v == nil || v.pkg || rhs == nil {
			return
		}
		_, isIface := under(v.Type).(*Interface)
		if isPointer(v.Type) || holdsBorrow(v.Type) || isIface {
			for _, p := range w.borrowPlacesIn(rhs) {
				w.aliasPlaces[v] = append(w.aliasPlaces[v], p)
			}
		}
		var lits []*syntax.FuncLit
		var via []*Var
		var neVia []*Var
		w.collectFuncValues(rhs, &lits, &via, &neVia)
		if len(lits) > 0 || len(via) > 0 {
			litEdges = append(litEdges, litEdge{to: v, lits: lits, via: via})
		}
		if len(neVia) > 0 {
			nonEscEdges = append(nonEscEdges, nonEscEdge{to: v, via: neVia})
		}
	}

	frameDepth := -1
	var frameSigs []*Signature
	var stack []syntax.Node
	var walk func(n syntax.Node)
	walk = func(n syntax.Node) {
		if n == nil {
			return
		}
		if x, ok := n.(syntax.Expr); ok && w.c.typeNodes[x] {
			return
		}
		opened, sig := w.declFrame(n, stack)
		if opened {
			frameDepth++
			frameSigs = append(frameSigs, sig)
		}
		switch s := n.(type) {
		case *syntax.Ident:
			if v, ok := w.info.Defs[s].(*Var); ok && !v.pkg {
				w.varFrame[v] = frameDepth
				if v.param {
					if len(frameSigs) > 0 {
						w.paramSig[v] = frameSigs[len(frameSigs)-1]
					}
					if nonEscaping(v.Type) {
						w.nonEscOrigins[v] = []*Var{v}
					}
				}
			}
		case *syntax.VarSpec:
			if s.Pattern == nil {
				for i, id := range s.Names {
					v, ok := w.info.Defs[id].(*Var)
					if !ok || v.pkg {
						continue
					}
					if val := specValue(s, i); val != nil {
						recordAlias(v, val)
					}
				}
			} else {
				syntax.Inspect(s.Pattern, func(m syntax.Node) bool {
					if id, ok := m.(*syntax.Ident); ok {
						if v, ok := w.info.Defs[id].(*Var); ok && !v.pkg {
							for _, val := range s.Values {
								recordAlias(v, val)
							}
						}
					}
					return true
				})
			}
		case *syntax.AssignStmt:
			if s.Tok == syntax.ASSIGN {
				for i, l := range s.Lhs {
					vals := s.Rhs
					if len(s.Lhs) == len(s.Rhs) {
						vals = s.Rhs[i : i+1]
					}
					for _, id := range assignedNames(l) {
						v, ok := w.info.Uses[id].(*Var)
						if !ok || v.pkg {
							continue
						}
						for _, val := range vals {
							recordAlias(v, val)
						}
					}
				}
			}
		}
		stack = append(stack, n)
		syntax.Inspect(n, func(m syntax.Node) bool {
			if m == n {
				return true
			}
			if m != nil {
				walk(m)
			}
			return false
		})
		stack = stack[:len(stack)-1]
		if opened {
			frameDepth--
			frameSigs = frameSigs[:len(frameSigs)-1]
		}
	}
	walk(d)

	for changed := true; changed; {
		changed = false
		for _, e := range litEdges {
			for _, lit := range e.lits {
				if !slices.Contains(w.localLits[e.to], lit) {
					w.localLits[e.to] = append(w.localLits[e.to], lit)
					changed = true
				}
			}
			for _, src := range e.via {
				for _, lit := range w.localLits[src] {
					if !slices.Contains(w.localLits[e.to], lit) {
						w.localLits[e.to] = append(w.localLits[e.to], lit)
						changed = true
					}
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, e := range nonEscEdges {
			for _, src := range e.via {
				for _, orig := range w.nonEscOrigins[src] {
					if !slices.Contains(w.nonEscOrigins[e.to], orig) {
						w.nonEscOrigins[e.to] = append(w.nonEscOrigins[e.to], orig)
						changed = true
					}
				}
			}
		}
	}
}

// declFrame reports whether n opens a lexical frame and its signature (if any).
func (w *borrowWalk) declFrame(n syntax.Node, stack []syntax.Node) (bool, *Signature) {
	switch f := n.(type) {
	case *syntax.FuncDecl:
		if fn, ok := w.info.Defs[f.Name].(*Func); ok {
			return true, fn.Sig
		}
		return true, nil
	case *syntax.TestDecl, *syntax.LawDecl:
		return true, nil
	case *syntax.FuncLit:
		if s := w.info.Literals[f]; s != nil {
			return true, s
		}
		s, _ := under(w.info.Types[f]).(*Signature)
		return true, s
	case *syntax.BlockStmt:
		if len(stack) == 0 {
			return false, nil
		}
		call, ok := stack[len(stack)-1].(*syntax.CallExpr)
		if !ok || call.Trailing != f {
			return false, nil
		}
		if k := w.info.Calls[call]; k != nil && k.Kind == CallWith {
			return false, nil
		}
		return true, w.info.Literals[f]
	}
	return false, nil
}

// borrowPlacesIn returns the places referenced as borrows inside rhs (for
// tracking `let p = &x`, `let q = p`, `let c Op = &x`, etc.).
func (w *borrowWalk) borrowPlacesIn(rhs syntax.Expr) []place {
	var out []place
	var walk func(syntax.Expr)
	walk = func(x syntax.Expr) {
		if x == nil {
			return
		}
		switch e := unparen(x).(type) {
		case *syntax.UnaryExpr:
			if e.Op == syntax.AND {
				if p, ok := w.placeOf(e.X); ok {
					out = append(out, p)
				}
			}
		case *syntax.Ident:
			if v, ok := w.info.Uses[e].(*Var); ok && !v.pkg {
				_, isIface := under(v.Type).(*Interface)
				if isPointer(v.Type) || holdsBorrow(v.Type) || isIface {
					out = append(out, place{expr: e, root: v})
				}
			}
		case *syntax.TupleExpr:
			for _, el := range e.Elts {
				walk(el)
			}
		case *syntax.IfStmt:
			if t := blockTail(e.Body); t != nil {
				walk(t)
			}
			switch el := e.Else.(type) {
			case *syntax.BlockStmt:
				if t := blockTail(el); t != nil {
					walk(t)
				}
			case *syntax.IfStmt:
				walk(el)
			}
		case *syntax.SwitchStmt:
			for _, cl := range e.Clauses {
				if len(cl.Body) > 0 {
					if t := stmtValue(cl.Body[len(cl.Body)-1]); t != nil {
						walk(t)
					}
				}
			}
		case *syntax.TypeSwitchStmt:
			for _, cl := range e.Clauses {
				if len(cl.Body) > 0 {
					if t := stmtValue(cl.Body[len(cl.Body)-1]); t != nil {
						walk(t)
					}
				}
			}
		case *syntax.CatchExpr:
			walk(e.X)
			for _, a := range e.Arms {
				if len(a.Body) > 0 {
					if t := stmtValue(a.Body[len(a.Body)-1]); t != nil {
						walk(t)
					}
				}
			}
		case *syntax.TryExpr:
			walk(e.X)
		}
	}
	walk(rhs)
	return out
}

// collectFuncValues records function literals and local function variables
// flowing out of rhs.
func (w *borrowWalk) collectFuncValues(rhs syntax.Expr, lits *[]*syntax.FuncLit, via, neVia *[]*Var) {
	var walk func(syntax.Expr)
	walk = func(x syntax.Expr) {
		if x == nil {
			return
		}
		switch e := unparen(x).(type) {
		case *syntax.FuncLit:
			*lits = append(*lits, e)
		case *syntax.Ident:
			if v, ok := w.info.Uses[e].(*Var); ok && !v.pkg {
				*via = append(*via, v)
				if nonEscaping(v.Type) {
					*neVia = append(*neVia, v)
				}
			}
		case *syntax.IfStmt:
			if t := blockTail(e.Body); t != nil {
				walk(t)
			}
			switch el := e.Else.(type) {
			case *syntax.BlockStmt:
				if t := blockTail(el); t != nil {
					walk(t)
				}
			case *syntax.IfStmt:
				walk(el)
			}
		case *syntax.SwitchStmt:
			for _, cl := range e.Clauses {
				if len(cl.Body) > 0 {
					if t := stmtValue(cl.Body[len(cl.Body)-1]); t != nil {
						walk(t)
					}
				}
			}
		case *syntax.TypeSwitchStmt:
			for _, cl := range e.Clauses {
				if len(cl.Body) > 0 {
					if t := stmtValue(cl.Body[len(cl.Body)-1]); t != nil {
						walk(t)
					}
				}
			}
		case *syntax.CatchExpr:
			walk(e.X)
			for _, a := range e.Arms {
				if len(a.Body) > 0 {
					if t := stmtValue(a.Body[len(a.Body)-1]); t != nil {
						walk(t)
					}
				}
			}
		case *syntax.TryExpr:
			walk(e.X)
		}
	}
	walk(rhs)
}

// expandPlace resolves local borrow aliases (`let r = &a`, `let c Op = &a`) to
// their underlying place(s).
func (w *borrowWalk) expandPlace(p place) []place {
	v, ok := p.root.(*Var)
	if !ok || len(w.aliasPlaces[v]) == 0 {
		return []place{p}
	}
	var out []place
	seen := map[*Var]bool{v: true}
	var expand func(cur place)
	expand = func(cur place) {
		rv, ok := cur.root.(*Var)
		if !ok || len(w.aliasPlaces[rv]) == 0 {
			out = append(out, cur)
			return
		}
		for _, base := range w.aliasPlaces[rv] {
			bv, isVar := base.root.(*Var)
			if isVar && seen[bv] {
				continue
			}
			steps := make([]placeStep, 0, len(base.steps)+len(cur.steps))
			steps = append(steps, base.steps...)
			steps = append(steps, cur.steps...)
			next := place{expr: cur.expr, root: base.root, steps: steps}
			if isVar && len(w.aliasPlaces[bv]) > 0 {
				seen[bv] = true
				expand(next)
				delete(seen, bv)
			} else {
				out = append(out, next)
			}
		}
	}
	expand(p)
	if len(out) == 0 {
		return []place{p}
	}
	return out
}

func (w *borrowWalk) visit(n syntax.Node) {
	if x, ok := n.(syntax.Expr); ok && w.c.typeNodes[x] {
		return
	}
	if call, ok := n.(*syntax.CallExpr); ok {
		w.call(call)
	}
	w.stack = append(w.stack, n)
	syntax.Inspect(n, func(m syntax.Node) bool {
		if m == n {
			return true
		}
		if m != nil {
			w.visit(m)
		}
		return false
	})
	w.stack = w.stack[:len(w.stack)-1]
}

// call checks the borrows and lender exclusivity of one call.
func (w *borrowWalk) call(call *syntax.CallExpr) {
	type taggedPlace struct {
		arg int
		p   place
	}
	var places []taggedPlace
	add := func(arg int, x syntax.Expr) {
		if p, ok := w.placeOf(x); ok {
			for _, ep := range w.expandPlace(p) {
				places = append(places, taggedPlace{arg: arg, p: ep})
			}
		}
	}
	var addArg func(arg int, a syntax.Expr)
	addArg = func(arg int, a syntax.Expr) {
		switch u := unparen(a).(type) {
		case *syntax.UnaryExpr:
			if u.Op == syntax.AND {
				if p, ok := w.placeOf(u.X); ok {
					// Taking &q of a local pointer variable q (when not relending
					// a mut parameter) borrows the slot q itself, not its target.
					if len(p.steps) == 0 && isPointer(w.info.Types[u.X]) && !w.info.Relends[u] {
						places = append(places, taggedPlace{arg: arg, p: p})
					} else {
						for _, ep := range w.expandPlace(p) {
							places = append(places, taggedPlace{arg: arg, p: ep})
						}
					}
				}
			}
		case *syntax.Ident:
			if isPointer(w.info.Types[a]) {
				add(arg, u)
			}
		case *syntax.TupleExpr:
			for _, el := range u.Elts {
				addArg(arg, el)
			}
		}
	}
	if k := w.info.Calls[call]; recvBorrow(w.info, k) {
		add(-1, k.Recv) // the implicit borrow, or a borrow passed on
	} else if k != nil && k.Kind == CallOp && k.Recv != nil {
		if p, ok := w.placeOf(k.Recv); ok {
			if v, ok := p.root.(*Var); ok && len(w.aliasPlaces[v]) > 0 {
				add(-1, k.Recv) // borrow-converted operation type receiver
			}
		}
	}
	k := w.info.Calls[call]
	for i, a := range call.Args {
		addArg(i, a)
	}
	for j := 1; j < len(places); j++ {
		for i := 0; i < j; i++ {
			if places[i].arg == places[j].arg {
				continue
			}
			if w.report(places[i].p, places[j].p) {
				break
			}
		}
	}
	if len(places) == 0 {
		return
	}
	borrows := make([]place, len(places))
	for i, tp := range places {
		borrows[i] = tp.p
	}
	w.checkCallCallbacks(call, k, borrows)
}

// checkCallCallbacks enforces lender exclusivity against every callback of
// call that may receive a borrow (or callee-provided capability), plus call.Fun
// itself when calling a function value with a borrow argument.
func (w *borrowWalk) checkCallCallbacks(call *syntax.CallExpr, k *Call, borrows []place) {
	w.stack = append(w.stack, call)
	defer func() { w.stack = w.stack[:len(w.stack)-1] }()

	if k == nil || k.Kind == CallFuncValue {
		w.checkCallbackExpr(call.Fun, borrows, call.Fun, map[*syntax.FuncLit]bool{}, map[callbackDiagKey]bool{})
	}

	calleeSig := w.calleeSignature(call, k)
	checkArg := func(i int, arg syntax.Expr) {
		cbSig := w.paramSignature(k, calleeSig, i)
		receives := false
		if cbSig != nil {
			receives = w.sigReceivesBorrow(cbSig, calleeSig)
		} else if arg != nil {
			if s, ok := under(w.info.Types[arg]).(*Signature); ok {
				receives = w.sigReceivesBorrow(s, calleeSig)
			}
		}
		if receives {
			w.checkCallbackExpr(arg, borrows, arg, map[*syntax.FuncLit]bool{}, map[callbackDiagKey]bool{})
		} else if arg != nil {
			switch unparen(arg).(type) {
			case *syntax.CompositeLit, *syntax.TupleExpr:
				w.checkNestedBorrowCallbacks(arg, calleeSig, borrows)
			}
		}
	}
	for i, a := range call.Args {
		checkArg(i, a)
	}
	if call.Trailing != nil {
		if k == nil || k.Kind != CallWith {
			cbSig := w.paramSignature(k, calleeSig, len(call.Args))
			if cbSig == nil {
				cbSig = w.info.Literals[call.Trailing]
			}
			if w.sigReceivesBorrow(cbSig, calleeSig) {
				w.checkCallbackBody(call.Trailing, borrows, nil, map[*syntax.FuncLit]bool{}, map[callbackDiagKey]bool{})
			}
		}
	}
}

// checkNestedBorrowCallbacks inspects composite/tuple arguments (e.g.
// Hooks{OnMut: func(v mut int) { ... }}) for embedded function literals that
// receive a borrow.
func (w *borrowWalk) checkNestedBorrowCallbacks(arg syntax.Expr, calleeSig *Signature, borrows []place) {
	switch e := unparen(arg).(type) {
	case *syntax.CompositeLit:
		for _, el := range e.Elts {
			if kv, ok := el.(*syntax.KeyValueExpr); ok {
				el = kv.Value
			}
			w.checkNestedBorrowCallbacks(el, calleeSig, borrows)
		}
	case *syntax.TupleExpr:
		for _, el := range e.Elts {
			w.checkNestedBorrowCallbacks(el, calleeSig, borrows)
		}
	case *syntax.FuncLit:
		litSig := w.info.Literals[e]
		if litSig == nil {
			litSig, _ = under(w.info.Types[e]).(*Signature)
		}
		if w.sigReceivesBorrow(litSig, calleeSig) {
			w.checkCallbackExpr(e, borrows, e, map[*syntax.FuncLit]bool{}, map[callbackDiagKey]bool{})
		}
	}
}

type callbackDiagKey struct {
	line int
	root Object
}

func (w *borrowWalk) calleeSignature(call *syntax.CallExpr, k *Call) *Signature {
	if k == nil {
		return nil
	}
	switch k.Kind {
	case CallFunc:
		if k.Func != nil {
			return k.Func.Sig
		}
	case CallOp, CallProviderOp, CallLawOp:
		if k.Op != nil {
			return k.Op.Sig
		}
	case CallFuncValue:
		s, _ := under(w.info.Types[call.Fun]).(*Signature)
		return s
	}
	return nil
}

func (w *borrowWalk) paramSignature(k *Call, calleeSig *Signature, i int) *Signature {
	if calleeSig == nil {
		return nil
	}
	pi := i
	if k != nil && k.Kind == CallFunc {
		pi = argParam(k, i)
	}
	if pi < 0 || pi >= len(calleeSig.Params) {
		return nil
	}
	s, _ := under(calleeSig.Params[pi].Type).(*Signature)
	return s
}

// sigReceivesBorrow reports whether a callback of signature cbSig may receive a
// borrow from a call of calleeSig: through a borrow-holding parameter (or a
// nested callback parameter that does), or through a capability in cbSig's
// needs row that calleeSig itself does not require (so calleeSig provides it).
func (w *borrowWalk) sigReceivesBorrow(cbSig, calleeSig *Signature) bool {
	if cbSig == nil {
		return false
	}
	for _, p := range cbSig.Params {
		if paramMayReceiveBorrow(p.Type) {
			return true
		}
	}
	return w.providesNewCap(cbSig, calleeSig)
}

func paramMayReceiveBorrow(t Type) bool {
	if t == nil {
		return false
	}
	if holdsBorrow(t) {
		return true
	}
	if s, ok := under(t).(*Signature); ok {
		for _, p := range s.Params {
			if paramMayReceiveBorrow(p.Type) {
				return true
			}
		}
	}
	return false
}

func (w *borrowWalk) providesNewCap(cbSig, calleeSig *Signature) bool {
	if cbSig == nil || cbSig.Uses.Empty() {
		return false
	}
	if calleeSig == nil || calleeSig.Uses.Empty() {
		return true
	}
	calleeElems := calleeSig.Uses.Elems()
	for _, elem := range cbSig.Uses.Elems() {
		if !slices.ContainsFunc(calleeElems, func(t Type) bool { return w.c.identical(t, elem) }) {
			return true
		}
	}
	return false
}

// checkCallbackExpr inspects a callback expression x passed to (or called by)
// a call holding borrows. Inline function literals report at the offending
// access inside their body; local-frame references report at reportAt.
func (w *borrowWalk) checkCallbackExpr(x syntax.Expr, borrows []place, reportAt syntax.Expr, visited map[*syntax.FuncLit]bool, reported map[callbackDiagKey]bool) {
	if x == nil {
		return
	}
	switch e := unparen(x).(type) {
	case *syntax.FuncLit:
		if e.Body != nil && !visited[e] {
			visited[e] = true
			w.checkCallbackBody(e.Body, borrows, nil, visited, reported)
		}
	case *syntax.Ident:
		v, ok := w.info.Uses[e].(*Var)
		if !ok || v.pkg {
			return
		}
		for _, lit := range w.localLits[v] {
			if lit.Body != nil && !visited[lit] {
				visited[lit] = true
				w.checkCallbackBody(lit.Body, borrows, reportAt, visited, reported)
			}
		}
		for _, gp := range w.nonEscOrigins[v] {
			w.checkClosureParam(gp, e, reportAt, borrows, reported)
		}
	case *syntax.IfStmt:
		if t := blockTail(e.Body); t != nil {
			w.checkCallbackExpr(t, borrows, reportAt, visited, reported)
		}
		switch el := e.Else.(type) {
		case *syntax.BlockStmt:
			if t := blockTail(el); t != nil {
				w.checkCallbackExpr(t, borrows, reportAt, visited, reported)
			}
		case *syntax.IfStmt:
			w.checkCallbackExpr(el, borrows, reportAt, visited, reported)
		}
	case *syntax.SwitchStmt:
		for _, cl := range e.Clauses {
			if len(cl.Body) > 0 {
				if t := stmtValue(cl.Body[len(cl.Body)-1]); t != nil {
					w.checkCallbackExpr(t, borrows, reportAt, visited, reported)
				}
			}
		}
	case *syntax.TypeSwitchStmt:
		for _, cl := range e.Clauses {
			if len(cl.Body) > 0 {
				if t := stmtValue(cl.Body[len(cl.Body)-1]); t != nil {
					w.checkCallbackExpr(t, borrows, reportAt, visited, reported)
				}
			}
		}
	case *syntax.CatchExpr:
		w.checkCallbackExpr(e.X, borrows, reportAt, visited, reported)
		for _, a := range e.Arms {
			if len(a.Body) > 0 {
				if t := stmtValue(a.Body[len(a.Body)-1]); t != nil {
					w.checkCallbackExpr(t, borrows, reportAt, visited, reported)
				}
			}
		}
	case *syntax.TryExpr:
		w.checkCallbackExpr(e.X, borrows, reportAt, visited, reported)
	}
}

// checkCallbackBody walks a callback body looking for any access overlapping
// borrows, any transitive local-frame invocation, or any opaque non-borrowing
// non-escaping closure parameter that could have captured a lender.
func (w *borrowWalk) checkCallbackBody(body syntax.Node, borrows []place, reportAt syntax.Expr, visited map[*syntax.FuncLit]bool, reported map[callbackDiagKey]bool) {
	var walk func(n syntax.Node)
	walk = func(n syntax.Node) {
		if n == nil {
			return
		}
		if x, ok := n.(syntax.Expr); ok && w.c.typeNodes[x] {
			return
		}
		if x, ok := n.(syntax.Expr); ok && w.isPlaceCandidate(x) {
			if p, ok := w.placeOf(x); ok {
				w.checkAccessPlace(p, borrows, reportAt, visited, reported)
				w.visitPlaceIndices(x, walk)
				return
			}
		}
		if call, ok := n.(*syntax.CallExpr); ok {
			w.checkProviderCapAccess(call, borrows, reportAt, reported)
		}
		w.stack = append(w.stack, n)
		syntax.Inspect(n, func(m syntax.Node) bool {
			if m == n {
				return true
			}
			if m != nil {
				walk(m)
			}
			return false
		})
		w.stack = w.stack[:len(w.stack)-1]
	}
	walk(body)
}

func (w *borrowWalk) isPlaceCandidate(x syntax.Expr) bool {
	switch e := x.(type) {
	case *syntax.Ident:
		_, ok := w.info.Uses[e].(*Var)
		return ok
	case *syntax.SelectorExpr:
		return isFieldSelector(w.info, e)
	case *syntax.IndexExpr:
		return len(e.Indices) == 1
	case *syntax.StarExpr:
		return true
	}
	return false
}

func isFieldSelector(info *Info, sel *syntax.SelectorExpr) bool {
	t := info.Types[sel.X]
	if p, ok := under(t).(*Pointer); ok {
		t = p.Elem
	}
	st, ok := under(t).(*Struct)
	if !ok {
		return false
	}
	for _, f := range st.Fields {
		if f.Name == sel.Sel.Name {
			return true
		}
	}
	return false
}

// visitPlaceIndices visits index sub-expressions inside a place expression so
// accesses like xs[a.idx] are checked against active borrows.
func (w *borrowWalk) visitPlaceIndices(x syntax.Expr, walk func(syntax.Node)) {
	for e := x; ; {
		switch t := e.(type) {
		case *syntax.ParenExpr:
			e = t.X
		case *syntax.StarExpr:
			e = t.X
		case *syntax.SelectorExpr:
			e = t.X
		case *syntax.IndexExpr:
			for _, idx := range t.Indices {
				walk(idx)
			}
			e = t.X
		default:
			return
		}
	}
}

// checkProviderCapAccess checks whether call invokes a capability operation
// whose provider in an enclosing `with` block was borrowed from a place
// overlapping borrows (e.g. `with context { Cell: &e } { e.Update(func(v mut int) { Cell.Set(2) }) }`).
func (w *borrowWalk) checkProviderCapAccess(call *syntax.CallExpr, borrows []place, reportAt syntax.Expr, reported map[callbackDiagKey]bool) {
	k := w.info.Calls[call]
	if k == nil || k.Kind != CallProviderOp {
		return
	}
	sel, ok := unparen(call.Fun).(*syntax.SelectorExpr)
	if !ok {
		return
	}
	capType := w.info.Types[sel.X]
	if capType == nil {
		return
	}
	for j := len(w.stack) - 1; j >= 0; j-- {
		wc, ok := w.stack[j].(*syntax.CallExpr)
		if !ok {
			continue
		}
		wk := w.info.Calls[wc]
		if wk == nil || wk.Kind != CallWith {
			continue
		}
		for f := w.info.Withs[wc]; f != nil; f = f.Next {
			for _, a := range f.Acquired {
				if a == nil || a.Entry == nil || a.Entry.Provider == nil {
					continue
				}
				if !slices.ContainsFunc(a.Entry.Caps, func(c Type) bool { return w.c.identical(c, capType) }) {
					continue
				}
				for _, p := range w.borrowPlacesIn(a.Entry.Provider) {
					for _, ep := range w.expandPlace(p) {
						ep.expr = call.Fun
						for _, bp := range borrows {
							if w.reportCallbackOverlap(bp, ep, reportAt, reported) {
								return
							}
						}
					}
				}
				return // innermost with binding capType shadows outer ones
			}
		}
	}
}

func (w *borrowWalk) checkAccessPlace(p place, borrows []place, reportAt syntax.Expr, visited map[*syntax.FuncLit]bool, reported map[callbackDiagKey]bool) {
	for _, ep := range w.expandPlace(p) {
		for _, bp := range borrows {
			if w.reportCallbackOverlap(bp, ep, reportAt, reported) {
				break
			}
		}
	}
	v, ok := p.root.(*Var)
	if !ok || v.pkg {
		return
	}
	if len(w.localLits[v]) > 0 {
		at := reportAt
		if at == nil {
			at = p.expr
		}
		for _, lit := range w.localLits[v] {
			if lit.Body != nil && !visited[lit] {
				visited[lit] = true
				w.checkCallbackBody(lit.Body, borrows, at, visited, reported)
			}
		}
	}
	for _, gp := range w.nonEscOrigins[v] {
		w.checkClosureParam(gp, p.expr, reportAt, borrows, reported)
	}
}

// checkClosureParam refuses using a non-borrowing, non-escaping function
// parameter gp inside a borrowing callback when gp could have captured a
// lender root from outside gp's frame.
func (w *borrowWalk) checkClosureParam(gp *Var, useExpr, reportAt syntax.Expr, borrows []place, reported map[callbackDiagKey]bool) {
	gpSig, _ := under(gp.Type).(*Signature)
	if w.sigReceivesBorrow(gpSig, w.paramSig[gp]) {
		return
	}
	gpFrame, ok := w.varFrame[gp]
	if !ok {
		return
	}
	at := reportAt
	if at == nil {
		at = useExpr
	}
	line, _ := w.c.cur.syntax.Position(at.Pos())
	for _, bp := range borrows {
		rv, ok := bp.root.(*Var)
		if !ok {
			continue
		}
		rFrame, ok := w.varFrame[rv]
		if !ok {
			continue
		}
		if rFrame < gpFrame || (rFrame == gpFrame && rv.param) {
			key := callbackDiagKey{line: line, root: bp.root}
			if reported[key] {
				continue
			}
			reported[key] = true
			lender := w.src(bp.expr)
			w.c.errorf(at, CodeType, borrowCallbackClosureText, lender, w.src(useExpr), lender)
			return
		}
	}
}

// overlapCheck decides whether places a and b overlap (maybe[0] == nil) or may
// overlap at dynamic indexes maybe[0], maybe[1].
func (w *borrowWalk) overlapCheck(a, b place) (bool, [2]syntax.Expr) {
	if a.root != b.root {
		return false, [2]syntax.Expr{}
	}
	var maybe [2]syntax.Expr
	for k := 0; k < len(a.steps) && k < len(b.steps); k++ {
		x, y := a.steps[k], b.steps[k]
		if x.index == nil {
			if x.field != y.field {
				return false, [2]syntax.Expr{}
			}
			continue
		}
		if y.index == nil {
			return false, [2]syntax.Expr{}
		}
		switch w.indexRel(x.index, y.index) {
		case relApart:
			return false, [2]syntax.Expr{}
		case relMaybe:
			if maybe[0] == nil {
				maybe = [2]syntax.Expr{x.index, y.index}
			}
		}
	}
	return true, maybe
}

// reportCallbackOverlap reports S108 when borrowed place a and callback access
// b may overlap.
func (w *borrowWalk) reportCallbackOverlap(a, b place, reportAt syntax.Expr, reported map[callbackDiagKey]bool) bool {
	ok, maybe := w.overlapCheck(a, b)
	if !ok {
		return false
	}
	at := reportAt
	if at == nil {
		at = b.expr
	}
	line, _ := w.c.cur.syntax.Position(at.Pos())
	key := callbackDiagKey{line: line, root: a.root}
	if reported[key] {
		return true
	}
	reported[key] = true
	lender, access := w.src(a.expr), w.src(b.expr)
	if maybe[0] != nil {
		i, j := w.src(maybe[0]), w.src(maybe[1])
		w.c.errorf(at, CodeType, borrowCallbackMaybeText, lender, access, i, j, i, j)
		return true
	}
	w.c.errorf(at, CodeType, borrowCallbackOverlapText, lender, access)
	return true
}

// report reports S108 when a and b may overlap, and says whether it did.
func (w *borrowWalk) report(a, b place) bool {
	ok, maybe := w.overlapCheck(a, b)
	if !ok {
		return false
	}
	at, other := w.src(b.expr), w.src(a.expr)
	if maybe[0] != nil {
		i, j := w.src(maybe[0]), w.src(maybe[1])
		w.c.errorf(b.expr, CodeType, borrowMaybeText, other, at, i, j, i, j)
		return true
	}
	w.c.errorf(b.expr, CodeType, borrowOverlapText, other, at)
	return true
}

// src is the source text of x.
func (w *borrowWalk) src(x syntax.Expr) string {
	return string(w.c.cur.syntax.Src[x.Pos():x.End()])
}

type indexRelation int

const (
	relSame indexRelation = iota
	relApart
	relMaybe
)

// indexRel relates two indexes at one step.
func (w *borrowWalk) indexRel(x, y syntax.Expr) indexRelation {
	cx, cy := w.info.Values[unparen(x)], w.info.Values[unparen(y)]
	if cx != nil && cy != nil {
		if constant.Compare(cx, token.EQL, cy) {
			return relSame
		}
		return relApart
	}
	if kx, ok := w.pathKey(x, true); ok {
		if ky, ok := w.pathKey(y, true); ok && kx == ky {
			return relSame
		}
	}
	if w.guarded(x, y) {
		return relApart
	}
	return relMaybe
}

// pathKey names the value x reads when x is a binding or a field path of
// one. A guard needs a value that no write changes between it and the
// call (stable): an immutable binding, read through no borrow.
func (w *borrowWalk) pathKey(x syntax.Expr, mutOK bool) (string, bool) {
	switch t := unparen(x).(type) {
	case *syntax.Ident:
		v, ok := w.info.Uses[t].(*Var)
		if !ok || (v.mut && !mutOK) {
			return "", false
		}
		return fmt.Sprintf("%p", v), true
	case *syntax.SelectorExpr:
		if !mutOK && isPointer(w.info.Types[t.X]) {
			return "", false
		}
		k, ok := w.pathKey(t.X, mutOK)
		return k + "." + t.Sel.Name, ok
	}
	return "", false
}

// stable reports whether the guard's operand g stands for the index x and
// keeps its value until the call: one constant, or one stable path.
func (w *borrowWalk) stable(g, x syntax.Expr) bool {
	cg, cx := w.info.Values[unparen(g)], w.info.Values[unparen(x)]
	if cg != nil || cx != nil {
		return cg != nil && cx != nil && constant.Compare(cg, token.EQL, cx)
	}
	kg, ok1 := w.pathKey(g, false)
	kx, ok2 := w.pathKey(x, false)
	return ok1 && ok2 && kg == kx
}

// guarded reports whether a dominating guard proves x != y where the call
// is: the call is in the body of an if whose condition has a conjunct
// a != b, in the else of an if whose condition has a disjunct a == b, or
// after an if, in its block or an enclosing one, whose condition has a
// disjunct a == b and whose body ends in return, break or continue.
func (w *borrowWalk) guarded(x, y syntax.Expr) bool {
	for j := len(w.stack) - 2; j >= 0; j-- {
		child := w.stack[j+1]
		switch s := w.stack[j].(type) {
		case *syntax.IfStmt:
			if child == syntax.Node(s.Body) && w.proves(parts(s.Cond, syntax.LAND), syntax.NEQ, x, y) {
				return true
			}
			if s.Else != nil && child == syntax.Node(s.Else) && w.proves(parts(s.Cond, syntax.LOR), syntax.EQL, x, y) {
				return true
			}
		case *syntax.BlockStmt:
			for _, st := range s.List {
				if syntax.Node(st) == child {
					break
				}
				if i, ok := st.(*syntax.IfStmt); ok && i.Else == nil && exits(i.Body) && w.proves(parts(i.Cond, syntax.LOR), syntax.EQL, x, y) {
					return true
				}
			}
		}
	}
	return false
}

// proves reports whether one of the terms is a op b with a and b the
// indexes x and y, in either order.
func (w *borrowWalk) proves(terms []syntax.Expr, op syntax.Token, x, y syntax.Expr) bool {
	for _, t := range terms {
		b, ok := unparen(t).(*syntax.BinaryExpr)
		if !ok || b.Op != op {
			continue
		}
		if (w.stable(b.X, x) && w.stable(b.Y, y)) || (w.stable(b.X, y) && w.stable(b.Y, x)) {
			return true
		}
	}
	return false
}

// parts are the operands of a chain of op (&& or ||) in cond.
func parts(cond syntax.Expr, op syntax.Token) []syntax.Expr {
	b, ok := unparen(cond).(*syntax.BinaryExpr)
	if !ok || b.Op != op {
		return []syntax.Expr{cond}
	}
	return append(parts(b.X, op), parts(b.Y, op)...)
}

// exits reports whether the block ends in return, break or continue.
func exits(b *syntax.BlockStmt) bool {
	if b == nil || len(b.List) == 0 {
		return false
	}
	switch b.List[len(b.List)-1].(type) {
	case *syntax.ReturnStmt, *syntax.DieStmt, *syntax.BranchStmt:
		return true
	}
	return false
}

// placeOf is the place x names: a root binding and its fields and indexes.
func (w *borrowWalk) placeOf(x syntax.Expr) (place, bool) {
	p := place{expr: x}
	for e := x; ; {
		switch t := e.(type) {
		case *syntax.ParenExpr:
			e = t.X
		case *syntax.StarExpr:
			e = t.X
		case *syntax.SelectorExpr:
			p.steps = append(p.steps, placeStep{field: t.Sel.Name})
			e = t.X
		case *syntax.IndexExpr:
			if len(t.Indices) != 1 {
				return p, false
			}
			p.steps = append(p.steps, placeStep{index: t.Indices[0]})
			e = t.X
		case *syntax.Ident:
			v, ok := w.info.Uses[t].(*Var)
			if !ok {
				return p, false
			}
			p.root = v
			for i, j := 0, len(p.steps)-1; i < j; i, j = i+1, j-1 {
				p.steps[i], p.steps[j] = p.steps[j], p.steps[i]
			}
			return p, true
		default:
			return p, false
		}
	}
}
