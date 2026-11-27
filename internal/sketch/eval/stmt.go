package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func (in *interp) stmts(fr *frame, list []syntax.Stmt) *abrupt {
	for _, s := range list {
		sf := in.sx(fr, s)
		if fr.forms() {
			if sf.sfast != nil || sf.sform != formDone && in.visitStmt(fr, s, sf) {
				if ab := sf.sfast(in, fr); ab != nil { // compile.go
					return ab
				}
				continue
			}
		}
		if ab := in.stmt(fr, s); ab != nil {
			return ab
		}
		if ab := in.releaseFacts(fr, sf); ab != nil { // the statement's temporaries (R43)
			return ab
		}
	}
	return nil
}

// stmt runs one statement for its effect on the frame.
func (in *interp) stmt(fr *frame, s syntax.Stmt) *abrupt {
	switch s.(type) {
	case *syntax.ExprStmt, *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt, *syntax.BlockStmt:
		_, ab := in.yieldStmt(fr, s, false)
		return ab
	}
	if ab := in.charge(fr, s, 1); ab != nil {
		return ab
	}
	return in.stmtOnly(fr, s)
}

// stmtOnly runs a statement that yields no value.
func (in *interp) stmtOnly(fr *frame, s syntax.Stmt) *abrupt {
	switch s := s.(type) {
	case *syntax.DeclStmt:
		return in.let(fr, s.Decl)
	case *syntax.AssignStmt:
		return in.assign(fr, s)
	case *syntax.ReturnStmt:
		if len(s.Results) == 0 {
			return fr.abrupt(ctlReturn, s, nil, "")
		}
		if len(s.Results) > 1 {
			ice("a return of %d values", len(s.Results))
		}
		v, ab := in.retValue(fr, s.Results[0])
		if ab != nil {
			return ab
		}
		return fr.abrupt(ctlReturn, s, v, "")
	case *syntax.DieStmt:
		return in.dieStmt(fr, s)
	case *syntax.BranchStmt:
		// What the iterations' scopes still own ends here (R6).
		if ab := in.releaseAt(fr, s); ab != nil {
			return ab
		}
		if s.Tok == syntax.CONTINUE {
			return fr.abrupt(ctlContinue, s, nil, "")
		}
		return fr.abrupt(ctlBreak, s, nil, "")
	case *syntax.ForStmt:
		return in.forStmt(fr, s)
	case *syntax.RangeStmt:
		return in.rangeStmt(fr, s)
	case *syntax.SkipStmt:
		// A skip ends the obligation as skipped, with the test's own
		// reason and the statement as its site, as an obligation that
		// reaches a form check-time evaluation does not run ends
		// (runtime.md, Tests 4): never passed, never refuted.
		why, _ := s.Text()
		return fr.abrupt(ctlUnsupported, s, nil, why)
	case *syntax.AssertStmt:
		return in.assertStmt(fr, s)
	case *syntax.AlwaysStmt:
		return in.alwaysStmt(fr, s)
	case *syntax.SometimesStmt:
		return in.sometimesStmt(fr, s)
	}
	ice("a statement of the kind %T", s)
	return nil
}

// forStmt runs `for { }` and `for cond { }`. Every iteration costs steps
// through its body and condition, so a loop that never ends exhausts the
// budget.
func (in *interp) forStmt(fr *frame, s *syntax.ForStmt) *abrupt {
	for {
		if ab := in.charge(fr, s, 1); ab != nil {
			return ab
		}
		if s.Cond != nil {
			c, ab := in.expr(fr, s.Cond)
			if ab == nil {
				ab = in.releaseAt(fr, s.Cond)
			}
			if ab != nil {
				return ab
			}
			if !truth(c) {
				return nil
			}
		}
		if done, ab := loopBody(in.iteration(fr, s.Body)); done {
			return ab
		}
		if ab := in.backEdge(fr, s); ab != nil {
			return ab
		}
	}
}

// iteration runs one iteration of a loop's body, a scope of its own: what
// it owns at its normal end is released then (R6).
func (in *interp) iteration(fr *frame, body *syntax.BlockStmt) *abrupt {
	ab := in.stmts(fr, body.List)
	if ab == nil {
		ab = in.releaseAt(fr, body)
	}
	return ab
}

// loopBody reads one iteration's completion: done when the loop ends
// (a break: no abrupt; any other abrupt but continue passes out).
func loopBody(ab *abrupt) (bool, *abrupt) {
	if ab == nil {
		return false, nil
	}
	switch ab.kind {
	case ctlBreak:
		return true, nil
	case ctlContinue:
		return false, nil
	}
	return true, ab
}

