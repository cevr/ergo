package eval

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Compiled forms (ORACLE O34). The tree walk reads, at every evaluation of
// a node, what never changes between evaluations: the node's form, its
// operands' facts, its callee's plan, whether its statement releases
// anything, whether a name is a hand-off. A node evaluated a second time
// in a frame outside a generic body is compiled: a Go closure with all of
// that read once and its operands' forms bound, so a later evaluation runs
// the closure and walks nothing. A form exists only for the plainest
// shapes (a constant, a local, an operator, a field of a local record, a
// call of an Ergo function, an assignment to a local, a let, an if, a for,
// an expression statement, a return); any other node, and any node in a
// frame of a generic body, is walked as before.
//
// A form does exactly what the walk does for its node: the same steps
// charged at the same nodes in the same order, the same lends, the same
// releases. TestPreparedEvalAgrees runs the example packages both
// ways and compares every obligation's outcome and its steps; walkOnly
// turns the forms off.

// fastExpr is tailK of one expression: its step, then its value.
type fastExpr func(in *interp, fr *frame) (Value, *abrupt)

// fastPeek is peekPath of one place with no path wanted: its value and
// its root slot, with no lend.
type fastPeek func(in *interp, fr *frame) (Value, *slot, *abrupt)

// fastStmt runs one statement of a list as stmts does: the statement,
// then its releases (R43).
type fastStmt func(in *interp, fr *frame) *abrupt

// walkOnly turns compiled forms off: every node is walked. The agreement
// test sets it to run the reference.
var walkOnly atomic.Bool

// Form states: a node is compiled at its second evaluation, once its
// first has filled the caches a form reads (a constant's value, a
// selector's record type).
const (
	formUnseen uint8 = iota
	formSeen
	formDone // compiled, or no form exists
)

// escape is exprK's reading of an operand's completion: a typed failure
// in a value position leaves the frame (rows.md, Catch 5).
func escape(ab *abrupt) *abrupt {
	if ab.kind == ctlFail {
		out := *ab
		out.kind = ctlEscape
		return &out
	}
	return ab
}

// visitExpr counts an evaluation of f's node and compiles it at the
// second; it reports whether a form is ready now.
func (in *interp) visitExpr(fr *frame, x syntax.Expr, f *facts) bool {
	switch f.form {
	case formUnseen:
		if !in.walk {
			f.form = formSeen
		}
		return false
	case formSeen:
		f.form = formDone
		f.fast = in.compileExpr(fr, x, f)
	}
	return f.fast != nil
}

// operand is the form of e, with facts k, in a value position of a
// compiled parent: its own form, compiled now if it has one, or a call of
// the walk.
func (in *interp) operand(fr *frame, e syntax.Expr, k *facts) fastExpr {
	if k.plain && k.form != formDone {
		k.form = formDone
		k.fast = in.compileExpr(fr, e, k)
	}
	if g := k.fast; g != nil {
		return g
	}
	return func(in *interp, fr *frame) (Value, *abrupt) {
		return in.tailK(fr, e, k)
	}
}

// formsMade counts the forms compiled, by the node's syntax type, for
// the agreement test's reach check.
var formsMade sync.Map // string -> *atomic.Int64

func madeForm(n syntax.Node) { madeKind(fmt.Sprintf("%T", n)) }

func madeKind(k string) {
	c, _ := formsMade.LoadOrStore(k, new(atomic.Int64))
	c.(*atomic.Int64).Add(1)
}

// compileExpr is the form of x, a plain expression whose facts are f, or
// nil when x has none.
func (in *interp) compileExpr(fr *frame, x syntax.Expr, f *facts) fastExpr {
	g := in.compileExprForm(fr, x, f)
	if g != nil {
		madeForm(x)
	}
	return g
}

