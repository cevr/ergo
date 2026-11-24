package lower

// IR builder: statements, blocks, if and loops. See ir.go.

import (
	"fmt"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// reserve makes v a known local in a census whose let of v did not build,
// so its uses do not report it again; outside a census it does nothing (the
// body stops).
func (b *irBuilder) reserve(v *check.Var) {
	if b.st.census == nil {
		return
	}
	if _, known := b.locals[v]; !known {
		b.locals[v] = b.next
		b.next++
	}
}

func (b *irBuilder) local(v *check.Var, id *syntax.Ident) int {
	b.ownable(v) // ir_own.go
	return b.bindLocal(v, id)
}

// bindLocal declares a Go local without registering ownership. A
// parameter's owner frame registers it after binding all its parameters.
func (b *irBuilder) bindLocal(v *check.Var, id *syntax.Ident) int {
	if _, dup := b.locals[v]; dup {
		irNo("local declared twice")
	}
	i := b.named(v.Name, b.goType(v.Type, id), b.pos(id, id.Pos()))
	b.locals[v] = i
	return i
}

// temp declares a temporary local of the Go type typ (an if expression's
// result, a try's pair, a spilled operand), named after name but unique in
// the function (tempNames): two temporaries never share a name, so the Go
// text view (TXT) and DWARF tell them apart.
func (b *irBuilder) temp(name, typ string, p irPos) int {
	return b.declare(b.names.alloc(name), typ, p)
}

// named declares the local of a source variable spelled name (a let, an
// `as` binding): it keeps its spelling unless that is taken (localName).
func (b *irBuilder) named(name, typ string, p irPos) int {
	return b.declare(b.localName(name), typ, p)
}

// localName is the Go name of a source local spelled name, the emitter's
// discipline (names.go): its spelling when Go's scope of the function (its
// parameters, the package, Go's reserved and predeclared names) and every
// local and temporary of the body so far leave it free, else the
// temporaries' namer's name_N. A local spelled len or string never shadows
// Go's, and two locals of one spelling in two scopes are told apart.
func (b *irBuilder) localName(name string) string {
	if !b.fc.scope.taken(name) && !b.given[name] {
		return name
	}
	return b.names.alloc(name)
}

// declare appends the local name of the Go type typ to the body.
func (b *irBuilder) declare(name, typ string, p irPos) int {
	i := b.next
	b.next++
	b.fn.Locals = append(b.fn.Locals, irLocal{Name: name, Type: typ, Pos: p})
	b.setLocalType(i, typ) // ir_rewrite_pass.go (I-FLIP: LF6w's setter, renamed beside RED17's localType)
	if b.given == nil {
		b.given = map[string]bool{}
	}
	b.given[name] = true
	return i
}

// tail is a statement list whose last statement yields the function's
// result when ret is true.
func (b *irBuilder) tail(list []syntax.Stmt, ret bool) []*irStmt {
	return b.tailTo(list, ret, nil)
}

// isBranchStmt reports a block, an if or a match: a statement whose
// branches' tails are the tail of the list that ends with it.
func isBranchStmt(s syntax.Stmt) bool {
	switch s.(type) {
	case *syntax.BlockStmt, *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return true
	}
	return false
}

// tailTo is tail; with to set, the last statement yields its value into
// to (an if expression's branch) instead of returning it.
func (b *irBuilder) tailTo(list []syntax.Stmt, ret bool, to *yieldTo) []*irStmt {
	var out []*irStmt
	skip := 0
	voidTail := b.voidTail && !ret && to == nil
	defer func(saved bool) { b.voidTail = saved }(b.voidTail)
	for i, s := range list {
		last := i == len(list)-1
		if skip > 0 {
			skip--
			continue
		}
		// A run of comparisons of one value, as one switch (chainswitch.go).
		if n := b.chainSwitch(&out, list, i, ret || to != nil); n > 0 {
			skip = n - 1
			continue
		}
		b.planFold(list, i) // ir_collectfold.go: a collect only folded next
		b.fillPrev(list, i) // ir_fill.go
		// Only the tail's own branches stay in the tail (failureTail).
		b.voidTail = voidTail && last && isBranchStmt(s)
		b.part(func() {
			exit := b.releasesAtExit(s, last && ret && to == nil) // ir_own.go
			var ss []*irStmt
			if es, ok := s.(*syntax.ExprStmt); ok && last && voidTail && b.isFailure(es.X) {
				// A branch tail of an error value in a body with no
				// value fails the frame (the emitter's ifYield).
				out = append(out, b.ret(es.X, b.pos(es.X, es.X.Pos()))...) // ir_fail.go
				return
			}
			if last && (ret || to != nil) {
				ss = b.yield(s, to)
			} else {
				ss = b.stmt(s)
			}
			if !exit {
				ss = b.scoped(s, ss, b.pos(s, s.Pos())) // ir_own.go: the statement's releases (R43)
			}
			out = append(out, ss...)
		})
		b.fc.killWritten(s) // bounds.go: the statement's writes end the facts over them (ir_bounds.go)
	}
	if (ret || to != nil) && len(list) == 0 {
		irNo("empty body with a result")
	}
	return out
}

// yieldTo is where a branch's last statement puts its value: a local.
type yieldTo struct {
	local int
	t     check.Type // the local's type: a variant joins its union (ir_union.go)
	// fails marks a fallible with's block under try or catch (ir_with.go,
	// failWith): a tail that is a fallible call puts its error in err, and
	// local is -1 when the with has no value.
	fails bool
	err   int
	// fuse marks a fused callee's results (ir_forms_match.go): each is a
	// jump to the arm its variant reaches.
	fuse bool
}

// yield is s in result position: its value is returned, or put in to.
func (b *irBuilder) yield(s syntax.Stmt, to *yieldTo) []*irStmt {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		if call := b.unreachableCall(s.X); call != nil {
			return b.unreachableStmt(call) // ir_defect.go: a defect leaves the branch
		}
		p := b.pos(s.X, s.X.Pos())
		_, conv := b.l.info.Implicit[s.X]
		if m, ok := unparen(s.X).(syntax.Stmt); ok && !conv {
			switch m.(type) {
			case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
				if to != nil && to.fails {
					irNo("with whose block fails in a match")
				}
				return b.matchStmt(m, true, to) // ir_match.go: each arm yields
			}
		}
		if to != nil && to.fuse {
			if _, ok := b.isWith(s.X); ok && !conv {
				irNo("with in a fused body")
			}
			return b.fuseTail(s.X, p) // ir_forms_match.go
		}
		if w, ok := b.isWith(s.X); ok && !conv {
			return b.withStmt(w, true, to) // ir_with.go: the block's tail yields
		}
		if to != nil && to.fails {
			return b.failYield(s.X, to, p) // ir_with.go
		}
		if to != nil {
			pre, x := b.value(s.X)
			x = b.assignedTo(s.X, x, to.t) // ir_union.go
			return append(pre, &irStmt{K: "set", Pos: p, Local: to.local, X: x})
		}
		return b.ret(s.X, p) // ir_fail.go
	case *syntax.ReturnStmt, *syntax.BranchStmt:
		return b.stmt(s) // it leaves: no value to yield (a catch arm's continue)
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		if to != nil && to.fails {
			irNo("with whose block fails in a match")
		}
		return b.matchStmt(s, true, to) // ir_match.go
	case *syntax.IfStmt:
		if s.Else == nil {
			if to != nil {
				irNo("if without else in result position")
			}
			// The branch returns, or the function's end panics (bodyEnd).
			return b.ifStmts(s, true, nil)
		}
		return b.ifStmts(s, true, to)
	case *syntax.ForStmt, *syntax.RangeStmt:
		if to != nil {
			irNo("result statement %T", s)
		}
		// A loop as the last statement: the function's end panics after
		// it (bodyEnd), as the emitter's does.
		return b.stmt(s)
	}
	irNo("result statement %T", s)
	return nil
}

