package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A for-in over a slice or a Map whose body appends one element to a
// local slice on every iteration reserves room for every element before
// it starts (types.md, Loops and len 8):
//
//	for _, v := range src { out = ergo.Append(out, f(v)) }
//
// lowers with `out = ergo.Reserve(out, len(src))` before the loop, so the
// loop grows out's array once, not about log2(len(src)) times. The loop
// qualifies when:
//
//   - its domain is a slice or a Map, whose length is the count of its
//     iterations, or a Range written in the loop whose count is backed by
//     memory or bounded (reserveRange): lo..len(p), lo a non-negative
//     integer literal and p a plain path, counts at most len(p); lo..hi,
//     both integer literals, at most reserveRangeSpan. Any other Range's
//     count is no length of memory, so a reservation could fail for a
//     loop that would have ended first; a string's or a Seq's count is not
//     known before the loop;
//   - one statement at the top of its body is `x = x.Append(e)` (or
//     `x = Append(x, e)`), x a local that the lowering names;
//   - nothing else in the body assigns x, and the body has no break,
//     continue or return, so each iteration appends once;
//   - the domain is a variable or a field path, through no pointer and
//     no call, that does not mention x: a range over x's array sees x's
//     element writes until an append moves the array (Bindings 3), so a
//     reservation would change what the loop reads.
//
// Capacity is not otherwise visible: the value rule (V3) clips or copies
// a slice where it is still seen elsewhere. An iteration that ends the
// loop with a failure or a defect leaves room unused, as hand Go's
// make(T, 0, len(src)) does.
//
// The reference lowering reserves nothing: reserveLowering "off", set by a
// test or by a whole build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.reserveLowering=off,
// so the examples' tests and the conformance corpus run under both
// (TestReserveAgrees).
//
// Prior art: Go's own advice and gopls's prealloc analyzer
// (make([]T, 0, len(src))); Rust's Vec::extend and collect reserve from
// the iterator's size_hint; OCaml's List.map and Gleam's list.map build a
// list of the known length; Elixir's for comprehension over a list
// builds its result in one pass.
var reserveLowering string

// reserveRangeSpan bounds the count of a Range of two literals that a loop
// reserves for: a larger one is left to grow, as any other Range is.
const reserveRangeSpan = 4096

// reserveTarget is the Go name of the local slice that s's body appends
// to once per iteration, or "" when s does not qualify (above).
func (fc *funcCtx) reserveTarget(s *syntax.RangeStmt, domain check.LoopDomain) string {
	switch {
	case reserveLowering == "off":
		return ""
	case domain == check.LoopRange:
		if !fc.reserveRange(s.X) {
			return ""
		}
	case domain != check.LoopSlice && domain != check.LoopMap:
		return ""
	}
	var target *check.Var
	var appendStmt syntax.Stmt
	for _, st := range s.Body.List {
		// A second append of the target is another assignment of it,
		// which the walk below refuses.
		if v := fc.selfAppend(st); v != nil {
			target, appendStmt = v, st
			break
		}
	}
	if target == nil || domain != check.LoopRange && !fc.plainPath(s.X) || fc.mentions(s.X, target) {
		return ""
	}
	name, ok := fc.locals[target]
	if !ok {
		return ""
	}
	clean := true
	syntax.Inspect(s.Body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BranchStmt, *syntax.ReturnStmt:
			clean = false
		case *syntax.AssignStmt:
			if syntax.Stmt(n) != appendStmt && fc.assignsTo(n, target) {
				clean = false
			}
		}
		return clean
	})
	if !clean {
		return ""
	}
	return name
}

