package lower

import (
	"maps"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A borrow passed on (OWN1, own-read-pass; S2's dump: `let t = d.p.Tree`
// copied the whole tree at every node). own-read (storeread.go) judges
// each use of the root by the storage it can write, and a borrow handed to
// a call, helper(p) or p.m(), counted as a write of every slice and Map
// type its target reaches: node(d, …), which writes only d.b, made the
// let of d.p.Tree copy. A call of a function with an Ergo body now counts
// what that function can write through the parameter: the storage its own
// uses of the parameter write, judged by the same rule, through the calls
// it passes the parameter on to, to a least fixpoint over recursion. Any
// other call (a function value, a Go body) keeps the old count.
//
// It is sound for the reason own-read is: a borrow is the only path to its
// target's storage in the call (types.md, Borrows 2), so the callee writes
// the caller's storage only through that parameter.
//
// The reference is borrowPassLowering "off" (selectable by -X): a borrow
// passed on writes everything its target reaches, as before.

// borrowPassLowering selects the summary: "" uses it; "off" is the
// reference.
var borrowPassLowering string

func borrowPassOn() bool { return borrowPassLowering != "off" }

// borrowSum is what a function writes through a borrow parameter: the
// slice and Map types (refown.go, reaches) and whether it can write
// storage those types do not name.
type borrowSum struct {
	types  map[string]bool
	hidden bool
}

func (s borrowSum) same(o borrowSum) bool {
	return s.hidden == o.hidden && maps.Equal(s.types, o.types)
}

type borrowKey struct {
	f *check.Func
	j int
}

// borrowWrites holds the summaries of one kind (S: borrowSum, fieldSum):
// final ones, and the ones a recursion is still deciding (active), with
// open set when a summary read an active one, so it is not final until
// that one is.
type borrowWrites[S any] struct {
	final  map[borrowKey]S
	active map[borrowKey]S
	open   bool
}

func newBorrowWrites[S any]() *borrowWrites[S] {
	return &borrowWrites[S]{final: map[borrowKey]S{}, active: map[borrowKey]S{}}
}

// borrowRounds bounds the fixpoint of one summary; past it the summary
// writes everything.
const borrowRounds = 64

// fix is the least fixpoint of body for the key k, starting from bottom
// for a summary its own recursion reads; past borrowRounds it is top.
func (b *borrowWrites[S]) fix(k borrowKey, bottom, top S, same func(S, S) bool, body func() S) S {
	if s, ok := b.final[k]; ok {
		return s
	}
	if s, ok := b.active[k]; ok {
		b.open = true
		return s
	}
	outer := b.open
	b.active[k] = bottom
	for range borrowRounds {
		b.open = false
		s := body()
		open := b.open
		if same(s, b.active[k]) {
			delete(b.active, k)
			if !open || len(b.active) == 0 {
				b.final[k] = s
			}
			b.open = outer || open && len(b.active) > 0
			return s
		}
		b.active[k] = s
	}
	delete(b.active, k)
	b.open = outer
	return top
}

// borrowWritten is what f writes through its parameter j (counting a dot
// call's receiver first): the least fixpoint of borrowBody.
func (l *lowerer) borrowWritten(f *check.Func, j int) borrowSum {
	vf := l.valueFacts()
	if vf.borrows == nil {
		vf.borrows = newBorrowWrites[borrowSum]()
	}
	return vf.borrows.fix(borrowKey{f, j}, borrowSum{types: map[string]bool{}}, borrowSum{types: map[string]bool{}, hidden: true}, borrowSum.same, func() borrowSum {
		return l.borrowBody(f, j)
	})
}

// borrowBody is one round of f's summary for parameter j: the storage each
// use of the parameter in f's body can write (storageWritten).
func (l *lowerer) borrowBody(f *check.Func, j int) borrowSum {
	all := borrowSum{types: map[string]bool{}, hidden: true}
	v := declParam(f, j)
	if v == nil {
		return all
	}
	w := l.analyzeValues(f.Decl, f.Pkg.Info)
	if w.root == nil {
		return all // the value rule is off: no walk to read
	}
	s := borrowSum{types: map[string]bool{}}
	vv := w.vars[v]
	if vv == nil {
		return s // the body never names it
	}
	for _, u := range vv.occs {
		if w.writesNothing(u) {
			continue
		}
		if w.storageWritten(u, s.types) {
			s.hidden = true
		}
	}
	return s
}

// declParam is the variable f's body names for its parameter j (the
// declaration's, Defs: emitBody binds those, not the signature's), or nil
// when f has no Ergo body or no such parameter.
func declParam(f *check.Func, j int) *check.Var {
	if f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		return nil
	}
	if f.Decl.Type == nil || f.Decl.Type.Params == nil {
		return nil
	}
	i := 0
	for _, field := range f.Decl.Type.Params.List {
		if len(field.Names) == 0 {
			if i == j {
				return nil
			}
			i++
		}
		for _, id := range field.Names {
			if i == j {
				v, _ := f.Pkg.Info.Defs[id].(*check.Var)
				return v
			}
			i++
		}
	}
	return nil
}

// passedBorrow is the function and parameter index a use u of a borrow
// hands it to: u is the borrow itself (no path below it), an argument or
// the receiver of a call of a function with an Ergo body. It is nil for
// any other use.
func (w *valueWalk) passedBorrow(u *vocc) (*check.Func, int) {
	var call *syntax.CallExpr
	if c := w.dotCall(u.path); c != nil {
		call = c // p.m(…): the receiver
	} else {
		if unparen(u.path) != syntax.Expr(u.id) {
			return nil, 0
		}
		parent, child := w.unparenParent(u.path)
		c, ok := parent.(*syntax.CallExpr)
		if !ok || c.Fun == child {
			return nil, 0
		}
		call = c
	}
	c := w.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil {
		return nil, 0
	}
	for j := 0; ; j++ {
		a := argOf(call, c, j)
		if a == nil {
			return nil, 0
		}
		if unparen(a) == syntax.Expr(u.id) {
			if declParam(c.Func, j) == nil {
				return nil, 0
			}
			return c.Func, j
		}
	}
}