func (in *interp) compileExprForm(fr *frame, x syntax.Expr, f *facts) fastExpr {
	if !f.plain || !fr.forms() {
		return nil
	}
	var at syntax.Node = x
	switch f.op {
	case opConst:
		if f.cval == nil {
			v, ok := fromConstant(fr.rt(f.typ).t, f.val)
			if !ok {
				return nil
			}
			f.cval = v
		}
		v := f.cval
		// A leaf costs no step: the node that reads it pays
		// (FU-eval-step-per-op, as tailK).
		return func(in *interp, fr *frame) (Value, *abrupt) {
			return v, nil
		}
	case opIdent:
		id := x.(*syntax.Ident)
		v, ok := f.use.(*check.Var)
		if !ok || fr.info.Handoffs[id] != nil {
			return nil
		}
		var hint int32
		return func(in *interp, fr *frame) (Value, *abrupt) {
			if s := in.slotAt(fr, v, &hint); s != nil {
				s.lend()
				return s.v, nil
			}
			return in.identUse(fr, id, v)
		}
	case opParen:
		p := x.(*syntax.ParenExpr)
		inner := in.operand(fr, p.X, in.kid(fr, f, 0, p.X))
		return func(in *interp, fr *frame) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1); ab != nil {
				return nil, ab
			}
			return inner(in, fr)
		}
	case opBinary:
		return in.compileBinary(fr, x.(*syntax.BinaryExpr), f)
	case opCall:
		return in.compileCall(fr, x.(*syntax.CallExpr), f)
	case opComposite:
		return in.compileRecord(fr, x.(*syntax.CompositeLit))
	case opPlace:
		if sel, ok := x.(*syntax.SelectorExpr); ok {
			if _, taken := fr.info.FieldTakes[sel]; taken {
				return nil
			}
		}
		peek := in.compilePeek(fr, x)
		if peek == nil {
			return nil
		}
		return func(in *interp, fr *frame) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1); ab != nil {
				return nil, ab
			}
			v, root, ab := peek(in, fr)
			if ab != nil {
				return nil, ab
			}
			if root != nil && !holdsNoStore(v) {
				root.lend() // a part that holds a store leaves the slot
			}
			return v, nil
		}
	}
	return nil
}

// compileBinary is the form of x op y (binary); a range a..b is walked.
func (in *interp) compileBinary(fr *frame, x *syntax.BinaryExpr, f *facts) fastExpr {
	if x.Op == syntax.DOTDOT {
		return nil
	}
	var at syntax.Node = x
	l := in.operand(fr, x.X, in.kid(fr, f, 0, x.X))
	r := in.operand(fr, x.Y, in.kid(fr, f, 1, x.Y))
	op := x.Op
	if op == syntax.LAND || op == syntax.LOR {
		or := op == syntax.LOR
		return func(in *interp, fr *frame) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1); ab != nil {
				return nil, ab
			}
			a, ab := l(in, fr)
			if ab != nil {
				return nil, escape(ab)
			}
			if truth(a) == or {
				return a, nil
			}
			b, ab := r(in, fr)
			if ab != nil {
				return nil, escape(ab)
			}
			return b, nil
		}
	}
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		a, ab := l(in, fr)
		if ab != nil {
			return nil, escape(ab)
		}
		b, ab := r(in, fr)
		if ab != nil {
			return nil, escape(ab)
		}
		return in.binop(fr, x, op, a, b)
	}
}

