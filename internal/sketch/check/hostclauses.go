package check

import (
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// hostResourceParam includes interfaces such as io.Reader and pointer
// parameters: a call may convert a resource to an interface or lend an
// address. Neither shape establishes that Go returns before its last use.
func (c *checker) hostResourceParam(t Type) bool {
	if _, ok := t.(*Pointer); ok {
		return true // a Go pointer may retain even a non-resource borrow
	}
	if n, ok := t.(*Named); ok && isHostNamed(n) && n.Orig == nil && n.state == 1 && n.Obj.Decl != nil {
		// A recursive interface (Chain(other Recursive)) is still being
		// resolved: read its declaration, not a provisional underlying.
		if spec, ok := n.Obj.Decl.Spec.(*syntax.InterfaceType); ok && len(spec.Union) == 0 && aliasTarget(n.Obj.Decl) == nil {
			return true
		}
	}
	return isHostNamed(t) && asInterface(t) != nil || c.mayHoldAny(t)
}

// hostClauses supplies the conservative clause even for an old or edited
// module binding. A binding cannot grant a narrower lifetime by writing a
// plain function type or omitting own. Only reviewed tables can do that.
//
// A Go method or interface operation lends its receiver for the call
// (GB1; interop.md, Resources 5): Go's memory stays safe whatever Go
// keeps, and what the receiver's owner must know, that a result may still
// need the receiver, is the keeps rule (hostKeptLoan). Two receivers stay
// taken whatever the binding writes: the release (a checked Close, R19),
// and a method whose result may hold the receiver itself (hostTakesRecv),
// which would give the caller a second owner of one Go value.
func (c *checker) hostClauses(target string, sig *Signature, at syntax.Node) {
	off := 0
	if d, ok := at.(*syntax.FuncDecl); ok && d.Recv != nil {
		off = 1
	}
	for i, p := range sig.Params {
		if s, ok := under0(p.Type).(*Signature); ok {
			if s.NonEscaping {
				if c.pkg.host != nil && c.pkg.host.cBind {
					// Verified C-binding call-scoped callback (CFFI-3):
					// contract scope=call guarantees synchronous execution
					// during the C call (header+contract SHA-256 stamped).
					continue
				}
				// The reviewed row acts only on its current, validated
				// guard line (LF2d, factguard.go).
				switch ok, refuted, why := SyncActs(target); {
				case refuted:
					c.errorf(at, CodeType, "%s declares a non-escaping Go callback, and its reviewed clause does not hold: %s", target, why)
				case !ok:
					c.errorf(at, CodeType, "%s declares a non-escaping Go callback without a reviewed clause that acts (%s); regenerate its binding", target, why)
				}
			}
			continue
		}
		if c.hostResourceParam(p.Type) && !v1.GoArgLent(target, i+off) && !slices.Contains(sig.Takes, i) {
			if c.pkg.host != nil && c.pkg.host.cBind && c.cBindLentParam(p.Type) {
				continue
			}
			sig.Takes = append(sig.Takes, i)
		}
	}
}

// hostTakesRecv reports a call of a Go method or a Go interface operation
// on recv whose result may hold a value of recv's type itself (gobind's
// mayHold, read from the binding): that receiver is taken, so one Go value
// never has two Ergo owners (GB1). A receiver that holds no resource has
// no owner to protect.
func (c *checker) hostTakesRecv(recv Type, result Type) bool {
	if recv == nil || result == nil || !c.holdsResource(recv) {
		return false
	}
	n, ok := recv.(*Named)
	if e := refElem(recv); e != nil {
		n, ok = e.(*Named)
	}
	if !ok || !isHostNamed(n) {
		return false
	}
	return c.hostMayHold(result, n, map[Type]bool{})
}

// hostRecvCall reports a call of a Go method or of a Go interface
// operation that has a receiver, with that call.
func (w *ownWalk) hostRecvCall(x *syntax.CallExpr) (*Call, bool) {
	call := w.info.Calls[x]
	if call == nil || call.Recv == nil {
		return nil, false
	}
	switch call.Kind {
	case CallFunc:
		f := call.Func
		return call, f != nil && f.Pkg.Host() && f.Decl != nil && f.Decl.Recv != nil
	case CallOp:
		return call, isHostNamed(w.info.Types[call.Recv])
	}
	return nil, false
}

// hostTakes reports a Go method call whose receiver hostTakesRecv takes.
func (w *ownWalk) hostTakes(x *syntax.CallExpr) bool {
	call, ok := w.hostRecvCall(x)
	return ok && w.c.hostTakesRecv(w.info.Types[call.Recv], w.info.Types[x])
}

// hostKeptLoans is the keeps rule of a Go method call (GB1; interop.md,
// Resources 5): a method lends its receiver for the call, and a result that
// may keep a Go value the receiver reaches (hostKeeps) holds a lend of the
// receiver's owner. So the result ends before that owner is released or
// handed off, and is never returned, stored or captured past it: the
// structured lend a closure holds of what it captures (lend.go), with the
// call as its keeper. A receiver with no owner in this frame (no resource,
// or none tracked) lends nothing to keep.
//
// A receiver that is itself a kept value with no owner (a go.Ref a call
// returned) passes its own lends on: the chain keeps the first owner.
func (w *ownWalk) hostKeptLoans(x *syntax.CallExpr) []resourceLoan {
	call, ok := w.hostRecvCall(x)
	if !ok || w.lending == nil || !w.st.reached || !w.c.hostKeeps(w.info.Types[x]) {
		return nil
	}
	if w.info.Closes[x] != nil || w.info.Closes[x.Fun] != nil || w.hostTakes(x) {
		return nil // a taken receiver has no owner left
	}
	if call.Kind == CallFunc && call.Func.Sig != nil && call.Func.Sig.TakesSelf ||
		call.Kind == CallOp && call.Op != nil && call.Op.Sig != nil && call.Op.Sig.TakesSelf {
		return nil
	}
	root, i, ok := w.placeRoot(call.Recv)
	if !ok || root == nil {
		return nil
	}
	if w.vars[i].loanOnly {
		return w.st.slots[i].loans
	}
	return []resourceLoan{{root: root, end: w.point(root, i), kept: calleeText(x)}}
}

// hostMayHold reports a type whose value may hold a value of the Go type
// recv: recv itself, a go.Ref of it, a Go interface (recv may implement
// it), or a field of a Go struct, an element, an Option payload or a
// tuple member that may.
func (c *checker) hostMayHold(t Type, recv *Named, seen map[Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if n, ok := t.(*Named); ok {
		if n.Obj == recv.Obj {
			return true
		}
		if isHostNamed(n) {
			switch u := under(n).(type) {
			case *Interface:
				// recv stands in a Go interface only when its method set
				// has each of the interface's operations by name (gobind's
				// mayHold reads the same).
				has := func(name string) bool { return recv.method(name) != nil }
				if it := asInterface(recv); it != nil {
					has = func(name string) bool { return it.method(name) != nil }
				}
				for _, m := range u.Methods {
					if !has(m.Name) {
						return false
					}
				}
				return true
			case *Struct:
				for _, f := range u.Fields {
					if c.hostMayHold(f.Type, recv, seen) {
						return true
					}
				}
				return false
			}
		}
	}
	if _, ok := t.(*Signature); ok {
		return false
	}
	_, _, _, _, kids := shape(t)
	for _, k := range kids {
		if c.hostMayHold(k, recv, seen) {
			return true
		}
	}
	return false
}

// hostKeeps reports a result of a Go call that may keep a Go value the
// receiver reaches: one that holds a go.Ref, a Go interface or a resource
// (GB1's keeps rule). A basic value, a string or a Go struct of them keeps
// nothing.
func (c *checker) hostKeeps(t Type) bool {
	seen := map[Type]bool{}
	var walk func(Type) bool
	walk = func(t Type) bool {
		if t == nil || seen[t] {
			return false
		}
		seen[t] = true
		if refElem(t) != nil {
			return true
		}
		if n, ok := t.(*Named); ok && isHostNamed(n) {
			switch u := under(n).(type) {
			case *Interface:
				return true
			case *Struct:
				for _, f := range u.Fields {
					if walk(f.Type) {
						return true
					}
				}
			}
		}
		if _, ok := t.(*Signature); ok {
			return false
		}
		_, _, _, _, kids := shape(t)
		return slices.ContainsFunc(kids, walk)
	}
	return c.holdsResource(t) || walk(t)
}
