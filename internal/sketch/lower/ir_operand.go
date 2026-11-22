package lower

// IR builder: the operands of a std body's call (FLIP OPC-conv). A
// runtime/v1 body's parameter takes the operand's value after the implicit
// conversion the checker records at it (Info.Implicit), which operands
// applies: a variant passed where its union stands (Ref.Set of Some, an
// actor's Send of one of its messages) reaches the body as the union. The
// operand's type after that conversion is operandType (lane GP's one
// definition, ir_fail.go).

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// operandFits reports whether the operand x passes as the Go parameter
// type goType of a runtime/v1 body at a call whose type parameters are
// subst, with no conversion of its own. pkg is the import path of the
// body's package (v1.Body.Package), whose names goType spells unqualified.
func (b *irBuilder) operandFits(x syntax.Expr, goType string, subst map[string]string, pkg string) bool {
	t := b.operandType(x)
	return b.sameGo(t, goType) || b.sameRT(t, goType, subst, pkg)
}

// operandConv is how the operand x passes as the Go parameter type goType
// (operandFits's terms): ok with conv "" when it fits as it is; ok with
// conv goType when x's type is a runtime type over that basic type (fs's
// Text, `type MappedText = string`, at FSClone's string), which a Go
// conversion passes unchanged, as the Go text's assignment does; else not
// ok (CONV-OPERAND).
func (b *irBuilder) operandConv(x syntax.Expr, goType string, subst map[string]string, pkg string) (conv string, ok bool) {
	if b.operandFits(x, goType, subst, pkg) {
		return "", true
	}
	if basicOver(b.operandType(x), goType) {
		return goType, true
	}
	return "", false
}

// basicOver reports whether t is a runtime type over the basic Go type
// goType (fs's Text over string): a Go conversion takes a value between
// them unchanged, either way.
func basicOver(t check.Type, goType string) bool {
	n, isNamed := t.(*check.Named)
	if !isNamed {
		return false
	}
	name, bound := check.GoTypeOf(n)
	if !bound {
		return false
	}
	gt, known := v1.TypeOf(name)
	return known && gt.Basic != "" && gt.Basic == goType
}

// convOperands wraps each operand in args whose convs entry names a Go
// type in that type's conversion (operandConv).
func convOperands(args []*irExpr, convs map[int]string) {
	for i, t := range convs {
		args[i] = &irExpr{K: "conv", Pos: args[i].Pos, T: t, X: args[i]}
	}
}
