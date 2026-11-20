package lower

// IR builder: values of a type defined over a function type (GP-DEFINED,
// the Seq half of lane B's 6cebc4c40). See ir.go.
//
// A Seq[T] value is the program's Seq[T], a function value of the uniform
// shape (PORT-FORIN, ir_seq.go; admitNamed admits it as its function
// type). Two forms reach it here, each as the emitter writes it:
//
//   - Seq[T](f), the conversion of a function value to the type defined
//     over its signature: Go's conversion between identical underlying
//     types, the decoder's "conv" (ergoFuncConv in the patch);
//   - s(yield), a call of a value of the defined type: the call of its
//     signature (definedSig, valueCall in ir_closure.go).

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// funcConversion reports that the conversion x, T(v), takes a function
// value to a type defined over a function type, or back: the checker
// admitted it, so the two signatures are identical, and Go converts it as
// identical underlying types.
func (b *irBuilder) funcConversion(x *syntax.CallExpr, c *check.Call) bool {
	if len(x.Args) != 1 {
		return false
	}
	to, from := c.Type, b.l.info.Types[x.Args[0]]
	return definedSig(to) != nil && definedSig(from) != nil && isDefinedSig(to) != isDefinedSig(from)
}

// definedSig is the signature of t, a function type or a type defined over
// one (`type Seq[T] func(…)`, an instance's with its arguments), else nil.
func definedSig(t check.Type) *check.Signature {
	if n, ok := t.(*check.Named); ok && n.Obj.Decl != nil && n.Alias() == nil {
		t = n.Defined()
	}
	sig, _ := t.(*check.Signature)
	return sig
}

// isDefinedSig reports t is a type defined over a function type.
func isDefinedSig(t check.Type) bool {
	_, named := t.(*check.Named)
	return named && definedSig(t) != nil
}