// ifWith is s with its condition cond, built (ifStmts, ir_order.go); ret
// says its branches yield, into to or as the result.
func (b *irBuilder) ifWith(s *syntax.IfStmt, cond *irExpr, ret bool, to *yieldTo) *irStmt {
	st := &irStmt{K: "if", Pos: b.pos(s, s.Pos()), X: cond, End: b.pos(s.Body, s.Body.Rbrace)}
	mark := b.fc.factMark()
	// bounds.go: the facts the condition gives its body (ir_bounds.go).
	b.fc.assume(s.Cond)
	st.Then = b.scoped(s.Body, b.tailTo(s.Body.List, ret && to == nil, to), b.pos(s.Body, s.Body.Rbrace)) // ir_own.go
	b.fc.factReset(mark)
	switch e := s.Else.(type) {
	case nil:
	case *syntax.BlockStmt:
		st.ElsePos, st.ElseEnd = b.pos(e, e.Lbrace), b.pos(e, e.Rbrace)
		st.Else = b.scoped(e, b.tailTo(e.List, ret && to == nil, to), b.pos(e, e.Rbrace)) // ir_own.go
	case *syntax.IfStmt:
		// The else block of an else-if wraps the chain to its last brace.
		last := syntax.Stmt(e)
		for {
			n, ok := last.(*syntax.IfStmt)
			if !ok || n.Else == nil {
				break
			}
			last = n.Else
		}
		end := e.Body.Rbrace
		switch l := last.(type) {
		case *syntax.BlockStmt:
			end = l.Rbrace
		case *syntax.IfStmt:
			end = l.Body.Rbrace
		}
		if to != nil && e.Else == nil {
			irNo("if without else in result position")
		}
		st.ElsePos, st.ElseEnd = b.pos(e, e.Pos()), b.pos(e, end)
		st.Else = b.ifStmts(e, ret, to) // its condition's statements inside the else block
	default:
		irNo("else %T", e)
	}
	return st
}

