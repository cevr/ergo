package lower

// IR builder: a call into an owning literal (refown.go, V6; FLIP F7
// FORK-OWNLIT). sync.Modify, or a Ref's Update, given a function literal
// written at the call, hands the literal the held value: the call is the
// owned form of its runtime body (RefModifyOwned, RefUpdateOwned), as the
// emitter's ownedRefCall writes it,
//
//	rt.RefModifyOwned[T, R](r, <adapted literal>, func(v T) T { return <copy of v> }, fresh)
//
// its operands in order (the receiver first for an operation), the
// literal adapted as any std callback is (ir_adapt.go), then the copy
// function of T, which the cell calls under its lock when the value it
// holds is shared, and the value walk's proof that the literal's result is
// unshared. The literal's parameter is a fresh local of the value walk
// (handParams), so its body's copies are the facts the IR reads already
// (valueCopy). The in-place form (inPlaceOf; FORK-INPLACE) calls
// RefUpdateInPlace or RefModifyInPlace with the literal built in the
// callback's shape, func(p *T) R, whose held local is *p (inPlaceLit).
//
// Prior art: Swift's Array and Dictionary copy on write after a uniqueness
// test of their storage (isKnownUniquelyReferenced), the cell's shared flag
// here; Clojure's transients and Haskell's ST write a value in place while
// one owner holds it. Gleam, Elixir and ReScript have no shared mutable cell
// over mutable data in their std: an Agent's state is a persistent map.

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// ownedSiteCall is the call x, the owned site s, of a std function (c's
// Func) or of an operation of a std runtime type (c.Recv's).
// inPlaceSite reports an owned Ref site whose literal has the in-place
// form (refown.go inPlaceOf). I-FLIP (FORK-INPLACE x IR-OWN): such a site
// takes FORK's RefUpdateInPlace/RefModifyInPlace call here, as the
// emitter writes it; every other owned site takes IR-OWN's owned form
// (ir_refown.go ownedRefCall).
func (b *irBuilder) inPlaceSite(x *syntax.CallExpr) bool {
	s := b.fc.refSiteOf(x)
	if s == nil || s.lit == nil {
		return false
	}
	ip, _, _ := b.fc.inPlaceOf(s)
	return ip != nil
}

func (b *irBuilder) ownedSiteCall(x *syntax.CallExpr, c *check.Call, s *refSite, p irPos) ([]*irStmt, *irExpr, bool) {
	if s.lit == nil {
		irNo("owning site of a function value") // the mutant hand-nonliteral
	}
	name := s.form.name
	owned, err := runtimeBody(name)
	if err != nil {
		irNo("owning site without its owned body")
	}
	ip, ipName, ipBody := b.fc.inPlaceOf(s)
	if ip != nil {
		name, owned = ipName, ipBody // the in-place form (inPlaceLit)
	}
	list := x.Args
	var targs string
	var subst map[string]string
	if c.Kind == check.CallOp {
		// The owned function takes the receiver type's type arguments
		// (opCallExpr), each the Go type parameter of its position.
		n, ok := b.l.info.Types[c.Recv].(*check.Named)
		if !ok || len(n.TArgs) != len(owned.TypeParams) {
			irNo("owning site on a receiver of another shape")
		}
		var ts []string
		subst = map[string]string{}
		for i, a := range n.TArgs {
			ts = append(ts, b.goType(a, x))
			subst[owned.TypeParams[i].Name] = b.fc.goType(a)
		}
		targs = strings.Join(ts, ", ")
		list = append([]syntax.Expr{c.Recv}, x.Args...)
	} else {
		targs, subst = b.stdTypeArgs(owned, c, x)
	}
	cb := s.form.cb
	if x.Trailing != nil || len(owned.Params) != len(list)+3 || cb >= len(list) || owned.Params[cb].Func == nil {
		irNo("owning site of another shape")
	}
	for i, q := range owned.Params[:len(list)] {
		if i != cb && !b.operandFits(list[i], q.Type, subst, owned.Package) { // ir_operand.go
			irNo("std call with a converted operand")
		}
	}
	if failsHere(c) {
		irNo("fallible call with an empty row at its site")
	}
	if goValue(owned) && !b.sameGo(b.l.info.Types[x], owned.Results[0]) && !b.sameRT(b.l.info.Types[x], owned.Results[0], subst, owned.Package) {
		irNo("std call with a converted result")
	}
	var pre []*irStmt
	var args []*irExpr
	if ip != nil {
		// The literal is built in the callback's own shape, func(p *T) R:
		// it has no statements of its own, so the other operands keep
		// their order around it.
		rest := append(append([]syntax.Expr{}, list[:cb]...), list[cb+1:]...)
		pre, args = b.operands(rest)
		args = append(args[:cb:cb], append([]*irExpr{b.inPlaceLit(s, ip, p)}, args[cb:]...)...)
	} else {
		pre, args = b.operands(list)
		t := b.l.info.Types[list[cb]]
		if n, ok := t.(*check.Named); ok {
			t = n.Underlying()
		}
		sig, _ := t.(*check.Signature)
		bind, fn := b.adaptOperand(sig, list[cb], args[cb], owned.Params[cb], p) // ir_adapt.go
		pre, args[cb] = append(pre, bind...), fn
	}
	args = append(args, b.cellCopy(s, p), &irExpr{K: "const", Pos: p, T: "bool", V: strconv.FormatBool(s.fresh)}, &irExpr{K: "const", Pos: p, T: "bool", V: strconv.FormatBool(s.alone)})
	if targs != "" {
		b.keepImport("var _ " + b.l.rt() + ".Site") // the instance's types may name the body's type parameters
	} else {
		b.keepImport("var _ = " + b.l.rtRef(name))
	}
	// The caller's task and the other hidden operands come first (REF-MASK: f runs masked on it).
	return pre, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, V: targs, Args: append(b.hidden(owned, x, c, p), args...)}, !owned.Pure
}

