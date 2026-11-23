package lower

import "go/constant"

// Compact source shapes avoid spending Go's inlining budget on a Range
// record or a slice capacity wrapper that its SSA passes erase anyway.
// The reference retains both wrappers.
var irCompactGo string

// SetIRCompactGoForTest selects the reference ("off") or compact source
// shapes ("") for the duration of a lowering comparison.
func SetIRCompactGoForTest(mode string) (restore func()) {
	prev := irCompactGo
	irCompactGo = mode
	return func() { irCompactGo = prev }
}

func (b *irBuilder) compactCountStart(e *irExpr) bool {
	v := b.consts[e]
	return irCompactGo != "off" && v != nil && v.Kind() == constant.Int && constant.Sign(v) == 0
}

// compactMakeSize admits a nonnegative constant or an expression whose
// syntax cannot be a Go constant. The old wrapper takes a negative
// constant at runtime; spelling it directly as make would not compile.
func (b *irBuilder) compactMakeSize(e *irExpr) bool {
	if v := b.consts[e]; v != nil {
		return v.Kind() == constant.Int && constant.Sign(v) >= 0
	}
	return dynamicMakeSize(e)
}

func dynamicMakeSize(e *irExpr) bool {
	switch e.K {
	case "local", "call", "mcall", "pcall", "vcall", "field", "index", "deref":
		return true
	case "conv", "un":
		return dynamicMakeSize(e.X)
	case "bin":
		return dynamicMakeSize(e.X) || dynamicMakeSize(e.Y)
	}
	return false
}