// rangeStmt runs a for-in over the domain the checker recorded (types.md,
// Loops and len 1, 7): a slice's initial header with live element writes,
// an array's initial value, a string's runes as Go decodes them, or a Range counting
// up from Start, End excluded. A Map's order is unspecified: maporder.go
// runs it in each of two orders. A Seq calls a function value.
func (in *interp) rangeStmt(fr *frame, s *syntax.RangeStmt) *abrupt {
	l := fr.info.Loops[s]
	if l == nil {
		ice("a for-in with no Loops entry")
	}
	dom, root, path, ab := in.rangeHead(fr, s, l)
	if ab != nil {
		return ab
	}
	if _, isTerm := dom.(term); isTerm && in.sym != nil {
		return in.symStop(fr, s.X, dom)
	}
	if l.Domain == check.LoopMap {
		return in.rangeMap(fr, s, dom.(mapV), root, path) // maporder.go
	}
	if l.Domain == check.LoopSeq {
		// A Seq a native made: a queue's Values, each step a Pop, ending
		// when the queue is closed and empty (runtime.md, Queues 6); the
		// lines of a process's standard input (process.go).
		q, ok := dom.(seqSource)
		if !ok {
			return in.seqLoop(fr, s, dom) // seq.go
		}
		if d, ok := q.(seqDone); ok {
			defer d.done()
		}
		for {
			v, more, ab := q.next(in, fr, s)
			if ab != nil {
				return ab
			}
			if !more {
				return nil
			}
			if ab := in.charge(fr, s, 1); ab != nil {
				return ab
			}
			if ab := in.loopBind(fr, s.Value, v); ab != nil {
				return ab
			}
			if done, ab := loopBody(in.iteration(fr, s.Body)); done {
				return ab
			}
			if ab := in.backEdge(fr, s); ab != nil {
				return ab
			}
		}
	}
	iter := func(pos, elem Value) (bool, *abrupt) {
		if root != nil && !basicValue(elem) {
			root.lend() // the iteration binding holds its own element value
		}
		if ab := in.charge(fr, s, 1); ab != nil {
			return true, ab
		}
		if ab := in.loopBind(fr, s.Key, pos); ab != nil {
			return true, ab
		}
		if ab := in.loopBind(fr, s.Value, elem); ab != nil {
			return true, ab
		}
		if done, ab := loopBody(in.iteration(fr, s.Body)); done {
			return true, ab
		}
		ab := in.backEdge(fr, s)
		return ab != nil, ab
	}
	switch l.Domain {
	case check.LoopSlice:
		xs := dom.(sliceV)
		if !xs.array {
			// A loop is not a value binding (Bindings 3). Follow writes
			// through the ranged place even when COW relocates its store.
			in.ranges = append(in.ranges, &sliceRange{root: root, path: path, view: &xs})
			defer func() { in.ranges = in.ranges[:len(in.ranges)-1] }()
		}
		for i := 0; i < xs.n; i++ {
			if done, ab := iter(int64(i), xs.elems()[i]); done {
				return ab
			}
		}
		return nil
	case check.LoopString:
		for i, r := range dom.(string) {
			if done, ab := iter(int64(i), r); done {
				return ab
			}
		}
		return nil
	case check.LoopRange:
		r := dom.(*record)
		start, end := r.fields[fieldIndex(r.t, "Start")], r.fields[fieldIndex(r.t, "End")]
		one, ok := convertBasic(int64(1), fr.rt(l.Elem).basic().Kind)
		if !ok {
			ice("a Range over %s", l.Elem)
		}
		for i := start; ; {
			less, _, _ := arith(syntax.LSS, i, end)
			if !less.(bool) {
				return nil
			}
			if done, ab := iter(nil, i); done {
				return ab
			}
			i, _, _ = arith(syntax.ADD, i, one)
		}
	}
	ice("a for-in over the domain %d", l.Domain)
	return nil
}

// rangeHead evaluates a for-in's domain: a slice or a Map read in place,
// with its root slot (lent) and path, or any other domain's value; then
// the header's temporaries are released.
func (in *interp) rangeHead(fr *frame, s *syntax.RangeStmt, l *check.Loop) (dom Value, root *slot, path []step, ab *abrupt) {
	if l.Domain == check.LoopSlice || l.Domain == check.LoopMap {
		dom, root, ab = in.peekPath(fr, s.X, &path)
		if root != nil {
			root.lend()
		}
	} else {
		dom, ab = in.expr(fr, s.X)
	}
	if ab == nil {
		ab = in.releaseAt(fr, s.X) // the header's temporaries
	}
	return dom, root, path, ab
}