// compileCall is the form of a call of a declared, non-generic Ergo
// function with no receiver, no trailing block and no taking std callee:
// callOf's path through stackArgs and callPlanned.
func (in *interp) compileCall(fr *frame, x *syntax.CallExpr, f *facts) fastExpr {
	c := f.call
	if c == nil || c.Kind != check.CallFunc || c.Site != nil || f.unreachable || x.Trailing != nil {
		return nil
	}
	p := in.planAt(f, c.Func)
	if p.takes || p.collection || p.appendsString {
		return nil
	}
	if !p.ergo {
		return in.compileNativeCall(fr, x, f, c, p)
	}
	if len(c.Func.Sig.TParams) > 0 {
		return nil
	}
	var at syntax.Node = x
	recv := in.receiverForm(fr, c.Recv, c.Func)
	args := make([]fastExpr, len(x.Args))
	for i, a := range x.Args {
		args[i] = in.operand(fr, a, in.kid(fr, f, i, a))
	}
	hands := len(fr.info.PartHands) > 0
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		mark := len(in.argStack)
		if recv != nil {
			v, ab := recv(in, fr)
			if ab != nil {
				return nil, ab
			}
			in.argStack = append(in.argStack, v)
		}
		for _, a := range args {
			v, ab := a(in, fr)
			if ab != nil {
				in.argStack = in.argStack[:mark]
				return nil, escape(ab)
			}
			in.argStack = append(in.argStack, v)
		}
		if hands {
			in.handParts(fr, c.Recv)
			in.handParts(fr, x.Args...)
		}
		n := len(in.argStack)
		v, ab := in.callPlanned(fr, x, p, nil, nil, in.argStack[mark:n:n])
		in.argStack = in.argStack[:mark]
		return v, ab
	}
}

// compileNativeCall is the form of callOf's path through args and
// callPlanned for a function the evaluator's native runs: its arguments
// in a slice of their own (a native may keep it), the receiver first,
// then the native. A generic native reads no type argument; one whose
// type parameter an operation type constrains is walked (call refuses it).
func (in *interp) compileNativeCall(fr *frame, x *syntax.CallExpr, f *facts, c *check.Call, p *funcPlan) fastExpr {
	if p.nat == nil {
		return nil
	}
	if len(c.Func.Sig.TParams) > 0 {
		if inst := fr.info.Instances[x]; inst != nil && len(inst.Witnesses) > 0 {
			return nil
		}
	}
	var at syntax.Node = x
	recv := in.receiverForm(fr, c.Recv, c.Func)
	args := make([]fastExpr, len(x.Args))
	for i, a := range x.Args {
		args[i] = in.operand(fr, a, in.kid(fr, f, i, a))
	}
	n := len(x.Args)
	if recv != nil {
		n++
	}
	hands := len(fr.info.PartHands) > 0
	nat, fn := p.nat, c.Func
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		vals := make([]Value, 0, n)
		if recv != nil {
			v, ab := recv(in, fr)
			if ab != nil {
				return nil, ab
			}
			vals = append(vals, v)
		}
		for _, a := range args {
			v, ab := a(in, fr)
			if ab != nil {
				return nil, escape(ab)
			}
			vals = append(vals, v)
		}
		if hands {
			in.handParts(fr, c.Recv)
			in.handParts(fr, x.Args...)
		}
		return nat(in, fr, x, fn, vals)
	}
}

// receiverForm is the form of receiver(recv, fn): nil for no receiver,
// recv's value in a value position when fn takes it as it is, else the
// walk's borrow or dereference.
func (in *interp) receiverForm(fr *frame, recv syntax.Expr, fn *check.Func) fastExpr {
	if recv == nil {
		return nil
	}
	if len(fn.Sig.Params) > 0 && isPointer(fn.Sig.Params[0].Type) != isPointer(fr.typeOf(recv)) {
		return func(in *interp, fr *frame) (Value, *abrupt) { return in.receiver(fr, recv, fn) }
	}
	g := in.operand(fr, recv, in.fx(fr, recv))
	return func(in *interp, fr *frame) (Value, *abrupt) {
		v, ab := g(in, fr)
		if ab != nil {
			return nil, escape(ab)
		}
		return v, nil
	}
}

