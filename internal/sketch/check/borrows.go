package check

import (
	"fmt"
	"go/constant"
	"go/token"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Overlapping borrows (#197 C2; types.md, Borrows 2): the borrows one call
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
// Prior art: Swift's law of exclusivity refuses two overlapping inout
// accesses statically where it can and checks the rest at run time; Rust
// refuses `f(&mut xs[0], &mut xs[1])` outright and offers `split_at_mut`
// and `get_many_mut`, which check the indexes at run time; Ergo decides
// constants statically and asks for the guard otherwise, so no run-time
// check is emitted. Gleam, Elixir and ReScript have no mutable borrows.

const (
	borrowOverlapText = "%s and %s are borrows of one place; a call takes no two overlapping borrows: pass one, or two places apart (types.md, Borrows 2)"
	borrowMaybeText   = "%s and %s may be borrows of one place, since %s may equal %s; guard the call with if %s != %s, with each index a constant or a let (types.md, Borrows 2)"
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

// checkBorrows applies the overlap rule to the file f.
func (c *checker) checkBorrows(f *fileInfo) {
	w := &borrowWalk{c: c, info: c.pkg.Info}
	for _, d := range f.syntax.Decls {
		w.visit(d)
	}
}

type borrowWalk struct {
	c     *checker
	info  *Info
	stack []syntax.Node
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

// call checks the borrows of one call.
func (w *borrowWalk) call(call *syntax.CallExpr) {
	var places []place
	add := func(x syntax.Expr) {
		if p, ok := w.placeOf(x); ok {
			places = append(places, p)
		}
	}
	if k := w.info.Calls[call]; k != nil && k.Kind == CallFunc && k.Recv != nil && k.Func != nil && k.Func.Sig != nil &&
		len(k.Func.Sig.Params) > 0 && isPointer(k.Func.Sig.Params[0].Type) {
		add(k.Recv) // the implicit borrow, or a borrow passed on
	}
	for _, a := range call.Args {
		switch u := unparen(a).(type) {
		case *syntax.UnaryExpr:
			if u.Op == syntax.AND {
				add(u.X)
			}
		case *syntax.Ident:
			if isPointer(w.info.Types[a]) {
				add(u)
			}
		}
	}
	for j := 1; j < len(places); j++ {
		for i := 0; i < j; i++ {
			if w.report(places[i], places[j]) {
				break
			}
		}
	}
}

// report reports S108 when a and b may overlap, and says whether it did.
func (w *borrowWalk) report(a, b place) bool {
	if a.root != b.root {
		return false
	}
	var maybe [2]syntax.Expr
	for k := 0; k < len(a.steps) && k < len(b.steps); k++ {
		x, y := a.steps[k], b.steps[k]
		if x.index == nil {
			if x.field != y.field {
				return false
			}
			continue
		}
		switch w.indexRel(x.index, y.index) {
		case relApart:
			return false
		case relMaybe:
			if maybe[0] == nil {
				maybe = [2]syntax.Expr{x.index, y.index}
			}
		}
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
	case *syntax.ReturnStmt, *syntax.BranchStmt:
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