// ifValue is the if expression s in a value position: a local of its type,
// declared first, and an if whose branches yield into it.
func (b *irBuilder) ifValue(s *syntax.IfStmt) ([]*irStmt, *irExpr) {
	if s.Else == nil {
		irNo("if without else in result position")
	}
	t, ok := b.l.info.Types[s]
	if !ok {
		irNo("expression without a type")
	}
	p := b.pos(s, s.Pos())
	i := b.temp(fmt.Sprintf("if%d", b.next), b.goType(t, s), p)
	pre := append([]*irStmt{{K: "var", Pos: p, Local: i}}, b.ifStmts(s, true, &yieldTo{local: i, t: t})...)
	return pre, &irExpr{K: "local", Pos: p, I: i}
}

// dropsPlain reports an IR value with no effect and no defect: a local, a
// constant, a zero value, a package variable, a function, or the address
// of a local. A dropped call whose value is one of these keeps only its
// statements.
func dropsPlain(e *irExpr) bool {
	switch e.K {
	case "local", "const", "zero", "global", "func":
		return true
	case "addr":
		return e.X != nil && e.X.K == "local"
	}
	return false
}

// discard is x evaluated for its effects and dropped (the emitter's
// discard): a call is a call statement, any other value is `_ = x`.
func (b *irBuilder) discard(x syntax.Expr) []*irStmt {
	x = unparen(x)
	p := b.pos(x, x.Pos())
	if w, ok := b.isWith(x); ok {
		return b.withStmt(w, false, nil) // ir_with.go
	}
	if call := b.unreachableCall(x); call != nil {
		return b.unreachableStmt(call) // ir_defect.go (FLIP OPC-unreach)
	}
	if call, ok := x.(*syntax.CallExpr); ok {
		if c := b.l.info.Calls[call]; c != nil && c.Kind != check.CallConversion && c.Kind != check.CallLen {
			b.dropping = call // its value is dropped (ir_forms_drop.go)
			pre, e := b.call(call, true)
			b.dropping = nil
			if e == nil {
				// A function value's call, bound and dropped (ir_closure.go),
				// or a tried call with no value (call).
				return pre
			}
			if dropsPlain(e) {
				// A call whose IR value is no call (go.New's &n, go.Zero's
				// zero, a tabulated read's local): its statements ran, and
				// the value has no effect, so it is dropped, as Go has no
				// expression statement of it (the decoder's rule).
				return pre
			}
			switch e.K {
			case "call", "mcall", "pcall", "vcall", "builtin":
			default:
				irNo("dropped value of the IR kind %s", e.K)
			}
			return append(pre, &irStmt{K: "expr", Pos: p, X: e})
		}
	}
	switch y := x.(type) {
	case *syntax.TryExpr, *syntax.CatchExpr:
		return b.failStmt(x) // ir_fail.go
	case *syntax.SwitchStmt:
		return b.matchStmt(y, false, nil) // ir_match.go
	case *syntax.TypeSwitchStmt:
		return b.matchStmt(y, false, nil) // ir_match.go
	case *syntax.FuncLit:
		irNo("expression statement %T", x)
	}
	pre, e := b.value(x)
	return append(pre, &irStmt{K: "discard", Pos: p, X: e})
}