// compilePeek is the form of peekPath of x with no path: a local, or a
// field of a record a compiled peek reads, through no borrow; nil for any
// other place.
func (in *interp) compilePeek(fr *frame, x syntax.Expr) fastPeek {
	written := x
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		v, ok := in.fx(fr, x).use.(*check.Var)
		if !ok {
			return nil
		}
		var hint int32
		return func(in *interp, fr *frame) (Value, *slot, *abrupt) {
			if s := in.slotAt(fr, v, &hint); s != nil {
				// The path's root is a leaf the path's node pays for
				// (FU-eval-step-per-op, as tailK).
				return s.v, s, nil
			}
			v, ab := in.expr(fr, written)
			return v, nil, ab
		}
	case *syntax.SelectorExpr:
		xf := in.fx(fr, x)
		if xf.field.t == nil || isPointer(fr.typeOf(x.X)) {
			return nil
		}
		base := in.basePeek(fr, x.X)
		name := x.Sel.Name
		return func(in *interp, fr *frame) (Value, *slot, *abrupt) {
			b, root, ab := base(in, fr)
			if ab != nil {
				return nil, nil, ab
			}
			r, ok := b.(*record)
			if !ok {
				ice("a field of %T", b)
			}
			return r.fields[xf.fieldOf(r.t, name)], root, nil
		}
	case *syntax.IndexExpr:
		if in.fx(fr, x).instance != nil || len(x.Indices) != 1 || isPointer(fr.typeOf(x.X)) {
			return nil
		}
		base := in.basePeek(fr, x.X)
		idx := in.operand(fr, x.Indices[0], in.fx(fr, x.Indices[0]))
		var at syntax.Node = x
		return func(in *interp, fr *frame) (Value, *slot, *abrupt) {
			b, root, ab := base(in, fr)
			if ab != nil {
				return nil, nil, ab
			}
			iv, ab := idx(in, fr)
			if ab != nil {
				return nil, nil, escape(ab)
			}
			switch b := b.(type) {
			case string:
				i, ok := in.index(fr, at, iv, len(b))
				if !ok {
					return nil, nil, fr.defect(at, "%s", indexError(iv, len(b)))
				}
				return b[i], root, nil
			case sliceV:
				i, ok := in.index(fr, at, iv, b.n)
				if !ok {
					return nil, nil, fr.defect(at, "%s", indexError(iv, b.n))
				}
				return b.b.data[b.off+i], root, nil
			case mapV:
				ice("a Map read by index")
			}
			ice("an index of %T", b)
			return nil, nil, nil
		}
	}
	return nil
}

// basePeek is the form of peekPath of x, a place's operand through no
// borrow: its compiled peek, or the walk's.
func (in *interp) basePeek(fr *frame, x syntax.Expr) fastPeek {
	if p := in.compilePeek(fr, x); p != nil {
		return p
	}
	return func(in *interp, fr *frame) (Value, *slot, *abrupt) {
		return in.peekPath(fr, x, nil)
	}
}

// visitStmt counts an evaluation of s, whose facts are sf, in a list and
// compiles it at the second; it reports whether a form is ready now.
func (in *interp) visitStmt(fr *frame, s syntax.Stmt, sf *facts) bool {
	switch sf.sform {
	case formUnseen:
		if !in.walk {
			sf.sform = formSeen
		}
		return false
	case formSeen:
		sf.sform = formDone
		sf.sfast = in.compileStmt(fr, s, sf)
	}
	return sf.sfast != nil
}

// stmtForm is the form of s in a list, compiled now if it has one, or a
// call of the walk.
func (in *interp) stmtForm(fr *frame, s syntax.Stmt) fastStmt {
	sf := in.sx(fr, s)
	if sf.sform != formDone {
		sf.sform = formDone
		sf.sfast = in.compileStmt(fr, s, sf)
	}
	if g := sf.sfast; g != nil {
		return g
	}
	return func(in *interp, fr *frame) *abrupt {
		if ab := in.stmt(fr, s); ab != nil {
			return ab
		}
		return in.releaseFacts(fr, sf)
	}
}