// reserveRange reports a Range domain whose count a loop may reserve: a
// range expression written in the loop, lo..len(p) with lo a non-negative
// integer literal and p a plain path, or lo..hi of two integer literals
// at most reserveRangeSpan apart.
func (fc *funcCtx) reserveRange(x syntax.Expr) bool {
	b, ok := unparen(x).(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.DOTDOT {
		return false
	}
	lo, ok := intLiteral(b.X)
	if !ok || lo < 0 {
		return false
	}
	if hi, ok := intLiteral(b.Y); ok {
		return hi >= lo && hi-lo <= reserveRangeSpan
	}
	call, ok := unparen(b.Y).(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	c := fc.l.info.Calls[call]
	return c != nil && c.Kind == check.CallLen && fc.plainPath(call.Args[0])
}

// intLiteral is the value of an integer literal.
func intLiteral(x syntax.Expr) (int64, bool) {
	l, ok := unparen(x).(*syntax.BasicLit)
	if !ok || l.Kind != syntax.INT {
		return 0, false
	}
	v, err := strconv.ParseInt(l.Value, 0, 64)
	return v, err == nil
}

// selfAppend is x when st is `x = x.Append(e)` or `x = Append(x, e)` of
// the prelude's Append, x a variable.
func (fc *funcCtx) selfAppend(st syntax.Stmt) *check.Var {
	a, ok := st.(*syntax.AssignStmt)
	if !ok || a.Tok != syntax.ASSIGN || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
		return nil
	}
	lhs, ok := unparen(a.Lhs[0]).(*syntax.Ident)
	if !ok {
		return nil
	}
	v, ok := fc.l.info.Uses[lhs].(*check.Var)
	if !ok {
		return nil
	}
	call, ok := unparen(a.Rhs[0]).(*syntax.CallExpr)
	if !ok {
		return nil
	}
	c := fc.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || !preludeAppend(c.Func) {
		return nil
	}
	src := c.Recv
	if src == nil {
		if len(call.Args) == 0 {
			return nil
		}
		src = call.Args[0]
	}
	id, ok := unparen(src).(*syntax.Ident)
	if !ok || fc.l.info.Uses[id] != v {
		return nil
	}
	return v
}

// plainPath reports a domain that names its storage: a variable, or a
// field of one, reached through no pointer and no call. Only such a
// domain is known not to share x's array: a pointer, or a call (a
// closure that returns x), could reach x itself.
func (fc *funcCtx) plainPath(x syntax.Expr) bool {
	x = unparen(x)
	if _, ptr := fc.typeOf(x).(*check.Pointer); ptr {
		return false
	}
	switch x := x.(type) {
	case *syntax.Ident:
		_, ok := fc.l.info.Uses[x].(*check.Var)
		return ok
	case *syntax.SelectorExpr:
		if id, ok := unparen(x.X).(*syntax.Ident); ok {
			if _, pkg := fc.l.info.Uses[id].(*check.PkgName); pkg {
				_, isLet := fc.l.info.Uses[x.Sel].(*check.Var)
				return isLet // another package's let
			}
		}
		return fc.plainPath(x.X)
	}
	return false
}

// assignsTo reports whether a assigns v or a place rooted at v.
func (fc *funcCtx) assignsTo(a *syntax.AssignStmt, v *check.Var) bool {
	for _, l := range a.Lhs {
		if fc.mentions(l, v) {
			return true
		}
	}
	return false
}

// mentions reports whether x reads or names v anywhere.
func (fc *funcCtx) mentions(x syntax.Node, v *check.Var) bool {
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && fc.l.info.Uses[id] == v {
			found = true
		}
		return !found
	})
	return found
}

// reserve writes the reservation of a loop, before it: room in target for
// count more elements. A slice's or a Map's count is len(x), x the
// domain's Go value, a plain path (plainPath), so len and the range read
// the same value.
func (fc *funcCtx) reserve(target, count string) {
	fc.line(target + " = " + fc.l.rt() + ".Reserve(" + target + ", " + count + ")")
}

// rangeCount is the count of the Range held in x (a spilled value read
// once): End - Start, or zero for an empty Range.
func (fc *funcCtx) rangeCount(rangeExpr syntax.Expr, x string) string {
	n, ok := fc.typeOf(rangeExpr).(*check.Named)
	if !ok {
		ice(rangeExpr, "a Range domain typed %s", fc.typeOf(rangeExpr))
	}
	td := fc.l.typeDecl(n.Obj, rangeExpr)
	return "max(int(" + x + "." + td.fieldName["End"] + "-" + x + "." + td.fieldName["Start"] + "), 0)"
}
