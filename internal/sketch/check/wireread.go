package check

import "fmt"

// A format reads its own types (types.md, Codecs 11; WIRE-READ), the read
// mirror of rule 10's WireWrite. The package of a record or union X that
// also declares R, a direct reader (DirectReaderShape), may declare
//
//	func WireRead(x mut X, r R) fails F
//
// Then the derived codec reads a value of X with that function when its
// reader is an R, and by X's ordinary wire form on every other reader. X
// comes first, as in WireWrite, so the function is X's (196m) and a
// package whose R reads several types declares one WireRead each. F joins
// decode's row (wireRow). The checker cannot prove that WireRead reads
// exactly one value, so the package states it as the law
// <X>ReadsOneValue(x X), whose body names R and X; checkWireReads
// requires it.
//
// Prior art: serde's Deserialize impls a format crate writes for its own
// types (serde_json's RawValue, read as the borrowed span of the input);
// Go's json.Unmarshaler, which any type may implement, and json.RawMessage;
// Roc's Decoding ability, whose format gets the per-type call.

// WireReadName is the protocol function's name.
const WireReadName = "WireRead"

// WireReadLawSuffix ends the name of the law a WireRead requires.
const WireReadLawSuffix = "ReadsOneValue"

// WireRead is a type's own read by a format's reader: Func is X's
// WireRead(x mut X, r Reader), and Reader the direct reader R of X's
// package it reads from.
type WireRead struct {
	Func   *Func
	Reader *Named
}

// WireReadOf is t's own read, when t's package declares one of the shape
// above; the checker has refused every other shape (checkWireReads).
func WireReadOf(t Type) (WireRead, bool) {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || len(n.TArgs) > 0 || len(n.origin().TParams) > 0 {
		return WireRead{}, false
	}
	f := n.method(WireReadName)
	if f == nil || f.Sig == nil {
		return WireRead{}, false
	}
	w, why := wireReadShape(n, f, f.Sig)
	return w, why == ""
}

// wireReadShape is f's read of n, or why f, n's WireRead, is not one. sig
// is f's signature with its rows resolved.
func wireReadShape(n *Named, f *Func, sig *Signature) (WireRead, string) {
	switch n.Underlying().(type) {
	case *Struct, *Union:
	default:
		return WireRead{}, "only a record or a union has a read of its own"
	}
	if !f.Ptr || len(sig.TParams) > 0 || len(sig.Params) != 2 || len(sig.Results) != 0 {
		return WireRead{}, "it takes the value to read into and the reader, func WireRead(x mut X, r R) fails F, and gives nothing"
	}
	if !sig.Uses.Empty() {
		return WireRead{}, "a read of its own uses no capability, so it has no needs row"
	}
	r, _ := sig.Params[1].Type.(*Named)
	if r == nil || r.Obj == nil || r.Obj.Pkg != n.Obj.Pkg || len(r.TArgs) > 0 {
		return WireRead{}, fmt.Sprintf("its second parameter is a reader of package %s", n.Obj.Pkg.Name)
	}
	if !DirectReaderShape(r, r.method) {
		return WireRead{}, fmt.Sprintf("%s does not provide the prelude's %s by its own functions", r, WireReaderName)
	}
	return WireRead{Func: f, Reader: r}, ""
}

// wireReadFunc is t's WireRead while bodies are checked, its signature
// resolved here (funcSig), or nil when t has no read of its own of the
// rule's shape; checkWireReads reports a WireRead of another shape.
func (c *checker) wireReadFunc(t Type) *Func {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || len(n.TArgs) > 0 || len(n.origin().TParams) > 0 {
		return nil
	}
	f := n.method(WireReadName)
	if f == nil {
		return nil
	}
	sig := c.funcSig(f)
	if sig == nil {
		return nil
	}
	if len(sig.Params) == 2 {
		// The reader's functions may not have their signatures yet.
		if r, ok := sig.Params[1].Type.(*Named); ok {
			c.resolveMethods(r)
		}
	}
	s := *sig
	s.Uses = c.types.resolve(sig.Uses)
	if _, why := wireReadShape(n, f, &s); why != "" {
		return nil
	}
	return f
}

// resolveMethods resolves the signature of each of the prelude Reader's
// operations that n has a function for, so DirectReaderShape reads them.
func (c *checker) resolveMethods(n *Named) {
	iface, ok := PreludeType(WireReaderName).Underlying().(*Interface)
	if !ok {
		return
	}
	for _, op := range iface.Methods {
		if f := n.method(op.Name); f != nil {
			c.funcSig(f)
		}
	}
}

// wireReadLaw is n's law <X>ReadsOneValue of the shape rule 11 requires:
// one parameter of type n, and a body that names both n and the reader r
// (its subjects, lawplace.go). The body's content is the package's; this
// is the cheap part of the shape the checker can see.
func (c *checker) wireReadLaw(n, r *Named) bool {
	name := n.Obj.Name + WireReadLawSuffix
	for _, law := range c.pkg.Laws {
		if law.Name != name || law.Owner != nil || law.Machine != nil || len(law.Params) != 1 {
			continue
		}
		if p, ok := law.Params[0].Type.(*Named); !ok || p.Obj != n.Obj {
			continue
		}
		names := map[string]bool{}
		for _, s := range law.Subjects {
			names[s.Name] = true
		}
		if names[n.Obj.Name] && names[r.Obj.Name] {
			return true
		}
	}
	return false
}

// checkWireReads refuses a WireRead of this package that is no read of
// its first parameter's type, and one with no law <X>ReadsOneValue: the
// derived codec would not call the first, and nothing would hold the
// second to reading one value.
func (c *checker) checkWireReads() {
	if f, ok := c.pkg.Scope.names[WireReadName].(*Func); ok && f.Decl != nil && !ownMethod(c.pkg, c.funcSig(f), f) {
		c.cur = f.file
		c.errorf(f.Decl.Name, CodeType, "WireRead reads a type of its own package: its first parameter is a record or union of package %s (types.md, Codecs 11)", c.pkg.Name)
	}
	for _, tn := range c.pkg.Types {
		n, ok := tn.Type.(*Named)
		if !ok {
			continue
		}
		f := n.method(WireReadName)
		if f == nil || f.Decl == nil || f.Pkg != c.pkg {
			continue
		}
		sig := c.funcSig(f)
		if sig == nil {
			continue
		}
		if len(sig.Params) == 2 {
			if r, ok := sig.Params[1].Type.(*Named); ok {
				c.resolveMethods(r)
			}
		}
		s := *sig
		s.Errors, s.Uses = c.types.resolve(sig.Errors), c.types.resolve(sig.Uses)
		c.cur = f.file
		if len(n.origin().TParams) > 0 {
			c.errorf(f.Decl.Name, CodeType, "WireRead of %s: a generic type has no read of its own yet (types.md, Codecs 11)", n)
			continue
		}
		w, why := wireReadShape(n, f, &s)
		if why != "" {
			c.errorf(f.Decl.Name, CodeType, "WireRead of %s is no read of its own: %s (types.md, Codecs 11)", n, why)
			continue
		}
		if !c.wireReadLaw(n, w.Reader) {
			c.errorf(f.Decl.Name, CodeType, "WireRead of %s states no law %s%s(x %s) beside it, whose body names %s and %s: write [s, x, s], read it back with a %s, and get s, x and s (types.md, Codecs 11)", n, n.Obj.Name, WireReadLawSuffix, n.Obj.Name, w.Reader.Obj.Name, n.Obj.Name, w.Reader.Obj.Name)
		}
	}
}