// loopBind binds a for binding: a name, or an irrefutable pattern the
// checker proved (Info.Patterns). No binding, or `_`, binds nothing.
func (in *interp) loopBind(fr *frame, p syntax.Expr, v Value) *abrupt {
	if p == nil {
		return nil
	}
	if id, ok := p.(*syntax.Ident); ok {
		if id.Name == "_" {
			return nil
		}
		if w, ok := in.fx(fr, id).def.(*check.Var); ok {
			fr.bind(w, v)
			in.ownVar(fr, w, p)
			return nil
		}
	}
	pat := in.fx(fr, p).pattern
	if pat == nil {
		ice("a for binding with neither a Defs nor a Patterns entry")
	}
	ok, ab := in.match(fr, p, pat, v)
	if ab != nil {
		return ab
	}
	if !ok {
		return fr.defect(p, "the for pattern does not match")
	}
	in.ownPattern(fr, pat, p)
	return nil
}

// let binds a local let.
func (in *interp) let(fr *frame, d *syntax.VarDecl) *abrupt {
	for _, spec := range d.Specs {
		if ab := in.letSpec(fr, spec); ab != nil {
			return ab
		}
	}
	return nil
}

// letSpec binds one spec of a let, registers each owned name in the step
// that receives it (R8), and releases the spec's temporaries (R43).
func (in *interp) letSpec(fr *frame, spec *syntax.VarSpec) *abrupt {
	ab := in.letBind(fr, spec)
	if ab == nil {
		ab = in.releaseAt(fr, spec)
	}
	return ab
}

func (in *interp) letBind(fr *frame, spec *syntax.VarSpec) *abrupt {
	switch {
	case spec.Pattern != nil:
		if len(spec.Values) != 1 {
			ice("a let pattern with %d values", len(spec.Values))
		}
		p := in.fx(fr, spec.Pattern).pattern
		if p == nil {
			ice("a let pattern with no Patterns entry")
		}
		v, ab := in.expr(fr, spec.Values[0])
		if ab != nil {
			return ab
		}
		ok, ab := in.match(fr, spec.Pattern, p, v)
		if ab != nil {
			return ab
		}
		if !ok {
			return fr.defect(spec.Pattern, "the let pattern does not match")
		}
		in.ownPattern(fr, p, spec)
		return nil
	case len(spec.Names) > 1 && len(spec.Values) == 1:
		v, ab := in.expr(fr, spec.Values[0])
		if ab != nil {
			return ab
		}
		tup, ok := v.(tuple)
		if !ok || len(tup.elems) != len(spec.Names) {
			ice("a let of %d names over %T", len(spec.Names), v)
		}
		for i, id := range spec.Names {
			if w, ok := in.fx(fr, id).def.(*check.Var); ok {
				fr.bind(w, tup.elems[i])
				in.ownVar(fr, w, id)
			}
		}
		return nil
	case len(spec.Names) != 1 || len(spec.Values) > 1:
		if len(spec.Values) != len(spec.Names) {
			ice("a let of %d names and %d values", len(spec.Names), len(spec.Values))
		}
		vals := make([]Value, len(spec.Values))
		for i, x := range spec.Values {
			v, ab := in.expr(fr, x)
			if ab != nil {
				return ab
			}
			vals[i] = v
		}
		for i, id := range spec.Names {
			if w, ok := in.fx(fr, id).def.(*check.Var); ok {
				fr.bind(w, vals[i])
				in.ownVar(fr, w, id)
			}
		}
		return nil
	}
	id := spec.Names[0]
	if p := in.fx(fr, id).pattern; p != nil && p.Kind != check.PatBind {
		// A known single name tests a type or constant the checker proved
		// irrefutable; the value runs for its effect.
		_, ab := in.expr(fr, spec.Values[0])
		return ab
	}
	if c, ok := in.fx(fr, id).def.(*check.Context); ok && c.Local {
		// A local context is a recipe with no value: each with that names
		// it builds its providers in place (rows.md, Context 9; effects.go).
		return nil
	}
	if id.Name == "_" {
		if len(spec.Values) == 1 {
			_, ab := in.expr(fr, spec.Values[0])
			return ab
		}
		return nil
	}
	v, ok := in.fx(fr, id).def.(*check.Var)
	if !ok {
		ice("the let %s has no Defs entry", id.Name)
	}
	if len(spec.Values) == 0 {
		z, ab := in.zero(fr, spec, fr.rt(v.Type))
		if ab != nil {
			return ab
		}
		fr.bind(v, z)
		in.ownVar(fr, v, id)
		return nil
	}
	val, ab := in.expr(fr, spec.Values[0])
	if ab != nil {
		return ab
	}
	var valOrig []SourceByteOrigin
	if in.track != nil {
		valOrig = in.track.take()
	}
	fr.bind(v, val)
	in.ownVar(fr, v, id)
	if in.track != nil {
		in.track.bindParam(fr, v, valOrig)
	}
	return nil
}