// compileBlock is the form of stmts over list.
func (in *interp) compileBlock(fr *frame, list []syntax.Stmt) fastStmt {
	forms := make([]fastStmt, len(list))
	for i, s := range list {
		forms[i] = in.stmtForm(fr, s)
	}
	return func(in *interp, fr *frame) *abrupt {
		for _, g := range forms {
			if ab := g(in, fr); ab != nil {
				return ab
			}
		}
		return nil
	}
}

// releasesOf is the form of releaseAt at key: nil when the plan releases
// nothing there.
func (in *interp) releasesOf(fr *frame, key syntax.Node) fastStmt {
	if key == nil || len(fr.info.Releases) == 0 {
		return nil
	}
	kf := in.sx(fr, key)
	if len(kf.releases) == 0 {
		return nil
	}
	return func(in *interp, fr *frame) *abrupt { return in.releaseFacts(fr, kf) }
}

// compileStmt is the form of s in a list, with its releases after it, or
// nil when s has none.
func (in *interp) compileStmt(fr *frame, s syntax.Stmt, sf *facts) fastStmt {
	if !fr.forms() {
		return nil
	}
	core := in.compileStmtCore(fr, s)
	if core == nil {
		return nil
	}
	madeForm(s)
	rel := in.releasesOf(fr, s)
	if rel == nil {
		return core
	}
	return func(in *interp, fr *frame) *abrupt {
		if ab := core(in, fr); ab != nil {
			return ab
		}
		return rel(in, fr)
	}
}

// compileStmtCore is the form of stmt(s): its step, then its work.
func (in *interp) compileStmtCore(fr *frame, s syntax.Stmt) fastStmt {
	var at syntax.Node = s
	switch s := s.(type) {
	case *syntax.ExprStmt:
		if _, tail := fr.info.Tails[s]; tail {
			return dropValue(in.compileYield(fr, s, false))
		}
		x := in.operand(fr, s.X, in.fx(fr, s.X))
		return func(in *interp, fr *frame) *abrupt {
			if ab := in.charge(fr, at, 1); ab != nil {
				return ab
			}
			if _, ab := x(in, fr); ab != nil {
				return escape(ab)
			}
			return nil
		}
	case *syntax.AssignStmt:
		return in.compileAssign(fr, s)
	case *syntax.DeclStmt:
		return in.compileLet(fr, s)
	case *syntax.IfStmt:
		return in.compileIf(fr, s)
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt, *syntax.BlockStmt:
		return dropValue(in.compileYield(fr, s, false))
	case *syntax.ForStmt:
		return in.compileFor(fr, s)
	case *syntax.RangeStmt:
		return in.compileRange(fr, s)
	case *syntax.ReturnStmt:
		if len(s.Results) != 1 {
			return nil
		}
		r, pos := in.compileRet(fr, s.Results[0]), s.Pos()
		return func(in *interp, fr *frame) *abrupt {
			if ab := in.charge(fr, at, 1); ab != nil {
				return ab
			}
			v, ab := r(in, fr)
			if ab != nil {
				return ab
			}
			// The frame's own return record, not a new one: a return
			// ends its frame, and every reader of a return (runFrameBody,
			// a case's body) takes its value before the frame is reused.
			fr.ret = abrupt{kind: ctlReturn, val: v, pkg: fr.pkg, decl: fr.decl, at: pos}
			return &fr.ret
		}
	}
	return nil
}

// dropValue is stmt's reading of a yield form: yieldStmt with its value
// dropped; nil for nil.
func dropValue(y fastYield) fastStmt {
	if y == nil {
		return nil
	}
	return func(in *interp, fr *frame) *abrupt {
		_, ab := y(in, fr)
		return ab
	}
}