// inPlaceLit is the in-place literal of the owned site s (refown.go,
// inPlaceLit), as the emitter's funcLitClosure with fc.l.inPlace set and
// adaptInPlace write it, in the callback's shape func(p *T) R directly:
// the held local s is *p (b.held), the `let mut s = v` that binds it is
// not built, and the last value (s, or (x, s)) returns x alone, or
// nothing. The literal runs on the caller's environment, as the emitter's
// captured-environment adapter does.
func (b *irBuilder) inPlaceLit(s *refSite, ip *inPlaceLit, p irPos) *irExpr {
	if b.env < 0 {
		irNo("owning literal in place in a pure function")
	}
	l, lit := b.l, s.lit
	list := lit.Body.List
	gt := b.goType(ip.elem, lit)
	inner := b.fc.innerFuncCtx()
	inner.env = "env"
	inner.frame = newFrame(ip.result, true, framePolls(l.info, lit))
	inner.frame.unit = ip.result == nil
	fc, res, loops, tries, switches, label, law := b.fc, b.result, b.loops, b.tries, b.switches, b.label, b.law
	defer func() {
		b.fc, b.result, b.loops, b.tries, b.switches, b.label, b.law = fc, res, loops, tries, switches, label, law
	}()
	ptr := b.temp("p", "*"+gt, p)
	if b.held == nil {
		b.held = map[*check.Var]int{}
	}
	b.held[ip.held] = ptr
	defer delete(b.held, ip.held)
	b.fc, b.result, b.loops, b.tries, b.switches, b.label, b.law = inner, nil, 0, 0, 0, "", nil
	out := &irExpr{K: "func", Pos: p, T: "func(*" + gt + ")", Params: []int{ptr}}
	if frameFootprint(l.info, lit).EntryPoll && inner.frame.polls {
		out.Body = append(out.Body, b.poll(lit))
	}
	out.Body = append(out.Body, b.tail(list[1:len(list)-1], false)...) // ir_stmt.go
	e := b.pos(lit.Body, lit.Body.Rbrace)
	out.End = &e
	if ip.result != nil {
		x := unparen(ip.tail).(*syntax.TupleExpr).Elts[0]
		pre, v := b.value(x)
		out.Body = append(append(out.Body, pre...), &irStmt{K: "ret", Pos: b.pos(x, x.Pos()), X: v})
		out.T += " " + b.goType(ip.result, lit)
	}
	return out
}

// cellCopy is the copy function the owned body calls on a shared held
// value: func(v T) T { return <v copied as cloneValue copies it> }. Its
// site is the literal's body, no expression, so the copy is never a deep
// one by vals.deep: cloneValue's never is.
func (b *irBuilder) cellCopy(s *refSite, p irPos) *irExpr {
	site := syntax.Node(s.lit.Body)
	gt := b.goType(s.elem, site)
	v := b.temp("v", gt, p)
	body := b.copyOf(s.elem, site, &irExpr{K: "local", Pos: p, I: v}) // ir_copy.go
	return &irExpr{K: "func", Pos: p, End: &p, T: "func(" + gt + ") " + gt, Params: []int{v}, Body: []*irStmt{{K: "ret", Pos: p, X: body}}}
}
