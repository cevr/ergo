package lower

// IR builder: float products round (FLOAT-STRICT). See ir.go.
//
// Go may fuse a float product and a sum into one fused multiply-add,
// rounding once, on arm64, ppc64, s390x and riscv64, and across statements
// too; an explicit conversion rounds to the type's precision and stops it
// (the Go spec, Arithmetic operators: "An explicit floating-point type
// conversion rounds to the precision of the target type, preventing
// fusion"). The emitter writes every float product that is not a constant
// as T(a * b) (expr.go roundsProduct, floatLowering); the IR writes the
// same conversion, so a program's float results are the same bits on
// amd64 and on the Mac's arm64 (types.md, Numeric types). floatLowering
// "fuse" is the reference here too.
//
// Prior art: C's FP_CONTRACT pragma and -ffp-contract=off, Rust (which
// never contracts without an explicit mul_add), Java's strictfp (the
// default since Java 17); Gleam, Elixir and ReScript inherit their host's
// rule (BEAM and JS never fuse).

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// rounded is e, the IR of a product of type t at site, under an explicit
// conversion to t when t is a float type and the product is no constant
// (x, its source expression, when it has one); e itself otherwise.
func (b *irBuilder) rounded(site syntax.Node, x syntax.Expr, t check.Type, e *irExpr) *irExpr {
	if e == nil || e.K != "bin" || e.Op != "*" || !roundsFloat(t) || x != nil && b.fc.isConst(x) {
		return e
	}
	return &irExpr{K: "conv", Pos: e.Pos, T: b.goType(t, site), X: e}
}

// roundsFloat reports a product of type t that rounds: t a float type,
// unless floatLowering selects Go's own form.
func roundsFloat(t check.Type) bool { return floatLowering != "fuse" && isFloatType(t) }
