package lower

// IR builder: the operands of a std body's call (FLIP OPC-conv). A
// runtime/v1 body's parameter takes the operand's value after the implicit
// conversion the checker records at it (Info.Implicit), which operands
// applies: a variant passed where its union stands (Ref.Set of Some, an
// actor's Send of one of its messages) reaches the body as the union. The
// operand's type after that conversion is operandType (lane GP's one
// definition, ir_fail.go).

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// operandFits reports whether the operand x passes as the Go parameter
// type goType of a runtime/v1 body at a call whose type parameters are
// subst, with no conversion of its own.
func (b *irBuilder) operandFits(x syntax.Expr, goType string, subst map[string]string) bool {
	t := b.operandType(x)
	return b.sameGo(t, goType) || b.sameRT(t, goType, subst)
}