// compileAssign is the form of `x = e` or `x op= e` for a local x: assign
// with no path and no hand-over.
func (in *interp) compileAssign(fr *frame, s *syntax.AssignStmt) fastStmt {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return nil
	}
	id, ok := unparen(s.Lhs[0]).(*syntax.Ident)
	if !ok || id.Name == "_" {
		return nil
	}
	v, ok := in.fx(fr, id).use.(*check.Var)
	if !ok || fr.info.OwnedVars[v] != nil {
		return nil
	}
	op, isOp := assignOp(s.Tok)
	if !isOp && s.Tok != syntax.ASSIGN {
		return nil
	}
	if _, call := unparen(s.Rhs[0]).(*syntax.CallExpr); call && !isOp {
		return nil // a hand-over (handover) may take the local's store
	}
	var at syntax.Node = s
	rhs := in.operand(fr, s.Rhs[0], in.fx(fr, s.Rhs[0]))
	var hint int32
	return func(in *interp, fr *frame) *abrupt {
		if ab := in.charge(fr, at, 1); ab != nil {
			return ab
		}
		root, local := fr.vars.getAt(v, &hint)
		if !local {
			return in.assign(fr, s) // a package let: assign says why it is not written
		}
		for _, r := range in.ranges {
			if r.root == root {
				return fr.unsupported(id, "replacement of an active slice range's place yet")
			}
		}
		old := root.v
		val, ab := rhs(in, fr)
		if ab != nil {
			return escape(ab)
		}
		if isOp {
			if val, ab = in.binop(fr, s, op, old, val); ab != nil {
				return ab
			}
		}
		root.v = val
		return nil
	}
}

// compileLet is the form of `let x = e` for one local name: letBind's
// last case, with no ownership and the spec's releases after it.
func (in *interp) compileLet(fr *frame, s *syntax.DeclStmt) fastStmt {
	if len(s.Decl.Specs) != 1 {
		return nil
	}
	spec := s.Decl.Specs[0]
	if spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
		return nil
	}
	id := spec.Names[0]
	idf := in.fx(fr, id)
	if p := idf.pattern; p != nil && p.Kind != check.PatBind {
		return nil
	}
	v, ok := idf.def.(*check.Var)
	if !ok || id.Name == "_" || fr.info.OwnedVars[v] != nil {
		return nil
	}
	var at syntax.Node = s
	x := in.operand(fr, spec.Values[0], in.fx(fr, spec.Values[0]))
	rel := in.releasesOf(fr, spec)
	var hint int32
	return func(in *interp, fr *frame) *abrupt {
		if ab := in.charge(fr, at, 1); ab != nil {
			return ab
		}
		val, ab := x(in, fr)
		if ab != nil {
			return escape(ab)
		}
		fr.vars.bindAt(v, val, &hint)
		if rel != nil {
			return rel(in, fr)
		}
		return nil
	}
}

// compileIf is the form of an if in a statement position: yieldStmt with
// its value dropped.
func (in *interp) compileIf(fr *frame, s *syntax.IfStmt) fastStmt {
	var at syntax.Node = s
	cond := in.operand(fr, s.Cond, in.fx(fr, s.Cond))
	condRel := in.releasesOf(fr, s.Cond)
	body := in.compileBlock(fr, s.Body.List)
	bodyRel := in.releasesOf(fr, s.Body)
	var els fastStmt
	switch e := s.Else.(type) {
	case nil:
	case *syntax.BlockStmt:
		list := in.compileBlock(fr, e.List)
		rel := in.releasesOf(fr, e)
		els = func(in *interp, fr *frame) *abrupt {
			if ab := list(in, fr); ab != nil {
				return ab
			}
			if rel != nil {
				return rel(in, fr)
			}
			return nil
		}
	case *syntax.IfStmt:
		els = in.compileIf(fr, e)
	default:
		return nil
	}
	return func(in *interp, fr *frame) *abrupt {
		if ab := in.charge(fr, at, 1); ab != nil {
			return ab
		}
		c, ab := cond(in, fr)
		if ab != nil {
			return escape(ab)
		}
		if condRel != nil {
			if ab := condRel(in, fr); ab != nil {
				return ab
			}
		}
		if truth(c) {
			if ab := body(in, fr); ab != nil {
				return ab
			}
			if bodyRel != nil {
				return bodyRel(in, fr)
			}
			return nil
		}
		if els != nil {
			return els(in, fr)
		}
		return nil
	}
}