// assignOp is the operator of an op-assignment, false for any other
// token. A switch, where the map it replaces hashed per assignment (a
// tenth of a counting loop's instructions; ORACLE O34).
func assignOp(t syntax.Token) (syntax.Token, bool) {
	switch t {
	case syntax.ADD_ASSIGN:
		return syntax.ADD, true
	case syntax.SUB_ASSIGN:
		return syntax.SUB, true
	case syntax.MUL_ASSIGN:
		return syntax.MUL, true
	case syntax.QUO_ASSIGN:
		return syntax.QUO, true
	case syntax.REM_ASSIGN:
		return syntax.REM, true
	case syntax.AND_ASSIGN:
		return syntax.AND, true
	case syntax.OR_ASSIGN:
		return syntax.OR, true
	case syntax.XOR_ASSIGN:
		return syntax.XOR, true
	case syntax.SHL_ASSIGN:
		return syntax.SHL, true
	case syntax.SHR_ASSIGN:
		return syntax.SHR, true
	case syntax.AND_NOT_ASSIGN:
		return syntax.AND_NOT, true
	}
	return 0, false
}

// step is one step of a written place's path: a field, or an index (an
// element of a slice or an array, a key of a Map).
type step struct {
	at    syntax.Expr
	field string
	index Value
}

// assign runs `x = e` and `x op= e` on a place: a local, or a path of
// fields and indexes rooted at one. As Go's two-phase assignment does, the
// place's index operands are evaluated, left to right, before the value.
func (in *interp) assign(fr *frame, s *syntax.AssignStmt) *abrupt {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		ice("an assignment of several values")
	}
	lhs := unparen(s.Lhs[0])
	if id, ok := lhs.(*syntax.Ident); ok && id.Name == "_" {
		_, ab := in.expr(fr, s.Rhs[0])
		return ab
	}
	mark := len(in.stepStack)
	root, why, ab := in.placePush(fr, lhs)
	if ab == nil && why == "" {
		ab = in.assignAt(fr, s, lhs, root, in.stepStack[mark:])
	}
	in.popSteps(mark)
	if ab != nil {
		return ab
	}
	if why != "" {
		return fr.unsupported(lhs, "an assignment to %s yet", why)
	}
	return nil
}

// assignAt is assign once its place is resolved: root and the path steps
// from it.
func (in *interp) assignAt(fr *frame, s *syntax.AssignStmt, lhs syntax.Expr, root *slot, steps []step) *abrupt {
	var ab *abrupt
	for _, r := range in.ranges {
		// A write of the ranged place or of a place on its path replaces
		// the ranged slice.
		if r.root == root && len(steps) <= len(r.path) && samePath(steps, r.path[:len(steps)]) {
			return fr.unsupported(lhs, "replacement of an active slice range's place yet")
		}
	}
	var old Value
	if s.Tok != syntax.ASSIGN {
		v, ab := in.read(fr, root.v, steps)
		if ab != nil {
			return ab
		}
		old = v
	}
	var val Value
	if call, own := in.handover(fr, root, steps, s); call != nil {
		val, ab = in.collectionCall(fr, call, fr.info.Calls[call], own, steps)
	} else if call := in.appenderHandover(fr, root, steps, s); call != nil {
		val, ab = in.appenderCall(fr, call, root, steps)
	} else {
		val, ab = in.expr(fr, s.Rhs[0])
	}
	if ab != nil {
		return ab
	}
	var rhsOrig []SourceByteOrigin
	if in.track != nil {
		rhsOrig = in.track.take()
	}
	if s.Tok != syntax.ASSIGN {
		op, ok := assignOp(s.Tok)
		if !ok {
			ice("the assignment %s", s.Tok)
		}
		val, ab = in.binop(fr, s, op, old, val)
		if ab != nil {
			return ab
		}
		if in.track != nil && s.Tok == syntax.ADD_ASSIGN && len(steps) == 0 {
			if _, ok := val.(string); ok {
				in.track.slots[root] = append(append([]SourceByteOrigin(nil), in.track.slots[root]...), rhsOrig...)
			}
		}
	} else if id, ok := lhs.(*syntax.Ident); ok && len(steps) == 0 {
		if _, ab := in.reassignOwned(fr, id, val); ab != nil {
			return ab
		}
	}
	nv, ab := in.storePath(fr, root, root.v, steps, 0, val)
	if ab != nil {
		return ab
	}
	root.v = nv
	if in.track != nil && s.Tok == syntax.ASSIGN && len(steps) == 0 {
		in.track.slots[root] = rhsOrig
	}
	return nil
}

