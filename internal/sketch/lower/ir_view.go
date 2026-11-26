package lower

// IR builder: the helpers of match fusion (ir_forms_match.go) that read a
// view's lets and results. A view function reads a row as a variant: its
// body is lets, then one match on the row's kind whose every arm builds one
// variant (lane B's B2 view walk). VW1 built a match on a view call as the
// view's switch; IRF-1's fusion (ir_forms_match.go) does that for any small
// callee, with its lets sunk into the results that read them (VW1b).

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// viewBudget is the most syntax nodes a view's body (fuseView) has to be
// built at a match site: B2's 60-kind view is about 1,500.
const viewBudget = 2048

// variant is the value of a view arm's x as its variant: its conversion
// to the view's union, which the checker records at x, is left out while
// x is built, since no union is built.
func (b *irBuilder) variant(x syntax.Expr) ([]*irStmt, *irExpr) {
	if _, ok := b.l.info.Implicit[x]; ok {
		if b.skipImplicit == nil {
			b.skipImplicit = map[syntax.Expr]bool{}
		}
		prev := b.skipImplicit[x]
		b.skipImplicit[x] = true
		defer func() { b.skipImplicit[x] = prev }()
	}
	return b.value(x)
}

// sameLocal is the local of a, when a is an immutable local of the caller
// with no conversion: the view's parameter can be that local.
func (b *irBuilder) sameLocal(a syntax.Expr) (int, bool) {
	id, ok := unparen(a).(*syntax.Ident)
	if !ok {
		return 0, false
	}
	if _, conv := b.l.info.Implicit[a]; conv {
		return 0, false
	}
	w, ok := b.l.info.Uses[id].(*check.Var)
	if !ok || w.Mut() {
		return 0, false
	}
	l, ok := b.locals[w]
	return l, ok
}

// A sunk let (VW1b) is built in the cases whose literal reads it, not
// before the switch: a row of a store reads, by its kind, only the columns
// its variant holds. A let sinks when its value can neither fail nor have
// an effect except by an index out of range: locals, constants, fields,
// slice indexes, conversions of basic values, and operators that cannot
// fail (no division, remainder or shift). Its index checks stay in its
// place (sunkLet.checks, `_ = xs[i]`): Go checks the bound and loads
// nothing, so a row out of range fails where and as it did. A let the
// view's subject reads, or a let that stays reads, stays.
type sunkLet struct {
	v      *check.Var
	value  syntax.Expr
	checks []syntax.Expr
}

// sunkLetOf is st as a let that may sink, or nil.
func (b *irBuilder) sunkLetOf(st syntax.Stmt) *sunkLet {
	ds, ok := st.(*syntax.DeclStmt)
	if !ok || ds.Decl == nil || len(ds.Decl.Specs) != 1 {
		return nil
	}
	sp := ds.Decl.Specs[0]
	if len(sp.Names) != 1 || sp.Pattern != nil || len(sp.Values) != 1 {
		return nil
	}
	w, ok := b.l.info.Defs[sp.Names[0]].(*check.Var)
	if !ok || w.Mut() || len(b.l.info.Releases[st]) > 0 {
		return nil
	}
	d := &sunkLet{v: w, value: sp.Values[0]}
	if !b.cannotFail(sp.Values[0], &d.checks) {
		return nil
	}
	return d
}

// cannotFail reports whether x can fail only by an index out of range,
// with no effect; checks gets its indexes, innermost first.
func (b *irBuilder) cannotFail(x syntax.Expr, checks *[]syntax.Expr) bool {
	if _, conv := b.l.info.Implicit[x]; conv {
		return false
	}
	switch y := x.(type) {
	case *syntax.ParenExpr:
		return b.cannotFail(y.X, checks)
	case *syntax.BasicLit:
		return true
	case *syntax.Ident:
		switch b.l.info.Uses[y].(type) {
		case *check.Var, *check.Const:
			return true
		}
	case *syntax.SelectorExpr:
		if _, ok := b.l.info.Types[y.X].(*check.Named); ok {
			return b.cannotFail(y.X, checks)
		}
	case *syntax.IndexExpr:
		if _, ok := b.l.info.Types[y.X].(*check.Slice); !ok || len(y.Indices) != 1 {
			return false
		}
		if !b.cannotFail(y.X, checks) || !b.cannotFail(y.Indices[0], checks) {
			return false
		}
		*checks = append(*checks, y)
		return true
	case *syntax.UnaryExpr:
		switch y.Op {
		case syntax.ADD, syntax.SUB, syntax.XOR, syntax.NOT:
			return b.cannotFail(y.X, checks)
		}
	case *syntax.BinaryExpr:
		switch y.Op {
		case syntax.ADD, syntax.SUB, syntax.MUL, syntax.AND, syntax.OR, syntax.XOR, syntax.AND_NOT,
			syntax.LAND, syntax.LOR, syntax.EQL, syntax.NEQ, syntax.LSS, syntax.GTR, syntax.LEQ, syntax.GEQ:
			return b.cannotFail(y.X, checks) && b.cannotFail(y.Y, checks)
		}
	case *syntax.CallExpr:
		c := b.l.info.Calls[y]
		if c == nil || c.Kind != check.CallConversion || len(y.Args) != 1 || y.Trailing != nil {
			return false
		}
		return basicOf(b.l.info.Types[y.Args[0]]) != nil && basicOf(b.l.info.Types[y]) != nil && b.cannotFail(y.Args[0], checks)
	}
	return false
}

// reads are the variables x reads.
func (b *irBuilder) reads(x syntax.Node) map[*check.Var]bool {
	out := map[*check.Var]bool{}
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if w, ok := b.l.info.Uses[id].(*check.Var); ok {
				out[w] = true
			}
		}
		return true
	})
	return out
}

// needs are the sunk lets x reads, with the sunk lets they read.
func (b *irBuilder) needs(x syntax.Expr, sunk map[syntax.Stmt]*sunkLet) map[syntax.Stmt]bool {
	out := map[syntax.Stmt]bool{}
	byVar := map[*check.Var]syntax.Stmt{}
	for st, d := range sunk {
		byVar[d.v] = st
	}
	var add func(syntax.Node)
	add = func(n syntax.Node) {
		for w := range b.reads(n) {
			if st, ok := byVar[w]; ok && !out[st] {
				out[st] = true
				add(sunk[st].value)
			}
		}
	}
	add(x)
	return out
}

// rename gives the locals the view's body declared from index k on the
// temporaries' names: the view's `let a` now sits in its caller's scope,
// whose body may spell a, or hold a second copy of the view.
func (b *irBuilder) rename(k int) {
	for i := k; i < len(b.fn.Locals); i++ {
		b.fn.Locals[i].Name = b.names.alloc(b.fn.Locals[i].Name)
	}
}
