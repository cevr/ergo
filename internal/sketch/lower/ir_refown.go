package lower

// IR builder: the Ref's owned site (V6, refown.go) on the IR path
// (IR-OWN). A function literal written at an Update or Modify call takes
// the held value over: the call is the runtime body's owned form
// (RefUpdateOwned, RefModifyOwned), with the call's operands, the copy
// function of the held type T and the fresh flag, as the emitter's
// ownedRefCall writes it. Which call is an owned site, its form and its
// fresh flag are the shared analysis's facts (valueFacts.refSites,
// decideSites); the IR only writes them. The literal is the IR's
// function literal (closureLit), adapted to the form's callback
// (adaptOperand, ir_adapt.go); the copy function is an IR literal
//
//	func(v T) T { return copy(v) }
//
// whose copy is the value rule's (copyAs, ir_copy.go: cloneValue's, which
// copies a Map whose entries are never written shallow, V8). Where the emitter
// takes the in-place form (RefModifyInPlace, inPlaceOf), the IR takes it
// too (IR-OWN-MODIFY): the literal takes the held value's address and
// writes it through the pointer, with no copy of T in or out
// (inPlaceLitIR, inPlaceCallback).

import (
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// ownedRefCall is the call x, the owned site s: list is its operands, the
// receiver first for an operation call; recv is the receiver's type for an
// operation call, else nil.
func (b *irBuilder) ownedRefCall(x *syntax.CallExpr, c *check.Call, s *refSite, list []syntax.Expr, recv *check.Named, p irPos) ([]*irStmt, *irExpr, bool) {
	name, cb := s.form.name, s.form.cb
	body, err := runtimeBody(name)
	switch {
	case err != nil || len(body.Params) != len(list)+2 || cb >= len(list) || body.Params[cb].Func == nil:
		irNo("owned site without its runtime body")
	case b.env < 0:
		irNo("owned site in a pure function")
	case s.lit == nil || unparen(list[cb]) != syntax.Expr(s.lit):
		irNo("owned site whose callback is no literal")
	case x.Trailing != nil:
		irNo("owned site with a trailing block")
	case goFallible(body) || failsHere(c):
		irNo("fallible owned site")
	case len(b.fc.captureOwners(x)) > 0:
		irNo("call that captures owners")
	}
	// The in-place form where the emitter takes it (inPlaceOf): the
	// literal writes the held value through a pointer (inPlaceLitIR).
	ip, inName, inBody := b.fc.inPlaceOf(s)
	if ip != nil {
		name, body = inName, inBody
	}
	if _, bound := runtimeBindings()[name]; !bound {
		irNo("std call with no runtime binding")
	}
	// The form's type arguments: a function's are the call's (stdTypeArgs);
	// an operation's are its receiver's, one per type parameter of the form.
	var targs string
	var subst map[string]string
	if recv == nil {
		targs, subst = b.stdTypeArgs(body, c, x)
	} else {
		args := typeArgs(recv)
		if len(args) != len(body.TypeParams) {
			irNo("owned operation whose form infers a type argument")
		}
		subst = map[string]string{}
		for i, gp := range body.TypeParams {
			if i > 0 {
				targs += ", "
			}
			targs += b.goType(args[i], x)
			subst[gp.Name] = b.fc.goType(args[i])
		}
	}
	for i, q := range body.Params[:len(list)] {
		if i != cb && !b.operandFits(list[i], q.Type, subst) { // ir_operand.go
			irNo("std call with a converted operand")
		}
	}
	if goValue(body) && !b.sameRT(b.l.info.Types[x], body.Results[0], subst) {
		irNo("std call with a converted result")
	}
	hidden := b.hidden(body, x, c, p)
	var pre []*irStmt
	var vals []*irExpr
	if ip != nil {
		if b.l.inPlace == nil {
			b.l.inPlace = map[syntax.Node]*inPlaceLit{}
		}
		b.l.inPlace[s.lit] = ip
		func() {
			defer delete(b.l.inPlace, s.lit)
			pre, vals = b.operands(list)
		}()
		bind, fv := b.inPlaceCallback(vals[cb], ip, s.lit, p)
		pre, vals[cb] = append(pre, bind...), fv
	} else {
		pre, vals = b.operands(list)
		// OPC's adaptOperand takes the literal's signature and its site (I-FLIP: OPC x IR-OWN).
		bind, fv := b.adaptOperand(b.l.info.Literals[s.lit], s.lit, vals[cb], body.Params[cb], p) // ir_adapt.go
		pre, vals[cb] = append(pre, bind...), fv
	}
	fresh := "false"
	if s.fresh {
		fresh = "true"
	}
	vals = append(vals, b.cloneLit(s.elem, x, p), &irExpr{K: "const", Pos: p, T: "bool", V: fresh})
	if targs != "" {
		b.keepImport("var _ " + b.l.rt() + ".Site")
	} else {
		b.keepImport("var _ = " + b.l.rtRef(name))
	}
	return pre, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, V: targs, Args: append(hidden, vals...)}, !body.Pure
}

