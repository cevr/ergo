package check

// Go values that are resources (#196 item 15l, #197 IG9; resources.md
// R1, Q9, Q10; docs/spec/interop.md, Resources). A Go type whose method
// set has Close() or Close() error is a resource at the boundary, as a
// type with a checked Close is (plan 1.2 rule 1): its value has one
// owner, which releases it at its scope's end on every exit, and a second
// use after the hand-off is S108. The binding states the method set, so
// no Go tool runs and no user marks a type.
//
//   - The release is Go's Close. Close() error at an automatic release is
//     a defect in the exit cause, which names the Ergo line of the
//     acquisition (q61): Go's habit of `defer f.Close()` drops it. An
//     explicit `try f.Close()` is the take; it fails go.Error, as any Go
//     call with a last error result does.
//   - A Go function's result of a resource type is owned by the caller
//     (q62): the ownership flow owns a call's result already.
//   - A Go package variable is borrowed (q62): it is a package let, so a
//     take of it is S108 "no owner" and nothing releases it.
//   - A value passed to a Go function is lent for the call: a Go
//     signature cannot move a value, so a resource converted to a Go
//     interface parameter (an io.Reader) is a lend there, not R22's
//     unreleased copy and not R23's move.
//   - Generic Go functions copy each type parameter (D-B): they are
//     bodiless, and the reviewed table (v1.MovesOnly) lists none.
//
// Prior art: Rust's `impl Drop for File` closes and drops the error
// (std::fs::File); Swift's ~Copyable FileDescriptor needs a written
// close; Go's own vet has no check for a leaked *os.File. Gleam, Elixir
// and ReScript leave a host handle to the host (the BEAM closes a port
// with its process).

// hostClose returns the Close of the Go type n: a method Close with no
// parameter (a binding method lists no receiver parameter), no result
// and an error row that is empty or go.Error alone. Its receiver may be T
// or *T (a go.Ref[T] reaches both). nil when n is no Go type or has no
// such Close.
func (c *checker) hostClose(n *Named) *Func {
	if n == nil || n.Obj == nil || !n.Obj.Pkg.Host() {
		return nil
	}
	f := n.method("Close")
	if f == nil {
		return nil
	}
	return c.hostCloseFunc(f)
}

// hostCloseFunc returns f when it is a Go method of the release shape.
func (c *checker) hostCloseFunc(f *Func) *Func {
	if f == nil || f.Name != "Close" || f.Pkg == nil || !f.Pkg.Host() || f.Decl == nil || f.Decl.Recv == nil {
		return nil
	}
	sig := c.funcSig(f)
	if sig == nil || len(sig.Params) != 0 || len(sig.Results) != 0 || len(sig.TParams) != 0 {
		return nil
	}
	if !hostReleaseRow(sig.Errors) {
		return nil
	}
	return f
}

// hostReleaseRow reports an error row a Go Close may have: none, or
// go.Error (Go's Close() error).
func hostReleaseRow(r *Row) bool {
	es := r.Elems()
	return len(es) == 0 || len(es) == 1 && isGoErrorType(es[0])
}

// isGoErrorType reports the predeclared go.Error.
func isGoErrorType(t Type) bool {
	universe()
	n, ok := t.(*Named)
	return ok && n == universeOnce.goPkg.Scope.names["Error"].(*TypeName).Type
}

// hostCloseOp returns the Close operation of a Go interface type it (an
// io.Closer, an io.ReadCloser): a Close with no parameters and results
// whose error row is empty or go.Error. nil for none.
func hostCloseOp(n *Named, it *Interface) *Method {
	if n == nil || n.Obj == nil || !n.Obj.Pkg.Host() || it == nil {
		return nil
	}
	m := it.method("Close")
	if !closeShape(m) || !hostReleaseRow(m.Sig.Errors) {
		return nil
	}
	return m
}

// hostRefClose returns the Close of T for t = go.Ref[T], a Go *T that
// holds a resource; nil for any other type.
func (c *checker) hostRefClose(t Type) *Func {
	e, ok := refElem(t).(*Named)
	if !ok {
		return nil
	}
	return c.hostClose(e)
}

// HostClose reports a release whose Close is a Go method (IG9): the
// lowering calls it through the release boundary and turns its error into
// a defect naming the acquisition (q61).
func (cl *Close) HostClose() bool {
	if cl == nil {
		return false
	}
	if cl.Func != nil {
		return cl.Func.Pkg != nil && cl.Func.Pkg.Host()
	}
	return cl.Op != nil && isHostNamed(cl.Receiver)
}

// HostFallible reports a Go Close that returns an error.
func (cl *Close) HostFallible() bool {
	if !cl.HostClose() {
		return false
	}
	if cl.Func != nil {
		return cl.Func.Sig != nil && !cl.Func.Sig.Errors.Empty()
	}
	return cl.Op.Sig != nil && !cl.Op.Sig.Errors.Empty()
}

// isHostPkgName reports the name of an import go.
func isHostPkgName(o Object) bool {
	pn, ok := o.(*PkgName)
	return ok && pn.Pkg.Host()
}

// hostLendArg reports that pt, the parameter of a Go function or method
// being checked, receives its argument as a lend: a Go signature cannot
// move a value (Go has no ownership), so a resource passed to it stays
// with its owner.
func (c *checker) hostLendArg(t Type) bool {
	return c.hostCall && isHostNamed(t)
}