// compileFor is the form of `for { }` and `for cond { }` (forStmt).
func (in *interp) compileFor(fr *frame, s *syntax.ForStmt) fastStmt {
	var at syntax.Node = s
	var cond fastExpr
	var condRel fastStmt
	if s.Cond != nil {
		cond = in.operand(fr, s.Cond, in.fx(fr, s.Cond))
		condRel = in.releasesOf(fr, s.Cond)
	}
	body := in.compileBlock(fr, s.Body.List)
	bodyRel := in.releasesOf(fr, s.Body)
	return func(in *interp, fr *frame) *abrupt {
		if ab := in.charge(fr, at, 1); ab != nil {
			return ab
		}
		for {
			if ab := in.charge(fr, at, 1); ab != nil {
				return ab
			}
			if cond != nil {
				c, ab := cond(in, fr)
				if ab != nil {
					return escape(ab)
				}
				if condRel != nil {
					if ab := condRel(in, fr); ab != nil {
						return ab
					}
				}
				if !truth(c) {
					return nil
				}
			}
			ab := body(in, fr)
			if ab == nil && bodyRel != nil {
				ab = bodyRel(in, fr)
			}
			if done, ab := loopBody(ab); done {
				return ab
			}
			if ab := in.backEdge(fr, at); ab != nil {
				return ab
			}
		}
	}
}

// binder is the form of loopBind for p: nil when it binds nothing.
func (in *interp) binder(fr *frame, p syntax.Expr) func(in *interp, fr *frame, v Value) *abrupt {
	if p == nil {
		return nil
	}
	if id, ok := p.(*syntax.Ident); ok {
		if id.Name == "_" {
			return nil
		}
		if w, ok := in.fx(fr, id).def.(*check.Var); ok && fr.info.OwnedVars[w] == nil {
			var hint int32
			return func(in *interp, fr *frame, v Value) *abrupt {
				fr.vars.bindAt(w, v, &hint)
				return nil
			}
		}
	}
	return func(in *interp, fr *frame, v Value) *abrupt { return in.loopBind(fr, p, v) }
}

// compileRange is the form of a for-in over a slice, an array, a string
// or a Range (rangeStmt); a Map or a Seq is walked.
func (in *interp) compileRange(fr *frame, s *syntax.RangeStmt) fastStmt {
	l := fr.info.Loops[s]
	if l == nil || l.Domain != check.LoopSlice && l.Domain != check.LoopString && l.Domain != check.LoopRange {
		return nil
	}
	var at syntax.Node = s
	var one Value
	if l.Domain == check.LoopRange {
		v, ok := convertBasic(int64(1), fr.rt(l.Elem).basic().Kind)
		if !ok {
			return nil
		}
		one = v
	}
	key, val := in.binder(fr, s.Key), in.binder(fr, s.Value)
	body := in.compileBlock(fr, s.Body.List)
	bodyRel := in.releasesOf(fr, s.Body)
	// iter is rangeStmt's iter: one iteration, done when the loop ends.
	iter := func(in *interp, fr *frame, root *slot, pos, elem Value) (bool, *abrupt) {
		if root != nil && !basicValue(elem) {
			root.lend() // the iteration binding holds its own element value
		}
		if ab := in.charge(fr, at, 1); ab != nil {
			return true, ab
		}
		if key != nil {
			if ab := key(in, fr, pos); ab != nil {
				return true, ab
			}
		}
		if val != nil {
			if ab := val(in, fr, elem); ab != nil {
				return true, ab
			}
		}
		ab := body(in, fr)
		if ab == nil && bodyRel != nil {
			ab = bodyRel(in, fr)
		}
		if done, ab := loopBody(ab); done {
			return true, ab
		}
		ab = in.backEdge(fr, at)
		return ab != nil, ab
	}
	return func(in *interp, fr *frame) *abrupt {
		if ab := in.charge(fr, at, 1); ab != nil {
			return ab
		}
		dom, root, path, ab := in.rangeHead(fr, s, l)
		if ab != nil {
			return ab
		}
		switch l.Domain {
		case check.LoopSlice:
			xs := dom.(sliceV)
			if !xs.array {
				in.ranges = append(in.ranges, &sliceRange{root: root, path: path, view: &xs})
				defer func() { in.ranges = in.ranges[:len(in.ranges)-1] }()
			}
			for i := 0; i < xs.n; i++ {
				if done, ab := iter(in, fr, root, int64(i), xs.elems()[i]); done {
					return ab
				}
			}
		case check.LoopString:
			for i, r := range dom.(string) {
				if done, ab := iter(in, fr, root, int64(i), r); done {
					return ab
				}
			}
		case check.LoopRange:
			r := dom.(*record)
			start, end := r.fields[fieldIndex(r.t, "Start")], r.fields[fieldIndex(r.t, "End")]
			for i := start; ; {
				less, _, _ := arith(syntax.LSS, i, end)
				if !less.(bool) {
					return nil
				}
				if done, ab := iter(in, fr, root, nil, i); done {
					return ab
				}
				i, _, _ = arith(syntax.ADD, i, one)
			}
		}
		return nil
	}
}