// inPlaceLitIR is the in-place form of the owned site's literal x (ip,
// refown.go inPlaceLit), the IR of the emitter's: func(env, p *T) (R,
// error), or (struct{}, error) for an Update. Its first statement, let
// mut s = v, is not built: s is (*p), the held value in the cell, so every
// read of s reads *p and every write of s or of a part of s writes *p,
// with no copy of T in or out. Its tail (x, s) returns x; the cell keeps
// what s holds (retOf). The body is built with s a local of its own
// (b.locals), then each use of that local becomes *p (irDerefLocal).
func (b *irBuilder) inPlaceLitIR(x *syntax.FuncLit, ip *inPlaceLit, list []syntax.Stmt, end syntax.Pos, endNode syntax.Node) *irExpr {
	l := b.l
	p := b.pos(x, x.Pos())
	goT := b.goType(ip.elem, x)
	res := b.fc.valueResults(nil) // the emitter's (struct{}, error)
	if ip.result != nil {
		res = " (" + b.goType(ip.result, x) + ", error)"
	}
	inner := b.fc.innerFuncCtx()
	inner.env = "env"
	inner.frame = newFrame(ip.result, true, framePolls(l.info, x))
	inner.frame.unit = ip.result == nil
	fc, env, result, loops, tries, switches, label, law, memos, tail := b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label, b.law, b.memos, b.inPlaceTail
	defer func() {
		b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label, b.law, b.memos, b.inPlaceTail = fc, env, result, loops, tries, switches, label, law, memos, tail
	}()
	b.memos = nil
	envLocal := b.temp("env", "*"+l.env(), p)
	ptr := b.temp("p", "*"+goT, p)
	held := b.temp("s", goT, p)
	if _, dup := b.locals[ip.held]; dup {
		irNo("in-place literal whose held local is bound")
	}
	b.locals[ip.held] = held
	lit := &irExpr{K: "func", Pos: p, T: "func(*" + l.env() + ", *" + goT + ")" + res, Params: []int{envLocal, ptr}}
	b.fc, b.env, b.result, b.loops, b.tries, b.switches, b.label, b.law, b.inPlaceTail = inner, envLocal, ip.result, 0, 0, 0, "", nil, ip.tail
	if frameFootprint(l.info, x).EntryPoll && inner.frame.polls {
		lit.Body = append(lit.Body, b.poll(x))
	}
	lit.Body = append(lit.Body, b.bodyStmts(list[1:])...) // ir_fail.go: let mut s = v is not built
	e := b.pos(endNode, end)
	lit.End = &e
	if ip.result != nil {
		lit.Body = append(lit.Body, b.bodyEnd(lit.Body, e)...)
	} else {
		lit.Body = b.unitReturns(lit.Body, e)
	}
	irDerefLocal(lit.Body, held, ptr)
	return lit
}

