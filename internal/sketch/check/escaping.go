package check

import (
	"sort"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Non-escaping function values (check-v1 stage E2, C16 and C17; 196e;
// types.md, Function values 2; spec Q1).
//
// A parameter whose type is written as a function type without `escaping`
// is non-escaping: its type is a Signature with NonEscaping set, and so is
// the type of every local a let binds from it. A function type anywhere
// else (a field, a result, an element, a type argument, a defined type's
// base, a local's written type) is escaping, so `escaping` there only
// repeats it. A non-escaping value lives no longer than the call that
// received it. One rule over its uses keeps it so:
//
//	every use of a non-escaping local is one of
//	  1. the callee of a call;
//	  2. an argument whose parameter is non-escaping;
//	  3. the value of a local let that binds one name, whose type is then
//	     non-escaping too;
//	  4. the target of an assignment, or its value when the target is a
//	     non-escaping local declared in the value's frame or in one nested
//	     in it;
//	and every frame between the frame that declares the local and the use
//	is non-escaping.
//
// Every other use is S108 at the use: a field value, a return or block
// result, an element, an argument of an escaping or generic parameter (a
// type argument is never non-escaping), a conversion, a tuple, a match
// subject. The rule is an allow-list over the parent of the use, so a
// position the checker gains later is refused until it is listed here
// (escaping_e2_test.go enumerates the positions from the syntax package).
//
// A frame is non-escaping when it cannot outlive the call it is passed to:
// a function literal that is the callee of a call or an argument of a
// non-escaping parameter, and a trailing block whose parameter is
// non-escaping (C17: the trailing block is the escape boundary; a block
// passed to an `escaping` parameter, such as task.Go's, is kept by its
// callee). A with's block is no frame: it runs in place. Every other
// function literal is an escaping frame.
//
// Function types compare with the marker: func(g func()) and
// func(g escaping func()) are distinct types (intern.go, mayBeIdentical),
// so a function value cannot launder a non-escaping argument through a
// parameter that keeps it.
//
// Prior art: Swift closures are non-escaping by default and `@escaping`
// marks the exception; a non-escaping closure may only be called or passed
// on as non-escaping, and a closure that captures one is non-escaping
// itself. Kotlin's inline lambdas (and `crossinline`) do the same for
// inline functions. Rust expresses it with lifetimes ('static bounds for a
// stored closure). Gleam, Elixir, ReScript and MoonBit have no marker:
// every function value may escape. Go has none either; its escape analysis
// decides heap allocation, not legality.

// escapeText and captureText are the two S108 messages.
const (
	escapeText  = "%s is a non-escaping function value: it may be called or passed to a non-escaping parameter, never stored, returned or passed where it may be kept; write `escaping` on its parameter's type to keep it (types.md, Function values 2)"
	captureText = "%s is a non-escaping function value, and this function literal or block may outlive the call that received it; capture it only in a block or literal passed to a non-escaping parameter, or write `escaping` on its parameter's type (types.md, Function values 2)"
)

// nonEscaping reports whether t is a non-escaping function type.
func nonEscaping(t Type) bool {
	s, ok := t.(*Signature)
	return ok && s.NonEscaping
}

// checkEscapes applies the rule to every file once Info's calls are final.
func (c *checker) checkEscapes() {
	for _, f := range c.files {
		c.cur = f
		w := &escWalk{c: c, info: c.pkg.Info, varFrame: map[*Var]int{}, declDepth: map[*Var]int{}, writes: map[*syntax.Ident]bool{}}
		for _, d := range f.syntax.Decls {
			w.localFrames(d)
			w.visit(d)
			w.finishCaptures()
		}
		c.checkBorrows(f)
	}
}

type escWalk struct {
	c    *checker
	info *Info
	// stack is the path from the declaration to the node being visited.
	stack []syntax.Node
	// frames are the function bodies around the node, each with whether
	// it may outlive the call it is passed to.
	frames []bool
	// varFrame is the index in frames of the frame that declares a local
	// (-1 at the package level).
	varFrame map[*Var]int

	// The capture rule (captures below): frameNodes parallels frames;
	// regions are the frames and loops around the node, which may run
	// again after a capture; declDepth is len(regions) where a local is
	// declared; writes are the root identifiers of writes; events are the
	// uses of let mut locals in the declaration being visited.
	frameNodes []syntax.Node
	regions    []syntax.Node
	declDepth  map[*Var]int
	writes     map[*syntax.Ident]bool
	events     map[*Var][]mutEvent
	order      []*Var

	// Local frames (localFrames below): collecting is set during the
	// first walk of a declaration, which records the uses of each local
	// a let binds to a function literal (litVar); local holds the
	// literals found to be non-escaping frames.
	collecting bool
	litVar     map[*syntax.FuncLit]*Var
	litUses    map[*Var][]litUse
}

// litUse is a use of a local bound to a function literal: whether its
// position keeps the value from escaping, and the frames around it.
type litUse struct {
	allowed bool
	frames  []syntax.Node
	esc     []bool
}

func (w *escWalk) visit(n syntax.Node) {
	if x, ok := n.(syntax.Expr); ok && w.c.typeNodes[x] {
		return
	}
	if id, ok := n.(*syntax.Ident); ok {
		if v, ok := w.info.Defs[id].(*Var); ok {
			w.varFrame[v] = len(w.frames) - 1
			w.declDepth[v] = len(w.regions)
		} else if v, ok := w.info.Uses[id].(*Var); ok && !v.pkg && w.collecting {
			if _, ok := w.litUses[v]; ok {
				w.litUses[v] = append(w.litUses[v], litUse{allowed: w.allowed(id, v), frames: append([]syntax.Node(nil), w.frameNodes...), esc: append([]bool(nil), w.frames...)})
			}
		} else if ok && !v.pkg && !w.collecting {
			if nonEscaping(v.Type) {
				w.use(id, v)
			}
			// A local of an invalid type has its own diagnostic (D3).
			switch {
			case v.Type == nil || isInvalid(v.Type):
			case v.mut:
				w.mutUse(id, v)
			case isPointer(v.Type):
				w.borrowUse(id, v)
			}
		}
		return
	}
	if !w.collecting {
		w.markWrites(n)
	} else if s, ok := n.(*syntax.VarSpec); ok {
		w.letLiteral(s)
	}
	frame, escaping := w.frame(n)
	if frame {
		w.frames = append(w.frames, escaping)
		w.frameNodes = append(w.frameNodes, n)
	}
	region := frame
	switch n.(type) {
	case *syntax.ForStmt, *syntax.RangeStmt:
		region = true
	}
	if region {
		w.regions = append(w.regions, n)
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
	if region {
		w.regions = w.regions[:len(w.regions)-1]
	}
	if frame {
		w.frames = w.frames[:len(w.frames)-1]
		w.frameNodes = w.frameNodes[:len(w.frameNodes)-1]
	}
}

// frame reports whether n opens a frame and whether that frame may
// outlive the call it is passed to.
func (w *escWalk) frame(n syntax.Node) (frame, escaping bool) {
	switch n := n.(type) {
	case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl:
		return true, false
	case *syntax.FuncLit:
		parent, child := w.parentOf(n)
		call, ok := parent.(*syntax.CallExpr)
		if !ok {
			// A literal a let binds is a local frame when the local only
			// runs in place (localFrames); the first walk is deciding.
			return true, w.collecting || !w.info.LocalFrames[n]
		}
		if call.Fun == child {
			return true, false
		}
		for i, a := range call.Args {
			if a == child {
				return true, !w.paramNonEscaping(call, i)
			}
		}
		return true, true
	case *syntax.BlockStmt:
		call, ok := w.parent().(*syntax.CallExpr)
		if !ok || call.Trailing != n {
			return false, false
		}
		if k := w.info.Calls[call]; k != nil && k.Kind == CallWith {
			return false, false
		}
		return true, !w.paramNonEscaping(call, len(call.Args))
	}
	return false, false
}

func (w *escWalk) parent() syntax.Node {
	if len(w.stack) == 0 {
		return nil
	}
	return w.stack[len(w.stack)-1]
}

// parentOf is the nearest ancestor of n, the node being visited (not yet
// on the stack), that is not a parenthesis, with its child on the path
// (n or the outermost parenthesis around it).
func (w *escWalk) parentOf(n syntax.Node) (parent, child syntax.Node) {
	child = n
	for j := len(w.stack) - 1; j >= 0; j-- {
		if _, ok := w.stack[j].(*syntax.ParenExpr); ok {
			child = w.stack[j]
			continue
		}
		return w.stack[j], child
	}
	return nil, child
}

// paramNonEscaping reports whether the i-th argument of call binds a
// non-escaping parameter: the trailing block is the argument after the
// written ones. A dot call's receiver is never a non-escaping value: it
// has the named type its function is declared on (196m). A call that was
// never checked (its callee is in error, such as a recursive group that
// needs its row written) has no Calls entry; its own diagnostic stands, so
// it adds none here, as in the Task use rule (taskescape.go).
func (w *escWalk) paramNonEscaping(call *syntax.CallExpr, i int) bool {
	k := w.info.Calls[call]
	if k == nil {
		return true
	}
	var sig *Signature
	switch k.Kind {
	case CallFunc:
		if k.Func != nil {
			sig = k.Func.Sig
		}
		if k.Recv != nil {
			i++ // a 196m dot call: the receiver is the first parameter
		}
	case CallOp, CallProviderOp, CallLawOp:
		if k.Op != nil {
			sig = k.Op.Sig
		}
	case CallFuncValue:
		sig, _ = under(w.info.Types[call.Fun]).(*Signature)
	}
	return sig != nil && i < len(sig.Params) && nonEscaping(sig.Params[i].Type)
}

// use checks one use of the non-escaping local v.
func (w *escWalk) use(id *syntax.Ident, v *Var) {
	if !w.allowed(id, v) {
		w.c.errorf(id, CodeType, escapeText, id.Name)
		return
	}
	from, ok := w.varFrame[v]
	if !ok {
		return
	}
	for k := from + 1; k < len(w.frames); k++ {
		if w.frames[k] {
			w.c.errorf(id, CodeType, captureText, id.Name)
			return
		}
	}
}

// allowed reports whether the use id of v stands in one of the rule's four
// positions.
func (w *escWalk) allowed(id *syntax.Ident, v *Var) bool {
	parent, child := w.parentOf(id)
	switch p := parent.(type) {
	case *syntax.CallExpr:
		if p.Fun == child {
			k := w.info.Calls[p]
			return k == nil || k.Kind == CallFuncValue
		}
		for i, a := range p.Args {
			if a == child {
				return w.paramNonEscaping(p, i)
			}
		}
	case *syntax.VarSpec:
		// A local let: a package let's value is no use of a local.
		if len(p.Names) == 1 && p.Pattern == nil && len(p.Values) == 1 && p.Values[0] == child {
			if p.Names[0].Name == "_" {
				return true // a discard keeps nothing
			}
			t, ok := w.info.Defs[p.Names[0]].(*Var)
			return ok && nonEscaping(t.Type)
		}
	case *syntax.AssignStmt:
		for _, l := range p.Lhs {
			if l == child {
				return true // the target; the value assigned is its own use
			}
		}
		if p.Tok != syntax.ASSIGN || len(p.Lhs) != len(p.Rhs) {
			return false
		}
		for i, r := range p.Rhs {
			if r != child {
				continue
			}
			lhs, ok := unparen(p.Lhs[i]).(*syntax.Ident)
			if !ok {
				return false
			}
			if lhs.Name == "_" {
				return true // a discard keeps nothing
			}
			t, ok := w.info.Uses[lhs].(*Var)
			if !ok || t.pkg || !nonEscaping(t.Type) {
				return false
			}
			// The target must not outlive the value: it is declared in
			// the value's frame or in one nested in it.
			tf, ok1 := w.varFrame[t]
			vf, ok2 := w.varFrame[v]
			return ok1 && ok2 && tf >= vf
		}
	case *syntax.InterpolatedString:
		// A function part has no text form: the text-form rule refuses it
		// (expr.go, textForm), so it keeps nothing, and one diagnostic
		// stands for the one mistake (check-v1 E6a).
		return true
	}
	return false
}

// Captures of let mut locals and of borrows by escaping frames (#197 C2;
// types.md, Function values 3 and Borrows 1; owner q60, 2026-11-09:
// "escaping let mut capture allowed only when no write follows").
//
// An escaping frame (a function literal or block that may outlive the
// call that made it) may capture a `let mut` local of an enclosing frame
// only when no write to that local follows the capture in either frame:
//
//   - the escaping frame, and every frame inside it, writes it nowhere;
//   - outside it, every write comes before it in the source and shares no
//     loop or frame with it that lies inside the local's scope, since such
//     a loop or frame may run the write again after the capture (a for, a
//     for-in, a non-escaping block that its callee may call again).
//
// A write is an assignment rooted at the local, a borrow `&x` of a place
// rooted at it, and the implicit borrow of a dot call whose function takes
// *T. Under the rule a capture by value and Go's capture by reference give
// one result, so the lowering copies each such local when it makes the
// literal (Info.ValueCaptures): the local stays on the stack, where Go's
// capture by reference of a variable that is assigned moves it to the
// heap. The rule is source order, not reachability: a write in the other
// arm of an if after the literal is refused too (the simpler rule; write
// it before the literal).
//
// A borrow (*T) is never captured by an escaping frame: it lives only as
// long as the call that lent it (Borrows 1).
//
// Prior art: Swift's escaping closures capture variables by reference and
// its exclusivity rule checks the accesses at run time; Kotlin refuses to
// write a captured `val` and boxes a captured `var` (Ref.IntRef); Java
// captures only effectively final locals, which is this rule without the
// writes before the capture; Rust's `move` closures copy at creation and
// the borrow checker refuses a later write under a borrowing closure.
// Gleam, Elixir and ReScript have no mutable locals: every capture is a
// value, which the rule gives Ergo for the escaping frames.

const (
	captureWriteText  = "%s is a let mut of an enclosing frame, and this function literal or block may outlive the call that made it, so it holds a copy of %s and cannot write it; declare a let mut inside it, or pass it to a non-escaping parameter (types.md, Function values 3)"
	writeAfterText    = "%s is written here after the function literal or block at %d:%d captured it, and that literal may outlive the call that made it, so it would not see the write; write %s before the literal, or capture a copy that is not written: let %s = %s before it (types.md, Function values 3)"
	borrowCaptureText = "%s is a borrow (*T), and this function literal or block may outlive the call that lent it; an escaping frame never captures a borrow: copy what it needs into a let before it (types.md, Borrows 1)"
)

// mutEvent is a use of a let mut local: whether it writes the local, the
// outermost escaping frame between the local's frame and the use (nil
// when there is none), and the regions around the use.
type mutEvent struct {
	id      *syntax.Ident
	write   bool
	esc     syntax.Node
	regions []syntax.Node
}

// markWrites records the root identifiers that the node n writes, before
// n's children are visited.
func (w *escWalk) markWrites(n syntax.Node) {
	mark := func(x syntax.Expr) {
		if id, kind := w.c.assignRoot(x); kind == rootName && id != nil {
			w.writes[id] = true
		}
	}
	switch n := n.(type) {
	case *syntax.AssignStmt:
		for _, l := range n.Lhs {
			mark(l)
		}
	case *syntax.UnaryExpr:
		if n.Op == syntax.AND {
			mark(n.X)
		}
	case *syntax.CallExpr:
		if recv := implicitBorrow(w.info, n); recv != nil {
			mark(recv)
		}
	}
}

// implicitBorrow is the receiver of a dot call whose function takes *T on
// a receiver that is no borrow already: the call borrows it.
func implicitBorrow(info *Info, call *syntax.CallExpr) syntax.Expr {
	k := info.Calls[call]
	if k == nil || k.Kind != CallFunc || k.Recv == nil || k.Func == nil || k.Func.Sig == nil {
		return nil
	}
	if len(k.Func.Sig.Params) == 0 || !isPointer(k.Func.Sig.Params[0].Type) || isPointer(info.Types[k.Recv]) {
		return nil
	}
	return k.Recv
}

// escapingFrom is the outermost escaping frame between the frame of v's
// declaration and the use, or nil.
func (w *escWalk) escapingFrom(v *Var) syntax.Node {
	from, ok := w.varFrame[v]
	if !ok {
		return nil
	}
	for k := from + 1; k < len(w.frames); k++ {
		if w.frames[k] {
			return w.frameNodes[k]
		}
	}
	return nil
}

// mutUse records a use of the let mut local v.
func (w *escWalk) mutUse(id *syntax.Ident, v *Var) {
	if w.events == nil {
		w.events = map[*Var][]mutEvent{}
	}
	if _, ok := w.events[v]; !ok {
		w.order = append(w.order, v)
	}
	w.events[v] = append(w.events[v], mutEvent{id: id, write: w.writes[id], esc: w.escapingFrom(v), regions: append([]syntax.Node(nil), w.regions...)})
}

// borrowUse checks a use of the borrow v.
func (w *escWalk) borrowUse(id *syntax.Ident, v *Var) {
	if w.escapingFrom(v) != nil {
		w.c.errorf(id, CodeType, borrowCaptureText, id.Name)
	}
}

// finishCaptures applies the rule to the declaration just visited and
// records the value captures.
func (w *escWalk) finishCaptures() {
	for _, v := range w.order {
		evs := w.events[v]
		reported := map[*syntax.Ident]bool{}
		seen := map[syntax.Node]bool{}
		for _, ce := range evs {
			e := ce.esc
			if e == nil || seen[e] {
				continue
			}
			seen[e] = true
			w.info.ValueCaptures[e] = append(w.info.ValueCaptures[e], v)
			at := indexOf(ce.regions, e)
			for _, wr := range evs {
				if !wr.write || reported[wr.id] {
					continue
				}
				switch {
				case indexOf(wr.regions, e) >= 0:
					reported[wr.id] = true
					w.c.errorf(wr.id, CodeType, captureWriteText, v.Name, v.Name)
				case wr.id.Pos() >= e.End() || commonPrefix(ce.regions[:at], wr.regions) > w.declDepth[v]:
					reported[wr.id] = true
					line, col := w.c.cur.syntax.Position(e.Pos())
					w.c.errorf(wr.id, CodeType, writeAfterText, v.Name, line, col, v.Name, renamed(v.Name), v.Name)
				}
			}
		}
	}
	w.events, w.order = nil, nil
}

func indexOf(xs []syntax.Node, n syntax.Node) int {
	for i, x := range xs {
		if x == n {
			return i
		}
	}
	return -1
}

// commonPrefix is the length of the longest common prefix of a and b.
func commonPrefix(a, b []syntax.Node) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// Local frames (#197 C2). A function literal that a `let` binds to one
// immutable local, `let show = func() { … }`, is a non-escaping frame when
// the local only runs in place: each use of it is a position the
// non-escaping rule allows (the callee of a call, an argument of a
// non-escaping parameter), and no frame between the let and the use is
// escaping. Such a literal cannot outlive the frame that declares it, as a
// literal passed to a non-escaping parameter cannot, so it captures as
// that one does: by reference, writes included. Every other literal a let
// binds is an escaping frame.
//
// A frame that holds a use is a literal that starts after the let, since
// the local is in scope only after it, so deciding the literals from the
// last to the first decides each one after every literal it depends on:
// one pass, linear in the uses and the frames around them.
//
// Prior art: Swift's local functions (`func f()` inside a function) may
// capture an inout parameter when they are only called, and escape
// analysis decides it; a closure stored in a local variable is escaping
// there. Kotlin's local functions capture by reference. Go's escape
// analysis keeps a closure that is only called on the stack.

// letLiteral records s when it binds one immutable local to a function
// literal.
func (w *escWalk) letLiteral(s *syntax.VarSpec) {
	if len(s.Names) != 1 || s.Pattern != nil || len(s.Values) != 1 {
		return
	}
	lit, ok := unparen(s.Values[0]).(*syntax.FuncLit)
	if !ok {
		return
	}
	v, ok := w.info.Defs[s.Names[0]].(*Var)
	if !ok || v.pkg || v.mut {
		return
	}
	w.litVar[lit] = v
	w.litUses[v] = []litUse{}
}

// localFrames decides which literals of the declaration d that a let binds
// are local frames, before the walk that applies the rules.
func (w *escWalk) localFrames(d syntax.Node) {
	w.collecting = true
	w.litVar, w.litUses = map[*syntax.FuncLit]*Var{}, map[*Var][]litUse{}
	w.visit(d)
	w.collecting = false
	lits := make([]*syntax.FuncLit, 0, len(w.litVar))
	for lit := range w.litVar {
		lits = append(lits, lit)
	}
	sort.Slice(lits, func(i, j int) bool { return lits[i].Pos() > lits[j].Pos() })
	for _, lit := range lits {
		if w.runsInPlace(w.litVar[lit]) {
			w.info.LocalFrames[lit] = true
		}
	}
}

// runsInPlace reports whether every use of the local v is allowed and has
// no escaping frame between v's frame and it.
func (w *escWalk) runsInPlace(v *Var) bool {
	from := w.varFrame[v]
	for _, u := range w.litUses[v] {
		if !u.allowed {
			return false
		}
		for k := from + 1; k < len(u.frames); k++ {
			esc := u.esc[k]
			if lit, ok := u.frames[k].(*syntax.FuncLit); ok {
				if _, bound := w.litVar[lit]; bound {
					esc = !w.info.LocalFrames[lit]
				}
			}
			if esc {
				return false
			}
		}
	}
	return true
}