// handover is the prelude call of x = x.Append(e), x = x.Put(k, v) or
// x = x.Delete(k) whose receiver is the assigned place itself, and the
// place's root slot: the assignment replaces the place, so the call may
// take its own store. The place is the local x, or a field path from it
// (x.f.g, or s.f through the borrow s), written the same on both sides:
// a field path evaluates nothing, so reading it as a place is reading it
// as a value (MX-EVWITNESS: vt's grow appended a row to s.cells one cell
// at a time, and each Append copied the whole screen, where Go's append
// extends the slice in place).
func (in *interp) handover(fr *frame, root *slot, steps []step, s *syntax.AssignStmt) (*syntax.CallExpr, *slot) {
	if s.Tok != syntax.ASSIGN {
		return nil, nil
	}
	call, ok := unparen(s.Rhs[0]).(*syntax.CallExpr)
	if !ok {
		return nil, nil
	}
	c := fr.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Recv == nil {
		return nil, nil
	}
	switch valueOpOf(c.Func) {
	case valOpAppend, valOpDelete, valOpPut:
	default:
		return nil, nil
	}
	if _, conv := fr.info.Implicit[c.Recv]; conv {
		return nil, nil
	}
	if len(steps) > 0 {
		if !samePlace(s.Lhs[0], c.Recv) {
			return nil, nil
		}
		return call, root
	}
	id, ok := unparen(c.Recv).(*syntax.Ident)
	if !ok {
		return nil, nil
	}
	v, ok := fr.info.Uses[id].(*check.Var)
	if s, _ := fr.vars.get(v); !ok || s != root {
		return nil, nil
	}
	return call, root
}

// appenderHandover is the call of x = f(x, …) whose callee is a pure
// extern that takes a slice first and gives a slice of the same type (an
// appender: Go's append, std strings' Builder writes, json's appends),
// and whose first argument is the assigned place itself, written as in
// handover. The assignment replaces the place, and a pure extern keeps
// nothing of its arguments, so the argument is read without lending its
// slot (MX-EVSLOT: lzpack's d.out.WriteByte(c), through
// b.buf = builderAppendByte(b.buf, c), lent the Decoder's slot, and the
// next d.win[i] = c copied the whole window, where Go copies nothing).
func (in *interp) appenderHandover(fr *frame, root *slot, steps []step, s *syntax.AssignStmt) *syntax.CallExpr {
	if s.Tok != syntax.ASSIGN {
		return nil
	}
	call, ok := unparen(s.Rhs[0]).(*syntax.CallExpr)
	if !ok || call.Trailing != nil || len(call.Args) == 0 {
		return nil
	}
	c := fr.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Recv != nil || c.Site != nil {
		return nil
	}
	f := c.Func
	if f.Decl == nil || f.Decl.Extern == nil || f.Sig == nil || len(f.Sig.Params) == 0 || len(f.Sig.Results) != 1 || len(f.Sig.Takes) > 0 {
		return nil
	}
	if !f.Sig.Uses.Empty() || !f.Sig.Errors.Empty() {
		return nil
	}
	if _, ok := f.Sig.Params[0].Type.(*check.Slice); !ok || f.Sig.Params[0].Type.String() != f.Sig.Results[0].String() {
		return nil
	}
	if _, conv := fr.info.Implicit[call.Args[0]]; conv || !samePlace(s.Lhs[0], call.Args[0]) {
		return nil
	}
	if len(steps) == 0 {
		id, ok := unparen(call.Args[0]).(*syntax.Ident)
		if !ok {
			return nil
		}
		v, ok := fr.info.Uses[id].(*check.Var)
		if s, _ := fr.vars.get(v); !ok || s != root {
			return nil
		}
	}
	return call
}

// appenderCall runs the appender call of appenderHandover: its first
// argument is the place at steps from root, read without a lend; the rest
// are evaluated left to right, as args does.
func (in *interp) appenderCall(fr *frame, x *syntax.CallExpr, root *slot, steps []step) (Value, *abrupt) {
	c := fr.info.Calls[x]
	if ab := in.charge(fr, x.Args[0], 1); ab != nil {
		return nil, ab
	}
	first, ab := in.read(fr, root.v, steps)
	if ab != nil {
		return nil, ab
	}
	args := []Value{first}
	for _, a := range x.Args[1:] {
		v, ab := in.expr(fr, a)
		if ab != nil {
			return nil, ab
		}
		args = append(args, v)
	}
	in.handParts(fr, x.Args[1:]...)
	return in.call(fr, x, c.Func, c.TypeArgs, args)
}