// irDerefLocal makes each use of the local s in list a use of *ptr: a
// read of s reads *ptr, and s = v (or s op= v) stores v in *ptr. Any
// other statement that names s as its own local (a def, a loop's key or
// value, a tuple's target, a match's subject local) stops the body: the
// in-place form holds s in the cell only.
func irDerefLocal(list []*irStmt, s, ptr int) {
	deref := func(p irPos) *irExpr {
		return &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: ptr}}
	}
	var expr func(e *irExpr)
	var stmts func(list []*irStmt)
	expr = func(e *irExpr) {
		if e == nil {
			return
		}
		if e.K == "local" && e.I == s {
			*e = *deref(e.Pos)
			return
		}
		for _, i := range e.Params {
			if i == s {
				irNo("in-place literal whose held value is a parameter")
			}
		}
		expr(e.X)
		expr(e.Y)
		for _, a := range e.Args {
			expr(a)
		}
		for _, a := range e.Elems {
			expr(a)
		}
		stmts(e.Body)
	}
	stmts = func(list []*irStmt) {
		for _, st := range list {
			if st.Local == s && st.K != "def" && st.K != "set" || st.Key != nil && *st.Key == s || st.Val != nil && *st.Val == s || slices.Contains(st.Ls, s) {
				irNo("in-place literal whose held value is the target of a %s", st.K)
			}
			if st.F != nil && st.F.Subj == s {
				irNo("in-place literal whose held value is a match's subject local")
			}
			switch {
			case st.K == "def" && st.Local == s:
				irNo("in-place literal that binds its held value again")
			case st.K == "set" && st.Local == s:
				// s = v (s op= v): the store into the cell's value.
				st.K, st.Local, st.Y, st.X = "store", 0, st.X, deref(st.Pos)
				expr(st.Y)
			default:
				expr(st.X)
				expr(st.Y)
			}
			stmts(st.Then)
			stmts(st.Else)
			stmts(st.Body)
			for _, c := range st.Cases {
				for _, v := range c.Vals {
					expr(v)
				}
				stmts(c.Body)
			}
		}
	}
	stmts(list)
}

// inPlaceCallback adapts fv, the in-place literal (inPlaceLitIR), to the
// in-place body's callback func(p *T) R, or func(p *T): it calls fv with
// the caller's environment and drops its error, which a literal with no
// error row never returns (the emitter's adaptInPlace).
func (b *irBuilder) inPlaceCallback(fv *irExpr, ip *inPlaceLit, site syntax.Node, p irPos) ([]*irStmt, *irExpr) {
	var pre []*irStmt
	if fv.K != "func" {
		v := b.temp("fv", fv.T, p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: v, X: fv})
		fv = &irExpr{K: "local", Pos: p, I: v}
	}
	goT := "*" + b.goType(ip.elem, site)
	ptr := b.temp("p", goT, p)
	call := &irExpr{K: "vcall", Pos: p, X: fv, Args: []*irExpr{{K: "local", Pos: p, I: b.env}, {K: "local", Pos: p, I: ptr}}}
	lit := &irExpr{K: "func", Pos: p, End: &p, Params: []int{ptr}}
	if ip.result == nil {
		lit.T = "func(" + goT + ")"
		lit.Body = []*irStmt{{K: "expr", Pos: p, X: call}}
		return pre, lit
	}
	rt := b.goType(ip.result, site)
	r, err := b.temp("r", rt, p), b.temp("err", "error", p)
	lit.T = "func(" + goT + ") " + rt
	lit.Body = []*irStmt{{K: "def2", Pos: p, Local: r, Val: &err, X: call}, {K: "ret", Pos: p, X: &irExpr{K: "local", Pos: p, I: r}}}
	return pre, lit
}

// cloneLit is the copy function of an owned site's held type t, the IR
// literal func(v T) T { return copy(v) }, as the emitter's ownedRefCall
// writes it (cloneValue).
func (b *irBuilder) cloneLit(t check.Type, site syntax.Node, p irPos) *irExpr {
	goT := b.goType(t, site)
	v := b.temp("v", goT, p)
	// ir_copy.go: OWN1's copyAs is FORK's copyOf on the line.
	cp := b.copyOf(t, site, &irExpr{K: "local", Pos: p, I: v})
	return &irExpr{K: "func", Pos: p, End: &p, T: "func(" + goT + ") " + goT, Params: []int{v}, Body: []*irStmt{{K: "ret", Pos: p, X: cp}}}
}
