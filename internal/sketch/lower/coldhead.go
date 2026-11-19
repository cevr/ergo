package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Cold heads (BUN5-cold). A small function that appends to a column
// often begins with a guard that grows the column when it is full:
//
//	func pushW(t *Tree, w uint32) {
//		if t.top >= len(t.stack) {
//			t.stack = slices.AppendAll(t.stack, zeros32(len(t.stack)))
//		}
//		t.stack[t.top] = w
//		t.top = t.top + 1
//	}
//
// The growth runs once per doubling, so almost never; yet the inliner
// wrote it at every inlined site (S2b's parser: 295 copies of pushW's).
// So where the inliner writes such a function, it writes the guard's
// test and, when the test holds, a call of the function itself, out of
// line, which grows the column and does the rest; the body after the
// guard is written at the site as before (partial inlining):
//
//	if t.top >= len(t.stack) {
//		t.pushW(w)
//		goto inl
//	}
//	t.stack[t.top] = w
//	…
//
// The guard is told by its shape alone, never by a name or an
// annotation (coldGuard): an if with no else, first in the body, whose
// condition compares an operand with no call to len(P), and whose body
// only assigns to places, each the result of a call that takes the place
// it writes as its first operand or receiver, one of them P. The test is
// then free of effects, so the call may read it again. The function must
// return nothing, fail nothing and take no environment (pure), no type
// argument, and its every parameter must be a local of the inlined body,
// so the call passes the same values the body has.
//
// Counted on S2b's parser (parsebench1, instructions per byte, GC off):
// 65.60 to 65.07, and its binary's text 125 KB smaller (BUN5-cold). The
// same move for the scanner's growToks, whose guard calls a function,
// cost the scanner 0.6% and is not taken: the guard's body is a call
// there, not a growth, so the shape does not hold.
//
// The reference is coldLowering "copy": the guard is inlined as written.
// TestInlineAgrees runs it as a lowering of its own.
//
// Prior art: GCC (-fpartial-inlining) and LLVM's PartialInliner inline a
// function's entry guard and call the function for the rest; HotSpot
// keeps an uncommon branch as a call (an uncommon trap). Gleam, ReScript
// and Elixir leave inlining to their host (the BEAM, a JavaScript
// engine), so none of them splits a body.
var coldLowering string

// coldGuard is the growth guard f's body begins with, or nil (Cold
// heads, above).
func (l *lowerer) coldGuard(f *check.Func, fd *funcDecl) *syntax.IfStmt {
	if coldLowering == "copy" || f.Decl == nil || f.Decl.Body == nil || len(f.Decl.Body.List) < 2 {
		return nil
	}
	if !fd.pure || fd.fallible || len(f.Sig.Results) > 0 || len(f.Sig.TParams) > 0 {
		return nil
	}
	s, ok := f.Decl.Body.List[0].(*syntax.IfStmt)
	if !ok || s.Else != nil || s.Body == nil || len(s.Body.List) == 0 {
		return nil
	}
	info := f.Pkg.Info
	cond, ok := unparen(s.Cond).(*syntax.BinaryExpr)
	if !ok || cond.Op != syntax.GEQ && cond.Op != syntax.GTR || !callFree(cond.X) {
		return nil
	}
	n, ok := unparen(cond.Y).(*syntax.CallExpr)
	if !ok || len(n.Args) != 1 {
		return nil
	}
	if c := info.Calls[n]; c == nil || c.Kind != check.CallLen {
		return nil
	}
	full := placeText(n.Args[0])
	if full == "" {
		return nil
	}
	grows := false
	for _, st := range s.Body.List {
		a, ok := st.(*syntax.AssignStmt)
		if !ok || a.Tok != syntax.ASSIGN || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
			return nil
		}
		to := placeText(a.Lhs[0])
		call, ok := unparen(a.Rhs[0]).(*syntax.CallExpr)
		if to == "" || !ok || callPlace(info, call) != to {
			return nil
		}
		grows = grows || to == full
	}
	if !grows {
		return nil
	}
	return s
}

// callPlace is the place a call takes first: its receiver, or its first
// operand; "" when that is no place.
func callPlace(info *check.Info, call *syntax.CallExpr) string {
	if c := info.Calls[call]; c != nil && c.Recv != nil {
		return placeText(c.Recv)
	}
	if len(call.Args) == 0 {
		return ""
	}
	return placeText(call.Args[0])
}

// placeText is x as a dotted path, a or a.b.c, or "" when x is no such
// place.
func placeText(x syntax.Expr) string {
	switch e := unparen(x).(type) {
	case *syntax.Ident:
		return e.Name
	case *syntax.SelectorExpr:
		if p := placeText(e.X); p != "" {
			return p + "." + e.Sel.Name
		}
	}
	return ""
}

// callFree reports an operand built of names, fields, literals and
// arithmetic only: reading it again changes nothing.
func callFree(x syntax.Expr) bool {
	switch e := unparen(x).(type) {
	case *syntax.Ident, *syntax.BasicLit:
		return true
	case *syntax.SelectorExpr:
		return callFree(e.X)
	case *syntax.BinaryExpr:
		return callFree(e.X) && callFree(e.Y)
	}
	return false
}

// coldHead writes the test of f's growth guard s in the inlined body ic
// and, when it holds, the call of f out of line with the body's
// parameters, then the jump past the body (Cold heads, above). args are
// the Go names of f's parameters in the body. callDir is the directive of
// the call's own position in the caller ("" with none): the out-of-line
// call is the call itself, so a defect inside f reports the frames a call
// has, not the inlined site's on top of f's own.
func (ic *funcCtx) coldHead(f *check.Func, fd *funcDecl, s *syntax.IfStmt, args []string, label string, site syntax.Node, callDir string) {
	ic.l.useFunc(f, site)
	call := fd.name + "(" + strings.Join(args, ", ") + ")"
	if fd.method != nil {
		call = "(" + args[0] + ")." + fd.name + "(" + strings.Join(args[1:], ", ") + ")"
	}
	ic.at(s)
	ic.line("if " + ic.headerValue(s.Cond) + " {")
	ic.depth++
	if callDir != "" {
		ic.writeDir(callDir)
	}
	ic.line(call)
	ic.at(s)
	ic.line("goto " + label)
	ic.depth--
	ic.line("}")
	ic.l.stats.ColdCalls++
}
