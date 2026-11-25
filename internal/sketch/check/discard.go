package check

import "ergo.local/prototype/internal/sketch/syntax"

// Dropped pure results (S110; M2-APPEND). A call in statement position, or
// as a body's discarded tail, whose callee is pure and whose operands
// cannot carry a write does nothing: every value is a value (types.md,
// Bindings 1), so `xs.Append(x)` leaves xs as it was and its result is the
// call's only outcome. Go's vet knows a list of such functions
// (unusedresult); Ergo knows the footprint, so the rule names no function:
// a call is refused when
//
//   - it calls a declared function (a dot call or a plain one) whose
//     footprint is empty, or a std or Go extern the footprint counts as
//     pure (pureStd), the prelude's Append, Get, Put and Delete among them;
//   - it cannot fail (a fallible statement is S105) and starts no task; a
//     needs row the body never uses is no effect, the footprint decides;
//   - no operand and no parameter can hold a borrow (a write through *T,
//     the implicit one of a dot call x.f() whose f takes *T included), and
//     no operand, parameter or result holds a resource (the ownership pass
//     reports a dropped resource itself, R42);
//   - it returns a value.
//
// The footprint is known only after every body (decl.go), so unusedValue
// records the candidates and checkDroppedResults judges them.

// droppedCall is a call whose result a statement discards.
type droppedCall struct {
	at   syntax.Expr
	call *syntax.CallExpr
	info *Call
	file *fileInfo
}

// noteDropped records a call whose result is discarded, when its kind is
// one the rule can judge.
func (c *checker) noteDropped(at syntax.Expr, call *syntax.CallExpr, info *Call) {
	if c.silent > 0 || info.Kind != CallFunc || info.Func == nil || info.StartsTask {
		return
	}
	if !info.Fails.Empty() {
		return
	}
	c.dropped = append(c.dropped, droppedCall{at: at, call: call, info: info, file: c.cur})
}

// checkDroppedResults reports each recorded call that does nothing but
// make the value it drops, then each never-reassigned let mut that is not
// the root of such a call's receiver (bindings.go).
func (c *checker) checkDroppedResults() {
	roots := map[*Var]bool{}
	for _, d := range c.dropped {
		if !c.pureCallee(d.info.Func) || !c.plainOperands(d) || c.raisesByName(d.info.Func) {
			continue
		}
		c.cur = d.file
		c.errorf(d.at, CodeUnused, "%s", droppedMessage(d))
		if v := c.rootVar(d.info.Recv); v != nil {
			roots[v] = true
		}
	}
	c.reportUnassigned(roots)
}

// rootVar is the local a receiver place starts from (xs of xs.f.g), or nil.
func (c *checker) rootVar(x syntax.Expr) *Var {
	for x != nil {
		switch e := unparen(x).(type) {
		case *syntax.Ident:
			v, _ := c.pkg.Info.Uses[e].(*Var)
			return v
		case *syntax.SelectorExpr:
			x = e.X
		default:
			return nil
		}
	}
	return nil
}

// raisesByName reports a function with an Ergo body that calls the
// prelude's Unreachable, itself or through a function with an Ergo body it
// calls: its call may end in a defect it names, an outcome a dropped
// result does not lose (`known(v, f)` in std flag checks its handle so).
// A defect that is not named (an index out of range) is not looked for.
// Each function is walked once; a cycle reads false while it is walked.
func (c *checker) raisesByName(f *Func) bool {
	if r, ok := c.raises[f]; ok {
		return r
	}
	if c.raises == nil {
		c.raises = map[*Func]bool{}
	}
	c.raises[f] = false
	if f.Sig != nil && f.Sig.Defects {
		c.raises[f] = true
		return true
	}
	if f.Decl == nil || f.Decl.Body == nil || f.Pkg == nil || f.Pkg.Info == nil {
		return false
	}
	calls := f.Pkg.Info.Calls
	found := false
	syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
		if found {
			return false
		}
		x, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		info := calls[x]
		if IsUnreachable(info) || (info != nil && info.Kind == CallFunc && info.Func != nil && c.raisesByName(info.Func)) {
			found = true
			return false
		}
		return true
	})
	c.raises[f] = found
	return found
}