// samePlace reports two field paths written the same: the same name,
// then the same fields (x.f.g). Each side's root resolves to one slot,
// since both name the same variable in one scope.
func samePlace(a, b syntax.Expr) bool {
	switch a := unparen(a).(type) {
	case *syntax.Ident:
		bi, ok := unparen(b).(*syntax.Ident)
		return ok && a.Name == bi.Name
	case *syntax.SelectorExpr:
		bs, ok := unparen(b).(*syntax.SelectorExpr)
		return ok && a.Sel.Name == bs.Sel.Name && samePlace(a.X, bs.X)
	}
	return false
}

// place resolves an assigned place: its root local's slot and the path
// from it, each index evaluated. why is what the place is when the
// evaluator does not write it.
func (in *interp) place(fr *frame, x syntax.Expr) (*slot, []step, string, *abrupt) {
	mark := len(in.stepStack)
	root, why, ab := in.placePush(fr, x)
	var steps []step
	if ab == nil && why == "" && len(in.stepStack) > mark {
		steps = append([]step(nil), in.stepStack[mark:]...)
	}
	in.popSteps(mark)
	if ab != nil || why != "" {
		return nil, nil, why, ab
	}
	return root, steps, "", nil
}

// placePush is place with the path pushed on the task's step stack
// (ORACLE O34): an assignment reads it there until the write is done and
// pops it (popSteps), so a write through a field or an index makes no
// path slice. A nested place, in an index operand, pushes above it and
// pops back before this one pushes again.
func (in *interp) placePush(fr *frame, x syntax.Expr) (*slot, string, *abrupt) {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		v, ok := in.fx(fr, x).use.(*check.Var)
		if !ok {
			ice("an assignment to %s, which is no variable", x.Name)
		}
		s, local := fr.vars.get(v)
		if !local {
			return nil, "a package let", nil
		}
		return s, "", nil
	case *syntax.SelectorExpr:
		if in.sym != nil && !in.sym.tmpl && isPointer(fr.typeOf(x.X)) {
			// A field of a provider's cell (symbolic.go).
			v, ab := in.expr(fr, x.X)
			if ab != nil {
				return nil, "", ab
			}
			c, isCell := v.(*cell)
			if !isCell {
				return nil, "a pointer that is no provider cell", nil
			}
			in.stepStack = append(in.stepStack, step{at: x, field: x.Sel.Name})
			return c.s, "", nil
		}
		var root *slot
		var why string
		var ab *abrupt
		if isPointer(fr.typeOf(x.X)) {
			root, ab = in.placeThrough(fr, x.X)
		} else {
			root, why, ab = in.placePush(fr, x.X)
		}
		if ab != nil || why != "" {
			return nil, why, ab
		}
		in.stepStack = append(in.stepStack, step{at: x, field: x.Sel.Name})
		return root, "", nil
	case *syntax.IndexExpr:
		var root *slot
		var why string
		var ab *abrupt
		if isPointer(fr.typeOf(x.X)) {
			root, ab = in.placeThrough(fr, x.X)
		} else {
			root, why, ab = in.placePush(fr, x.X)
		}
		if ab != nil || why != "" {
			return nil, why, ab
		}
		iv, ab := in.expr(fr, x.Indices[0])
		if ab != nil {
			return nil, "", ab
		}
		in.stepStack = append(in.stepStack, step{at: x, index: iv})
		return root, "", nil
	case *syntax.StarExpr:
		root, ab := in.placeThrough(fr, x.X)
		return root, "", ab
	}
	return nil, "this place", nil
}

// popSteps pops the step stack back to mark, clearing what it held.
func (in *interp) popSteps(mark int) {
	clear(in.stepStack[mark:])
	in.stepStack = in.stepStack[:mark]
}

// read is the value at the path from v, for an op-assignment.
func (in *interp) read(fr *frame, v Value, steps []step) (Value, *abrupt) {
	for _, st := range steps {
		if ab := in.charge(fr, st.at, 1); ab != nil {
			return nil, ab
		}
		switch c := v.(type) {
		case *record:
			v = c.fields[fieldIndex(c.t, st.field)]
		case sliceV:
			i, ok := in.index(fr, st.at, st.index, c.n)
			if !ok {
				return nil, fr.defect(st.at, "%s", indexError(st.index, c.n))
			}
			v = c.b.data[c.off+i]
		case mapV:
			ice("an op-assignment reads a Map entry")
		default:
			ice("a path through %T", v)
		}
	}
	return v, nil
}

