package lower

// IR builder: the presized fill (B-FILL; not loops.go's fill idiom, which
// writes one value over a range). A slice built by a count loop that
// appends one value per iteration to an empty slice,
//
//	let mut xs = []T{}
//	for i in a..b {
//		xs = xs.Append(e)
//	}
//
// is the slice made at its final length, then filled by index:
//
//	xs := v1.SliceOfLen[T](b - a)
//	for i := a; i < b; i++ { xs[i - a] = e }
//
// The loop makes the same values in the same order, and the slice holds
// them, as the appends would. What changes is the work: one allocation,
// not one per doubling, no capacity check per value, and a length Go
// knows. With constant bounds, Go's prove pass then drops the bounds checks
// that slice's later reads can decide (the IR-FORMS opt probe's
// keys[k & 63]).
//
// The form applies only where nothing can tell the difference:
//   - the let is the statement just before the loop, so no statement
//     reads the empty slice between them;
//   - the loop's body is that one assignment, so it neither breaks nor
//     continues nor reads the slice anywhere else;
//   - e does not name the slice, so no value depends on its growing
//     length;
//   - each bound is a constant or a local, which reads the same before
//     the let and at the loop (the length is computed before the Range);
//   - the Range's values are Go ints (the index) and the element is a
//     basic type, which owns nothing;
//   - the loop is no loop idiom (ir_loops.go): a collect's guarded call
//     appends the part Q[a:b] to the slice at once, or takes it.
//
// presizeLowering "off" keeps the appends: the reference that the
// property (irspike TestIRFillsMatchSource) and the probe's receipt
// compare against.
//
// Prior art: Gleam and Elixir build a list by recursion and prepend, then
// reverse; their compilers do not preallocate. Rust's
// `(a..b).map(f).collect::<Vec<_>>()` preallocates through the
// iterator's exact size_hint (TrustedLen), and LLVM then removes the
// bounds checks. This is the same fact taken from the loop's shape.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// presizeLowering selects the presized fill: "" builds it, "off" keeps
// the appends (the reference). Set with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.presizeLowering=off.
var presizeLowering string

// fillState is a builder's fill facts.
type fillState struct {
	// prev is the statement before at, in their list: a statement built
	// outside tailTo's loop (bodyStmts' last) finds at is not itself.
	prev, at syntax.Stmt
	// lets are the defs of the lets of an empty slice, by variable.
	lets map[*check.Var]*irStmt
	// site is the assignment the loop being built fills by index.
	site *fillSite
}

// fillSite is a filled loop's one assignment and its index.
type fillSite struct {
	assign *syntax.AssignStmt
	elem   syntax.Expr
	slice  int
	index  *irExpr
}

// fillPrev records the statement before list[i] (tailTo).
func (b *irBuilder) fillPrev(list []syntax.Stmt, i int) {
	b.fills.prev, b.fills.at = nil, list[i]
	if i > 0 {
		b.fills.prev = list[i-1]
	}
}

// fillLet records def, the let of v, when its value is an empty slice
// literal.
func (b *irBuilder) fillLet(v *check.Var, value syntax.Expr, def *irStmt) {
	lit, ok := unparen(value).(*syntax.CompositeLit)
	if !ok || len(lit.Elts) != 0 || lit.Base != nil {
		return
	}
	if _, ok := b.l.info.Types[value].(*check.Slice); !ok {
		return
	}
	if b.fills.lets == nil {
		b.fills.lets = map[*check.Var]*irStmt{}
	}
	b.fills.lets[v] = def
}