// pureCallee reports a function whose call runs nothing a footprint counts:
// a pure std or Go extern, or a function with a body whose recorded
// footprint is empty. A recursive or opaque one is not.
func (c *checker) pureCallee(f *Func) bool {
	if f.Decl == nil || MakesHost(f) {
		return false
	}
	if pureStd(f) || preludeCollection(f) {
		return true
	}
	if f.Decl.Extern != nil || f.Decl.Body == nil || f.Pkg == nil || f.Pkg.Info == nil {
		return false
	}
	fp, ok := f.Pkg.Info.Footprints[f.Decl]
	return ok && fp.Empty()
}

// plainOperands reports a call none of whose operands, parameters or
// results can carry a write or an owner: no borrow, no resource. A
// Go-bound method's receiver is no parameter of its signature, so such a
// call is left alone.
func (c *checker) plainOperands(d droppedCall) bool {
	f, info := d.info.Func, d.info
	if f.Sig == nil || (info.Recv != nil && !info.RecvIsParam()) {
		return false
	}
	types := c.pkg.Info.Types
	ops := make([]syntax.Expr, 0, len(d.call.Args)+1)
	if info.Recv != nil {
		ops = append(ops, info.Recv)
	}
	ops = append(ops, d.call.Args...)
	for _, x := range ops {
		t := types[x]
		if t == nil || holdsBorrow(t) || c.holdsResource(t) {
			return false
		}
	}
	for _, p := range f.Sig.Params {
		if holdsBorrow(p.Type) {
			return false
		}
	}
	t := types[d.call]
	return t != nil && !c.holdsResource(t)
}

// droppedMessage says what the call does and what to write instead: the
// rebinding when the call returns a new value of its receiver's type and
// the receiver is a place, else a let or no call.
func droppedMessage(d droppedCall) string {
	name := d.info.Func.Name
	if d.info.Recv != nil {
		if isPlace(d.info.Recv) && sameResult(d) {
			place := exprText(d.info.Recv)
			return "the result of " + place + "." + name + " is not used, and " + name + " leaves " + place +
				" unchanged (a value is a value), so this call does nothing; write " + place + " = " + place + "." + name + "(…) to keep it"
		}
	}
	return "the result of " + name + " is not used, and the call does nothing else (its footprint is empty); bind it with let, or remove the call"
}

// sameResult reports a call whose one result has its receiver's type.
func sameResult(d droppedCall) bool {
	f := d.info.Func
	if len(f.Sig.Results) != 1 || len(f.Sig.Params) == 0 {
		return false
	}
	return sameShape(f.Sig.Results[0], f.Sig.Params[0].Type)
}

// sameShape compares a signature's result with its first parameter as
// written: the same type, or the same named type of the same type
// arguments, type parameters included ([]T and []T, Map[K, V] and Map[K, V]).
func sameShape(a, b Type) bool {
	if a == b {
		return true
	}
	switch a := a.(type) {
	case *Slice:
		b, ok := b.(*Slice)
		return ok && sameShape(a.Elem, b.Elem)
	case *Map:
		b, ok := b.(*Map)
		return ok && sameShape(a.Key, b.Key) && sameShape(a.Elem, b.Elem)
	case *Named:
		b, ok := b.(*Named)
		if !ok || origin(a) != origin(b) || len(a.TArgs) != len(b.TArgs) {
			return false
		}
		for i := range a.TArgs {
			if !sameShape(a.TArgs[i], b.TArgs[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// origin is a named type's generic declaration, or the type itself.
func origin(n *Named) *Named {
	if n.Orig != nil {
		return n.Orig
	}
	return n
}

// isPlace reports a receiver that names a place the caller can assign: a
// name, or a field path of one.
func isPlace(x syntax.Expr) bool {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		return true
	case *syntax.SelectorExpr:
		return isPlace(x.X)
	}
	return false
}
