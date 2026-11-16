package lower

// IR builder: the operands of a std body's call (FLIP OPC-conv). A
// runtime/v1 body's parameter takes the operand's value after the implicit
// conversion the checker records at it (Info.Implicit), which operands
// applies: a variant passed where its union stands (Ref.Set of Some, an
// actor's Send of one of its messages) reaches the body as the union. The
// body's spelling of a runtime type inside a composite ([]HTTPField) reads
// as main.go spells it (rtSpelling).

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// operandType is the type the operand x has where it is passed: the
// union of a variant the checker converts there, else x's own type.
func (b *irBuilder) operandType(x syntax.Expr) check.Type {
	if conv, ok := b.l.info.Implicit[x]; ok && conv.Kind == check.ConvVariant {
		return conv.To
	}
	return b.l.info.Types[x]
}

// operandFits reports whether the operand x passes as the Go parameter
// type goType of a runtime/v1 body at a call whose type parameters are
// subst, with no conversion of its own.
func (b *irBuilder) operandFits(x syntax.Expr, goType string, subst map[string]string) bool {
	t := b.operandType(x)
	return b.sameGo(t, goType) || b.sameRT(t, goType, subst)
}
