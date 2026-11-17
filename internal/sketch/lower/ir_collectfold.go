package lower

// IR builder: a collect whose slice is only folded (FLIP F7 GP-FOLD). See
// ir_loops.go and loops.go.
//
// The three statements
//
//	let mut ys = []T{}
//	for i in lo..hi { ys = ys.Append(Q[i+s]) }   // a collect (DT4)
//	for y in ys { body }                          // the only read of ys
//
// build no slice when the collect's guard holds: the fold ranges over the
// part Q[lo+s:hi+s] itself, a view of Q, so there is no growth, no copy
// and no allocation, where hand Go collects first and then folds:
//
//	var src []T
//	if guard { src = Q[a:b] } else { the loop; src = ys }
//	for y in src { body }
//
// The else branch is the collect as the program wrote it, so a part out of
// bounds raises the defect it raises today. The fold sees the elements the
// slice would hold, in its order and at its positions (a for with an index
// counts from 0 in both), when:
//
//  1. ys starts empty (its let is the statement before, an empty literal),
//     and nothing reads ys after the fold or in its body: the slice is
//     only folded, so ys staying empty is never seen.
//  2. Q's elements do not change while the fold runs. The body names no
//     variable that holds a slice or a Map, a pointer, a function or any
//     other value that could reach Q's array: only scalars and records of
//     them. Its calls then take no such value either, and a function
//     literal is not taken. In the program as written, Q is read only in
//     the collect, and the value rule may let a later write reuse a dead
//     Q's array; a body of scalars writes no array at all.
//
// The reference is collectFoldLowering "off" (a test, or a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.collectFoldLowering=off):
// the collect builds the slice and the fold ranges over it.
//
// Prior art: deforestation (Wadler) and GHC's foldr/build fusion remove an
// intermediate list a consumer only folds; Rust's iterator chains and
// Java's streams never build one; Elixir's Stream and Gleam's iterator
// fuse lazily at run time, at a closure call per element. Here the fusion
// is decided at build time on the checker's facts, with no closure.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

var collectFoldLowering string

// foldPlan is one collect whose slice only the next loop folds: the local
// src the fold ranges over, set once the collect is built with it.
type foldPlan struct {
	fold *syntax.RangeStmt
	src  int
	done bool
}

// planFold notes the collect list[i] whose slice only list[i+1] folds
// (rules 1 and 2 above), for countLoop and the fold's range to read.
func (b *irBuilder) planFold(list []syntax.Stmt, i int) {
	if collectFoldLowering == "off" || i == 0 || i+1 >= len(list) {
		return
	}
	s, ok := list[i].(*syntax.RangeStmt)
	if !ok {
		return
	}
	lp := b.l.info.Loops[s]
	if lp == nil || lp.Domain != check.LoopRange {
		return
	}
	idiom := b.loopIdiom(s, lp) // ir_loops.go
	if idiom == nil || idiom.kind != "collect" {
		return
	}
	v, ok := b.l.info.Uses[idiom.xs].(*check.Var)
	if !ok || !b.startsEmpty(list[i-1], v) {
		return
	}
	f, ok := list[i+1].(*syntax.RangeStmt)
	if !ok {
		return
	}
	if fl := b.l.info.Loops[f]; fl == nil || fl.Domain != check.LoopSlice {
		return
	}
	if id, ok := unparen(f.X).(*syntax.Ident); !ok || b.l.info.Uses[id] != v {
		return
	}
	for _, n := range append([]syntax.Node{f.Body}, nodes(list[i+2:])...) {
		if b.namesVar(n, v) {
			return
		}
	}
	if !b.scalarBody(f) {
		return
	}
	if b.folds == nil {
		b.folds = map[*syntax.RangeStmt]*foldPlan{}
	}
	plan := &foldPlan{fold: f, src: -1}
	b.folds[s], b.folds[f] = plan, plan
}

// startsEmpty reports that s is `let mut ys = []T{}`, ys the variable v.
func (b *irBuilder) startsEmpty(s syntax.Stmt, v *check.Var) bool {
	d, ok := s.(*syntax.DeclStmt)
	if !ok || d.Decl == nil || len(d.Decl.Specs) != 1 {
		return false
	}
	sp := d.Decl.Specs[0]
	if len(sp.Names) != 1 || b.l.info.Defs[sp.Names[0]] != v || len(sp.Values) != 1 {
		return false
	}
	lit, ok := unparen(sp.Values[0]).(*syntax.CompositeLit)
	return ok && len(lit.Elts) == 0
}

// nodes is the statements as nodes.
func nodes(list []syntax.Stmt) []syntax.Node {
	out := make([]syntax.Node, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// namesVar reports a name of v under n.
func (b *irBuilder) namesVar(n syntax.Node, v *check.Var) bool {
	found := false
	syntax.Inspect(n, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && b.l.info.Uses[id] == v {
			found = true
		}
		return !found
	})
	return found
}

// scalarBody reports a fold f whose body names only variables of scalar
// types, and records of them, and takes no function literal (rule 2).
func (b *irBuilder) scalarBody(f *syntax.RangeStmt) bool {
	ok := true
	syntax.Inspect(f.Body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			ok = false
		case *syntax.Ident:
			if v, isVar := b.l.info.Uses[n].(*check.Var); isVar && !b.scalarType(v.Type, map[check.Type]bool{}) {
				ok = false
			}
		}
		return ok
	})
	return ok
}

// scalarType reports a basic type, or a record whose fields are all of
// scalar types.
func (b *irBuilder) scalarType(t check.Type, seen map[check.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch u := underEqual(t).(type) {
	case *check.Basic:
		return true
	case *check.Struct:
		for _, f := range u.Fields {
			if !b.scalarType(f.Type, seen) {
				return false
			}
		}
		return true
	}
	return false
}

// foldCollect is the collect s of plan written for its fold: before it,
// the source's declaration; when the guard holds, the source is the part
// q of Q; else the loop builds ys and the source is ys. nil keeps the
// collect: an ys of a defined slice type.
func (b *irBuilder) foldCollect(plan *foldPlan, idiom *loopIdiom, elem check.Type, p irPos, q *irExpr, loop *irStmt) (head, then, els []*irStmt) {
	st := "[]" + b.goType(elem, idiom.call)
	if b.goType(b.l.info.Types[idiom.xs], idiom.xs) != st {
		return nil, nil, nil
	}
	xs := b.locals[b.l.info.Uses[idiom.xs].(*check.Var)]
	plan.src = b.temp("fold", st, p)
	head = []*irStmt{{K: "var", Pos: p, Local: plan.src}}
	then = []*irStmt{{K: "set", Pos: p, Local: plan.src, X: q}}
	els = []*irStmt{{K: "set", Pos: loop.End, Local: plan.src, X: &irExpr{K: "local", Pos: loop.End, I: xs}}}
	return head, then, els
}

// foldRangeValue is the value the for-in s ranges over: the source its
// collect set when s is a fused fold, else rangeValue's.
func (b *irBuilder) foldRangeValue(s *syntax.RangeStmt) ([]*irStmt, *irExpr) {
	if plan := b.folds[s]; plan != nil && plan.fold == s && plan.done {
		return nil, &irExpr{K: "local", Pos: b.pos(s.X, s.X.Pos()), I: plan.src}
	}
	return b.rangeValue(s.X) // ir_order.go
}