// compileRecord is the form of a record literal with no base (composite)
// whose values name every field.
func (in *interp) compileRecord(fr *frame, x *syntax.CompositeLit) fastExpr {
	if x.Base != nil {
		return nil
	}
	t := fr.rtypeOf(x)
	if check.RefinementOf(t.t) != nil {
		return nil
	}
	switch t.under().t.(type) {
	case *check.Slice, *check.Array, *check.Map, *check.MachineType:
		return nil
	}
	n, ok := t.t.(*check.Named)
	if !ok || structOf(n) == nil || check.ScriptOf(n) != nil || check.IsGoError(n) {
		return nil
	}
	st := structOf(n)
	nf := len(st.Fields)
	key := argsKey(n, t.env)
	idx := make([]int, len(x.Elts))
	vals := make([]fastExpr, len(x.Elts))
	set := make([]bool, nf)
	for i, e := range x.Elts {
		j, v := i, e
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			id, ok := kv.Key.(*syntax.Ident)
			if !ok {
				return nil
			}
			j, v = fieldIndex(n, id.Name), kv.Value
		}
		if j >= nf {
			return nil
		}
		idx[i], set[j] = j, true
		vals[i] = in.operand(fr, v, in.fx(fr, v))
	}
	if slices.Contains(set, false) {
		return nil // a field left out takes its default: walked
	}
	var at syntax.Node = x
	hands := len(fr.info.PartHands) > 0
	return func(in *interp, fr *frame) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		out := newRecord(n, key, nf)
		if ab := in.charge(fr, at, int64(nf)); ab != nil {
			return nil, ab
		}
		for i, g := range vals {
			v, ab := g(in, fr)
			if ab != nil {
				return nil, escape(ab)
			}
			out.fields[idx[i]] = v
		}
		if hands {
			in.handParts(fr, x.Elts...)
		}
		return out, nil
	}
}

// fastBody is bodyValue of one function's body.
type fastBody func(in *interp, fr *frame) (Value, *abrupt)

// compileBody is the form of yield over a function body: its statements
// but the last as compiled forms, then the last, which yields the result,
// and its releases (compile_yield.go).
func (in *interp) compileBody(fr *frame, list []syntax.Stmt) fastBody {
	if len(list) == 0 || !fr.forms() {
		return nil
	}
	if fr.env != nil {
		madeKind("instance body") // the agreement test's reach check
	}
	return fastBody(in.compileList(fr, list, true))
}