// fillLoop makes s, the count loop of lp whose Range's Start is start and
// whose binding is the local loop, a filled loop when it is one (the file
// comment): the let's value becomes the slice at its length, and
// fillStore builds the assignment. Its result ends the fill once the
// loop's body is built.
func (b *irBuilder) fillLoop(s *syntax.RangeStmt, lp *check.Loop, start *irExpr, loop int, p irPos) func() {
	done := func() {}
	if presizeLowering == "off" || b.loopIdiom(s, lp) != nil {
		// A loop idiom (ir_loops.go) writes the loop's guarded call
		// before it: a collect appends the part Q[a:b] to the slice, or
		// takes it, which a slice presized to the loop's length would
		// hold after its zero values (IRF-2: stackvm's held gave
		// [0 5] for [5]).
		return done
	}
	decl, ok := b.fills.prev.(*syntax.DeclStmt)
	if !ok || b.fills.at != syntax.Stmt(s) || !decl.Decl.Mut.IsValid() || len(decl.Decl.Specs) != 1 || len(decl.Decl.Specs[0].Names) != 1 {
		return done
	}
	v, ok := b.l.info.Defs[decl.Decl.Specs[0].Names[0]].(*check.Var)
	def := b.fills.lets[v]
	if !ok || def == nil || len(s.Body.List) != 1 {
		return done
	}
	assign, ok := s.Body.List[0].(*syntax.AssignStmt)
	if !ok || assign.Tok != syntax.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || !b.isNameOf(assign.Lhs[0], v) {
		return done
	}
	elem, ok := b.appendOne(assign.Rhs[0], v)
	if !ok || b.mentions(elem, v) {
		return done
	}
	bx, ok := unparen(s.X).(*syntax.BinaryExpr)
	if !ok || bx.Op.String() != ".." || !b.stable(bx.X, v) || !b.stable(bx.Y, v) {
		return done
	}
	sl, _ := b.l.info.Types[decl.Decl.Specs[0].Values[0]].(*check.Slice)
	if sl == nil || b.scalarBasic(sl.Elem) == nil || b.goType(lp.Elem, s) != "int" {
		return done
	}
	et := b.goType(sl.Elem, s)
	if b.goType(b.operandType(elem), elem) != et {
		return done
	}
	n := &irExpr{K: "bin", Pos: p, Op: "-", X: b.expr(bx.Y), Y: b.expr(bx.X)}
	b.keepImport("var _ = " + b.l.rt() + ".SliceOfLen[" + et + "]")
	def.X = &irExpr{K: "pcall", Pos: def.Pos, T: runtimeImport, Name: "SliceOfLen", V: et, Args: []*irExpr{n}}
	b.fills.site = &fillSite{
		assign: assign,
		elem:   elem,
		slice:  b.locals[v],
		index:  &irExpr{K: "bin", Pos: p, Op: "-", X: &irExpr{K: "local", Pos: p, I: loop}, Y: start},
	}
	return func() { b.fills.site = nil }
}

// fillStore is the filled loop's assignment s: its value stored at the
// iteration's index.
func (b *irBuilder) fillStore(s *syntax.AssignStmt) ([]*irStmt, bool) {
	site := b.fills.site
	if site == nil || site.assign != s {
		return nil, false
	}
	p := b.pos(s, s.Pos())
	pre, vals := b.operands([]syntax.Expr{site.elem})
	at := &irExpr{K: "index", Pos: p, X: &irExpr{K: "local", Pos: p, I: site.slice}, Y: site.index}
	return append(pre, &irStmt{K: "store", Pos: p, X: at, Y: vals[0]}), true
}

// appendOne is the one value x appends to v, when x is the prelude's
// Append of v and one value, Go's builtin append.
func (b *irBuilder) appendOne(x syntax.Expr, v *check.Var) (syntax.Expr, bool) {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok || call.Trailing != nil {
		return nil, false
	}
	c := b.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || b.l.intrinsic(c.Func) == nil || !b.builtinAppend(c.Func) {
		return nil, false
	}
	args := call.Args
	if c.Recv != nil {
		args = append([]syntax.Expr{c.Recv}, args...)
	}
	if len(args) != 2 || !b.isNameOf(args[0], v) {
		return nil, false
	}
	return args[1], true
}

// isNameOf reports x as the name of v.
func (b *irBuilder) isNameOf(x syntax.Expr, v *check.Var) bool {
	id, ok := unparen(x).(*syntax.Ident)
	return ok && b.l.info.Uses[id] == v
}

// mentions reports a name of v in x.
func (b *irBuilder) mentions(x syntax.Expr, v *check.Var) bool {
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && b.l.info.Uses[id] == v {
			found = true
		}
		return !found
	})
	return found
}

// stable reports a bound x that reads the same before the let of v and
// at the loop: a constant, or a local other than v.
func (b *irBuilder) stable(x syntax.Expr, v *check.Var) bool {
	if _, ok := b.l.info.Values[x]; ok {
		return true
	}
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return false
	}
	u, ok := b.l.info.Uses[id].(*check.Var)
	_, local := b.locals[u]
	return ok && u != v && local
}