func (b *irBuilder) stmt(s syntax.Stmt) []*irStmt {
	switch s := s.(type) {
	case *syntax.AssertStmt:
		return b.assert(s) // ir_assert.go
	case *syntax.SkipStmt:
		return b.skip(s) // ir_assert.go
	case *syntax.DeclStmt:
		var out []*irStmt
		for _, spec := range s.Decl.Specs {
			if spec.Pattern == nil && len(spec.Names) == 1 && len(spec.Values) == 0 {
				out = append(out, b.letDefault(s.Decl, spec)...) // ir_letpat.go
				continue
			}
			if spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
				if tl, ok := b.letTuple(spec, b.pos(s.Decl, s.Decl.Pos())); ok {
					out = append(out, tl...) // ir_tuple.go
					continue
				}
				irNo("let pattern")
			}
			id := spec.Names[0]
			if p := b.l.info.Patterns[id]; p != nil && p.Kind != check.PatBind || id.Name == "_" {
				// A discard, or a known name the checker proved
				// irrefutable: the value is evaluated and dropped.
				out = append(out, b.discard(spec.Values[0])...)
				continue
			}
			if w, ok := b.l.info.Defs[id].(*check.Context); ok && w.Local {
				// A local context is a recipe: its let lowers to nothing,
				// and each with that names it publishes its providers
				// (ir_with.go, as body.go's letStmt).
				continue
			}
			v, ok := b.l.info.Defs[id].(*check.Var)
			if !ok {
				irNo("let without a variable")
			}
			b.recordLet(v, spec.Values[0]) // ir_unfold.go
			if sl, ok := b.strLet(v, spec.Values[0], b.pos(s.Decl, s.Decl.Pos())); ok {
				out = append(out, sl...) // strbuf.go
				continue
			}
			if sl, ok := b.scalarLet(v, spec.Values[0], s.Decl.Mut.IsValid(), b.pos(s.Decl, s.Decl.Pos())); ok {
				out = append(out, sl...) // ir_scalar.go
				continue
			}
			var pre []*irStmt
			var x *irExpr
			b.part(func() { pre, x = b.value(spec.Values[0]) })
			out = append(out, pre...)
			i := -1
			b.part(func() { i = b.local(v, id) })
			if i < 0 {
				b.reserve(v)
			}
			def := &irStmt{K: "def", Pos: b.pos(s.Decl, s.Decl.Pos()), Local: i, X: x}
			b.fillLet(v, spec.Values[0], def) // ir_fill.go
			out = append(out, def)
			if i >= 0 {
				b.fc.assumeLet(v, spec.Values[0]) // bounds.go (ir_bounds.go)
			}
			b.part(func() {
				out = append(append(out, b.ownLet(v, i, id, spec.Values[0])...), b.releaseAt(spec, b.pos(id, id.Pos()))...)
			}) // ir_own.go: the let's temporaries (R43)
		}
		return out
	case *syntax.AssignStmt:
		if out, ok := b.fillStore(s); ok { // ir_fill.go
			return out
		}
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
			irNo("assignment form several")
		}
		op := ""
		if s.Tok != syntax.ASSIGN {
			text := s.Tok.String()
			op = text[:len(text)-1]
			if !irOps()[op] {
				irNo("assignment form %s", text)
			}
		}
		if id, ok := s.Lhs[0].(*syntax.Ident); ok && id.Name == "_" && op == "" {
			return b.discard(s.Rhs[0])
		}
		id, ok := s.Lhs[0].(*syntax.Ident)
		if !ok {
			return b.store(s, op) // ir_place.go
		}
		v, ok := b.l.info.Uses[id].(*check.Var)
		if ptr, held := b.held[v]; held && ok {
			// The held local of an in-place literal is *p (ir_ownlit.go):
			// s = v and s op= v store into the cell's value, which Go
			// reads whole before the store, as the emitter's `*p = v`.
			if op != "" {
				b.operator(op, b.l.info.Types[s.Lhs[0]], s)
			}
			p := b.pos(s, s.Pos())
			pre, x := b.value(s.Rhs[0])
			to := &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: ptr}}
			return append(pre, &irStmt{K: "store", Pos: p, X: to, Y: x, Op: op})
		}
		if sc := b.scalars[v]; sc != nil && ok {
			if op != "" {
				irNo("op-assignment of a scalar-form local")
			}
			pre, x := b.value(s.Rhs[0])
			return append(pre, b.scatter(sc, x, s, b.pos(s, s.Pos()))...) // ir_scalar.go
		}
		if sb, built := b.strBufs[v]; built && ok {
			return b.strAssign(s, v, sb) // strbuf.go
		}
		i, known := b.locals[v]
		if !ok || !known {
			irNo("assignment to a non-local")
		}
		if op != "" {
			b.operator(op, b.l.info.Types[s.Lhs[0]], s)
		}
		pre, x := b.value(s.Rhs[0])
		if op != "" && len(pre) > 0 {
			// The emitter reads the target before a value that writes
			// statements (assign's old): x = old op v.
			t := b.l.info.Types[s.Lhs[0]]
			p := b.pos(s, s.Pos())
			old := b.temp("old", b.goType(t, s), p)
			out := append([]*irStmt{{K: "def", Pos: p, Local: old, X: &irExpr{K: "local", Pos: p, I: i}}}, pre...)
			// A float product rounds: T(old * v) (ir_float.go).
			return append(out, &irStmt{K: "set", Pos: p, Local: i, X: b.rounded(s, nil, t, &irExpr{K: "bin", Pos: p, Op: op, X: &irExpr{K: "local", Pos: p, I: old}, Y: x})})
		}
		if t := b.l.info.Types[s.Lhs[0]]; op == "*" && roundsFloat(t) {
			// x *= v of floats is x = T(x * v): the product rounds, as
			// the emitter's assign writes it (ir_float.go).
			p := b.pos(s, s.Pos())
			return append(pre, &irStmt{K: "set", Pos: p, Local: i, X: b.rounded(s, nil, t, &irExpr{K: "bin", Pos: p, Op: op, X: &irExpr{K: "local", Pos: p, I: i}, Y: x})})
		}
		return append(pre, &irStmt{K: "set", Pos: b.pos(s, s.Pos()), Local: i, X: x, Op: op})
	case *syntax.ReturnStmt:
		p := b.pos(s, s.Pos())
		if b.fuse != nil && len(s.Results) == 1 {
			return b.fuseTail(s.Results[0], p) // ir_forms_match.go: a fused callee's return
		}
		if len(s.Results) == 0 {
			return b.ret(nil, p) // ir_fail.go
		}
		if len(s.Results) != 1 {
			irNo("several results")
		}
		return b.ret(s.Results[0], p) // ir_fail.go
	case *syntax.ExprStmt:
		return b.discard(s.X)
	case *syntax.IfStmt:
		return b.ifStmts(s, false, nil) // ir_order.go
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return b.matchStmt(s, false, nil) // ir_match.go
	case *syntax.ForStmt:
		// bounds.go: at the loop's head (ir_bounds.go).
		b.fc.killWritten(s.Body)
		return b.memoLoop(b.next, func() []*irStmt { return b.whileStmt(s) }) // ir_order.go, ir_memo.go
	case *syntax.BranchStmt:
		if b.loops == 0 {
			irNo("break or continue outside a loop")
		}
		switch s.Tok {
		case syntax.BREAK:
			st := &irStmt{K: "break", Pos: b.pos(s, s.Pos())}
			if b.switches > 0 {
				// Go's unlabeled break would leave the switch (ir_match.go):
				// it names the loop, as the emitter's branch does.
				if b.label == "" {
					b.labels++
					b.label = "loop" + strconv.Itoa(b.labels)
				}
				st.Label = b.label
			}
			return append(b.releaseAt(s, st.Pos), st) // ir_own.go: what the iterations' scopes own (R6)
		case syntax.CONTINUE:
			return append(b.releaseAt(s, b.pos(s, s.Pos())), b.continueStmt(s)...) // ir_fail.go: the back-edge poll first
		}
		irNo("statement *syntax.BranchStmt %s", s.Tok)
	case *syntax.RangeStmt:
		b.fc.killWritten(s.Body) // bounds.go: at the loop's head (ir_bounds.go)
		lp := b.l.info.Loops[s]
		if lp != nil && lp.Domain == check.LoopRange {
			return b.memoLoop(b.next, func() []*irStmt { return b.countLoop(s, lp) }) // ir_pkg.go, ir_memo.go
		}
		seq := b.seqLoop(s)                           // ir_seq.go
		if lp == nil || !goRange(lp.Domain) && !seq { // ir_pkg.go
			b.gate("for-in over a non-slice")
		}
		if _, ok := b.intMapOf(b.l.info.Types[s.X]); ok && lp.Domain == check.LoopMap {
			irNo("for-in over an int map") // ir_intmap.go: its emitter body ranges over Go's map
		}
		pre, x := b.foldRangeValue(s) // ir_collectfold.go: rangeValue (ir_order.go), run once before the loop, unless fused
		if seq {
			var bind []*irStmt
			bind, x = b.seqRange(s, lp, x) // ir_seq.go
			pre = append(pre, bind...)
		}
		first := b.next // before the key and value: ir_memo.go
		st := &irStmt{K: "range", Pos: b.pos(s, s.Pos()), X: x, End: b.pos(s.Body, s.Body.Rbrace)}
		bind := func(e syntax.Expr) *int {
			if !bound(e) {
				return nil
			}
			id, ok := e.(*syntax.Ident)
			if !ok {
				irNo("for-in pattern")
			}
			v, ok := b.l.info.Defs[id].(*check.Var)
			if !ok {
				irNo("for-in binding without a variable")
			}
			i := -1
			b.part(func() { i = b.local(v, id) })
			if i < 0 {
				b.reserve(v)
				return nil
			}
			return &i
		}
		b.part(func() { st.Key = bind(s.Key) })
		b.part(func() { st.Val = bind(s.Value) })
		if seq {
			// A range over a func has one variable, the element: the
			// range's first (ir_seq.go).
			st.Key, st.Val = st.Val, nil
		}
		loop := b.memoLoop(first, func() []*irStmt { // ir_memo.go
			// The nest's counter goes before the loop, after the
			// pre-header's memoized calls (ir_poll.go).
			ctr := b.loopBody(st, s, s.Body)
			return append(ctr, st)
		})
		st.Pos = past(st.Pos, pre) // ir_match.go
		return append(pre, loop...)
	}
	irNo("statement %T", s)
	return nil
}

