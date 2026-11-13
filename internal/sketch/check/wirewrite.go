package check

import "fmt"

// A format writes its own types (types.md, Codecs 9; CX1). The package
// of a type X that also declares the cell provider P of a sealed
// operation type providing the prelude's Writer may declare
//
//	func WireWrite(x X, w *P)
//
// Then the derived codec writes a value of X with that function when its
// writer is a cell of P, and by X's ordinary wire form (its by-name pair,
// its structure) on every other writer. It is a protocol function, found
// on X by its name and shape as Wire and WireForm are, so no package or
// type name selects it, and only X's own package can declare it: one
// answer per (type, provider).
//
// Prior art: serde's `#[serde(with = …)]` and a format crate's own
// Serialize impls for its types (quick-xml's `@attr` rename); Go's
// encoding/xml `xml:",attr"` tag, which only encoding/xml reads; Roc's
// Encoding ability, whose format gets the per-type call.

// WireWriteName is the protocol function's name.
const WireWriteName = "WireWrite"

// WireWrite is a type's own write by a format's writer: Func is X's
// WireWrite(x X, w *Provider), and Op the sealed operation type of X's
// package whose one cell provider is Provider and which provides the
// prelude's Writer.
type WireWrite struct {
	Func     *Func
	Op       *Named
	Provider *Named
}

// WireWriteOf is t's own write, when t's package declares one of the
// shape above; the checker has refused every other shape (checkWireWrites).
func WireWriteOf(t Type) (WireWrite, bool) {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || len(n.TArgs) > 0 || len(n.origin().TParams) > 0 {
		return WireWrite{}, false
	}
	f := n.method(WireWriteName)
	if f == nil || f.Sig == nil {
		return WireWrite{}, false
	}
	w, why := wireWriteShape(n, f, f.Sig)
	return w, why == ""
}

// wireWriteShape is f's write of n, or why f, n's WireWrite, is not one.
// sig is f's signature with its rows resolved.
func wireWriteShape(n *Named, f *Func, sig *Signature) (WireWrite, string) {
	switch n.Underlying().(type) {
	case *Struct, *Union:
	default:
		return WireWrite{}, "only a record or a union has a write of its own"
	}
	if f.Ptr || len(sig.TParams) > 0 || len(sig.Params) != 2 || len(sig.Results) != 0 {
		return WireWrite{}, "it takes the value and the writer's provider, func WireWrite(x X, w *P), and gives nothing"
	}
	if !sig.Errors.Empty() || !sig.Uses.Empty() {
		return WireWrite{}, "an encode fails nothing and needs nothing, so it has no fails or needs row"
	}
	ptr, ok := sig.Params[1].Type.(*Pointer)
	var prov *Named
	if ok {
		prov, _ = ptr.Elem.(*Named)
	}
	if prov == nil || prov.Obj == nil || prov.Obj.Pkg != n.Obj.Pkg || len(prov.TArgs) > 0 {
		return WireWrite{}, fmt.Sprintf("its second parameter is a pointer to a provider of package %s", n.Obj.Pkg.Name)
	}
	writer := PreludeType(WireWriterName)
	wi := asInterface(writer)
	for _, tn := range n.Obj.Pkg.Types {
		op, ok := tn.Type.(*Named)
		if !ok || asInterface(op) == nil {
			continue
		}
		conv, ok := CellProvider(op)
		if !ok {
			continue
		}
		from := conv.From
		if p, ok := from.(*Pointer); ok {
			from = p.Elem
		}
		if fn, ok := from.(*Named); !ok || fn.Obj != prov.Obj {
			continue
		}
		all := wi != nil
		for _, m := range wi.Methods {
			if conv.Table[m.Name] == nil {
				all = false
				break
			}
		}
		if all {
			return WireWrite{Func: f, Op: op, Provider: prov}, ""
		}
	}
	return WireWrite{}, fmt.Sprintf("%s is not the cell provider of a sealed operation type of package %s that provides the prelude's Writer", prov, n.Obj.Pkg.Name)
}

// ownMethod reports f a method of its first parameter's type, a type of
// pkg (196m).
func ownMethod(pkg *Package, sig *Signature, f *Func) bool {
	if sig == nil || len(sig.Params) == 0 {
		return false
	}
	t := sig.Params[0].Type
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	return ok && n.Obj != nil && n.Obj.Pkg == pkg && n.method(f.Name) == f
}

// checkWireWrites refuses a WireWrite of this package that is no write of
// its first parameter's type: the derived codec would not call it, so
// the program would not do what it says.
func (c *checker) checkWireWrites() {
	if f, ok := c.pkg.Scope.names[WireWriteName].(*Func); ok && f.Decl != nil && !ownMethod(c.pkg, c.funcSig(f), f) {
		c.cur = f.file
		c.errorf(f.Decl.Name, CodeType, "WireWrite writes a type of its own package: its first parameter is a record or union of package %s (types.md, Codecs 9)", c.pkg.Name)
	}
	for _, tn := range c.pkg.Types {
		n, ok := tn.Type.(*Named)
		if !ok {
			continue
		}
		f := n.method(WireWriteName)
		if f == nil || f.Decl == nil || f.Pkg != c.pkg {
			continue
		}
		sig := c.funcSig(f)
		if sig == nil {
			continue
		}
		s := *sig
		s.Errors, s.Uses = c.types.resolve(sig.Errors), c.types.resolve(sig.Uses)
		if len(n.origin().TParams) > 0 {
			c.cur = f.file
			c.errorf(f.Decl.Name, CodeType, "WireWrite of %s: a generic type has no write of its own yet (types.md, Codecs 9)", n)
			continue
		}
		if _, why := wireWriteShape(n, f, &s); why != "" {
			c.cur = f.file
			c.errorf(f.Decl.Name, CodeType, "WireWrite of %s is no write of its own: %s (types.md, Codecs 9)", n, why)
		}
	}
}
