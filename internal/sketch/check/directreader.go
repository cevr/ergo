package check

// A direct reader (JVD-DIRECT; lower/wirereader.go) is a named,
// non-generic record R that provides the prelude's Reader by its own
// functions, each with the operation's arity and fallibility and R, by
// value, as its first parameter. std json's Reader is one. The lowering
// reads a decode on such an R by calls of R's functions, with no Reader
// interface; a type's own read (types.md, Codecs 11) takes an R. This is
// the one statement of the shape, which both read.

// DirectReaderShape reports that n has a direct reader's shape, with fn
// giving R's function for each operation of the prelude's Reader: the
// conversion table at a decode site in the lowering, n's own functions in
// the checker.
func DirectReaderShape(n *Named, fn func(op string) *Func) bool {
	if n == nil || len(n.TArgs) > 0 {
		return false
	}
	if _, isIface := n.Underlying().(*Interface); isIface {
		return false
	}
	iface, ok := PreludeType(WireReaderName).Underlying().(*Interface)
	if !ok {
		return false
	}
	for _, op := range iface.Methods {
		f := fn(op.Name)
		if f == nil || f.Sig == nil || len(f.Sig.TParams) > 0 {
			return false
		}
		if len(f.Sig.Params) != len(op.Sig.Params)+1 || len(f.Sig.Results) != len(op.Sig.Results) {
			return false
		}
		if (len(solvedElems(f.Sig.Errors)) > 0) != (len(solvedElems(op.Sig.Errors)) > 0) {
			return false
		}
		if m, ok := f.Sig.Params[0].Type.(*Named); !ok || m.Obj != n.Obj || len(m.TArgs) > 0 {
			return false // a provider function on *R
		}
	}
	return true
}

// solvedElems are a row's elements with every inferred row replaced by
// its solution and every row argument by its row; a row parameter stays.
func solvedElems(r *Row) []Type {
	var out []Type
	var walk func(r *Row)
	walk = func(r *Row) {
		for _, e := range r.Elems() {
			switch e := e.(type) {
			case *RowVar:
				walk(e.Solution())
			case *RowArg:
				walk(e.Row)
			default:
				out = append(out, e)
			}
		}
	}
	walk(r)
	return out
}
