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
// takes the in-place form (RefModifyInPlace, inPlaceLit), whose literal
// writes the held value through a pointer, the IR takes the owned form:
// the held value is still taken over, with no copy of a Map or slice, but
// the literal copies T in and out (a follow-up: the in-place literal).

import (
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
	case err != nil || len(body.Params) != len(list)+3 || cb >= len(list) || body.Params[cb].Func == nil:
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
		if i != cb && !b.operandFits(list[i], q.Type, subst, body.Package) { // ir_operand.go
			irNo("std call with a converted operand")
		}
	}
	if goValue(body) && !b.sameRT(b.l.info.Types[x], body.Results[0], subst, body.Package) {
		irNo("std call with a converted result")
	}
	hidden := b.hidden(body, x, c, p)
	pre, vals := b.operands(list)
	// OPC's adaptOperand takes the literal's signature and its site (I-FLIP: OPC x IR-OWN).
	bind, fv := b.adaptOperand(b.l.info.Literals[s.lit], s.lit, vals[cb], body.Params[cb], p) // ir_adapt.go
	pre, vals[cb] = append(pre, bind...), fv
	fresh := "false"
	if s.fresh {
		fresh = "true"
	}
	alone := "false"
	if s.alone {
		alone = "true"
	}
	vals = append(vals, b.cloneLit(s.elem, x, p), &irExpr{K: "const", Pos: p, T: "bool", V: fresh}, &irExpr{K: "const", Pos: p, T: "bool", V: alone})
	if targs != "" {
		b.keepImport("var _ " + b.l.rt() + ".Site")
	} else {
		b.keepImport("var _ = " + b.l.rtRef(name))
	}
	return pre, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, V: targs, Args: append(hidden, vals...)}, !body.Pure
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