// storePath is cur with val written at the path: a new record at each
// field, and each slice, array or Map on the path owned by tok, copied
// once when another value may share it. cur is the value at full[:k], and
// the steps left are full[k:].
func (in *interp) storePath(fr *frame, root *slot, cur Value, full []step, k int, val Value) (Value, *abrupt) {
	steps := full[k:]
	if len(steps) == 0 {
		// The value itself replaces the slot's: no store on the path to
		// own, so no token is made (ORACLE O34). A token made here owned
		// nothing, and a later write through a path makes it then.
		return val, nil
	}
	tok := root.own()
	st := steps[0]
	switch c := cur.(type) {
	case *record:
		i := fieldIndex(c.t, st.field)
		if ab := in.charge(fr, st.at, int64(len(c.fields))); ab != nil {
			return nil, ab
		}
		inner, ab := in.storePath(fr, root, c.fields[i], full, k+1, val)
		if ab != nil {
			return nil, ab
		}
		next := newRecord(c.t, c.key, len(c.fields))
		copy(next.fields, c.fields)
		next.fields[i] = inner
		return next, nil
	case sliceV:
		i, ok := in.index(fr, st.at, st.index, c.n)
		if !ok {
			return nil, fr.defect(st.at, "%s", indexError(st.index, c.n))
		}
		old := c.b
		oldOff := c.off
		c, ab := in.ownSlice(fr, st.at, c, tok)
		if ab != nil {
			return nil, ab
		}
		for _, r := range in.ranges {
			if r.root == root && r.view.b == old && samePath(full[:k], r.path) {
				r.view.b, r.view.off = c.b, r.view.off-oldOff
			}
		}
		inner, ab := in.storePath(fr, root, c.b.data[c.off+i], full, k+1, val)
		if ab != nil {
			return nil, ab
		}
		c.b.data[c.off+i] = inner
		return c, nil
	case mapV:
		if len(steps) != 1 {
			ice("a write through a Map entry")
		}
		return in.putIn(fr, st.at, c, st.index, val, tok)
	}
	ice("a store into %T", cur)
	return nil, nil
}

// fieldIndex is the index of the field name of the record type t.
func fieldIndex(t *check.Named, name string) int {
	s := structOf(t)
	if s == nil {
		ice("%s is no record", t)
	}
	for i, f := range s.Fields {
		if f.Name == name {
			return i
		}
	}
	ice("%s has no field %s", t, name)
	return -1
}

// valueMatch runs a match on a literal subject: the first case whose
// constant equals the subject, else its `_` or default case (Go's switch;
// lower/body.go valueMatch).
func (in *interp) valueMatch(fr *frame, s *syntax.SwitchStmt, ret bool) (Value, *abrupt) {
	if in.sx(fr, s).match == nil {
		ice("a value match with no Matches entry")
	}
	tag, ab := in.expr(fr, s.Tag)
	if ab == nil {
		ab = in.releaseAt(fr, s.Tag)
	}
	if ab != nil {
		return nil, ab
	}
	var def *syntax.CaseClause
	for _, cl := range s.Clauses {
		if len(cl.List) == 0 {
			def = cl
		}
		for _, item := range cl.List {
			p := in.fx(fr, item).pattern
			if p == nil {
				ice("a value case with no Patterns entry")
			}
			switch p.Kind {
			case check.PatWildcard:
				def = cl
			case check.PatConst:
				c, ok := fromConstant(fr.rt(p.Type).t, p.Value)
				if !ok {
					return nil, fr.unsupported(item, "a case constant of %s yet", p.Type)
				}
				if ab := in.charge(fr, item, 1); ab != nil {
					return nil, ab
				}
				eq, ab := in.equal(fr, item, tag, c)
				if ab != nil {
					return nil, ab
				}
				if eq {
					return in.arm(fr, cl, ret)
				}
			default:
				ice("a value case of pattern kind %d", p.Kind)
			}
		}
	}
	if def == nil {
		return nil, fr.defect(s, "no case of the match took the value")
	}
	return in.arm(fr, def, ret)
}

// arm runs the selected clause of a match: its ownership step, then its
// body as a scope.
func (in *interp) arm(fr *frame, cl *syntax.CaseClause, ret bool) (Value, *abrupt) {
	in.patternOwners(fr, cl)
	return in.scope(fr, cl, cl.Body, ret)
}