// loopBody gives st, loop s, its body, where break and continue stand,
// then its back-edge poll in a polling frame (loopPoll, ir_fail.go), and
// the label a break inside a switch names. A quiet loop polls amortized
// (ir_poll.go); the outermost loop of a quiet nest returns its counter's
// definition, which the caller puts before the loop.
func (b *irBuilder) loopBody(st *irStmt, s syntax.Stmt, body *syntax.BlockStmt) []*irStmt {
	quiet := pollAmortLowering == "amortized" && b.loopPolls() && b.quietLoop(body) // ir_poll.go
	var own *pollAmort
	outer := b.amort
	if quiet && b.amort == nil {
		own = &pollAmort{ctr: -1, pos: st.Pos}
		b.amort = own
	}
	b.loops++
	b.pollSites = append(b.pollSites, nil)
	switches, label := b.switches, b.label
	b.switches, b.label = 0, ""
	defer func() {
		b.loops, b.switches, b.label = b.loops-1, switches, label
		b.pollSites = b.pollSites[:len(b.pollSites)-1]
		b.amort = outer
	}()
	st.Body = b.scoped(body, b.tail(body.List, false), b.pos(body, body.Rbrace)) // ir_own.go: the iteration's scope (R6)
	poll := b.loopPoll(s, body)
	// The back-edge poll follows a body that reaches its end: after a
	// break or a terminating statement it never runs, and go vet calls it
	// unreachable (ir_end.go).
	if n := len(st.Body); n == 0 || st.Body[n-1].K != "break" && !irTerminates(st.Body[n-1]) {
		st.Body = append(st.Body, poll...)
	}
	st.Label = b.label
	if quiet {
		b.amortize(b.pollSites[len(b.pollSites)-1])
	}
	if own != nil && own.ctr >= 0 {
		return []*irStmt{{K: "def", Pos: st.Pos, Local: own.ctr, X: pollEveryConst(st.Pos)}}
	}
	return nil
}

// bodyEnd is what follows the IR body of a function with a result whose
// last statement does not terminate by Go's rule (a loop that breaks, an
// if without else; ir_end.go): the emitter's panic, which a checked
// program never reaches (fc.body). After a terminating statement it
// writes nothing: go vet calls a statement there unreachable.
func (b *irBuilder) bodyEnd(body []*irStmt, end irPos) []*irStmt {
	if b.result == nil || len(body) == 0 || irListTerminates(body) {
		return nil
	}
	msg := &irExpr{K: "const", Pos: end, T: "string", V: "ergo: unreachable: the function's end"}
	return []*irStmt{{K: "panic", Pos: end, X: msg}}
}