// typeMatch runs any other match: the first clause one of whose patterns
// matches the subjects, its bindings bound; a default clause last.
func (in *interp) typeMatch(fr *frame, s *syntax.TypeSwitchStmt, ret bool) (Value, *abrupt) {
	if in.sx(fr, s).match == nil {
		ice("a match with no Matches entry")
	}
	var one Value // the one subject, with no slice made for it
	var subjects []Value
	if len(s.Subjects) != 1 {
		subjects = make([]Value, len(s.Subjects))
	}
	for i, g := range s.Subjects {
		v, ab := in.expr(fr, g.X)
		if ab == nil {
			ab = in.releaseAt(fr, g.X)
		}
		if ab == nil {
			ab = in.releaseAt(fr, g)
		}
		if ab != nil {
			return nil, ab
		}
		if subjects == nil {
			one = v
		} else {
			subjects[i] = v
		}
	}
	subject := one
	if subjects != nil {
		subject = tuple{elems: subjects}
	}
	v, ab := in.typeArms(fr, s, subject, ret)
	if ab == nil {
		ab = in.releaseAt(fr, s)
	}
	return v, ab
}

// typeArms runs the clause of s that takes subject: the one subject's
// value, or the tuple of them all.
func (in *interp) typeArms(fr *frame, s *syntax.TypeSwitchStmt, subject Value, ret bool) (Value, *abrupt) {
	var def *syntax.CaseClause
	for _, cl := range s.Clauses {
		if len(cl.List) == 0 {
			def = cl
			continue
		}
		for _, item := range cl.List {
			p := in.fx(fr, item).pattern
			if p == nil {
				ice("a case with no Patterns entry")
			}
			if ab := in.charge(fr, item, 1); ab != nil {
				return nil, ab
			}
			ok, ab := in.match(fr, item, p, subject)
			if ab != nil {
				return nil, ab
			}
			if ok {
				return in.arm(fr, cl, ret)
			}
		}
	}
	if def == nil {
		return nil, fr.defect(s, "no case of the match took the value")
	}
	return in.arm(fr, def, ret)
}

// match tests v against the checked pattern p, read in the frame's
// substitution, and binds its names.
func (in *interp) match(fr *frame, at syntax.Node, p *check.Pattern, v Value) (bool, *abrupt) {
	if in.sym != nil && in.sym.tmpl && p.Kind != check.PatWildcard && p.Kind != check.PatBind && p.Kind != check.PatAs && (isTerm(v) || p.Kind == check.PatConst && symbolic(v)) {
		return in.symMatch(fr, at, p, v) // foldvalue.go
	}
	switch p.Kind {
	case check.PatWildcard:
		return true, nil
	case check.PatBind:
		fr.bind(p.Var, v)
		return true, nil
	case check.PatTypeTest:
		return hasType(v, fr.rt(p.Type)), nil
	case check.PatRecord:
		if !hasType(v, fr.rt(p.Type)) {
			return false, nil
		}
		r := v.(*record)
		for _, f := range p.Fields {
			ok, ab := in.match(fr, at, f.Pattern, r.fields[fieldIndex(r.t, f.Name)])
			if !ok || ab != nil {
				return false, ab
			}
		}
		return true, nil
	case check.PatConst:
		c, ok := fromConstant(fr.rt(p.Type).t, p.Value)
		if !ok {
			ice("a constant pattern of %s", p.Type)
		}
		return in.equal(fr, at, v, c)
	case check.PatTuple:
		t, ok := v.(tuple)
		if !ok || len(t.elems) != len(p.Elems) {
			ice("a tuple pattern over %T", v)
		}
		for i, e := range p.Elems {
			ok, ab := in.match(fr, at, e, t.elems[i])
			if !ok || ab != nil {
				return false, ab
			}
		}
		return true, nil
	case check.PatAs:
		if len(p.Elems) != 1 {
			return false, nil
		}
		ok, ab := in.match(fr, at, p.Elems[0], v)
		if !ok || ab != nil {
			return false, ab
		}
		fr.bind(p.Var, v)
		return true, nil
	}
	ice("a pattern of kind %d", p.Kind)
	return false, nil
}

// hasType reports whether v's dynamic type is t: a record of t, or of a
// variant of the union t. A value that is no record has its static type.
func hasType(v Value, t rtype) bool {
	r, ok := v.(*record)
	if !ok {
		return true
	}
	t = t.env.resolve(t.t)
	switch tt := t.t.(type) {
	case *check.Union:
		for _, variant := range tt.Variants {
			if hasType(v, rtype{variant, t.env}) {
				return true
			}
		}
		return false
	case *check.Named:
		if u, isUnion := tt.Underlying().(*check.Union); isUnion {
			return hasType(v, rtype{u, t.env})
		}
		return r.t.Obj == tt.Obj && r.key == argsKey(tt, t.env)
	}
	return false
}

// equal is a == b, its work charged before it is done.
func (in *interp) equal(fr *frame, at syntax.Node, a, b Value) (bool, *abrupt) {
	var ab *abrupt
	eq, paid := equal(a, b, func(n int64) bool {
		ab = in.charge(fr, at, n)
		return ab == nil
	})
	if !paid {
		return false, ab
	}
	return eq, nil
}
