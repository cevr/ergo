package lower

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/syntax"
)

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

// compactLoopBound removes the Range record for an already simple bound.
// Computed bounds and local loop nests keep their explicit evaluation
// shapes; changing them can lengthen a hot path in the containing frame.
func (b *irBuilder) compactLoopBound(start, end *irExpr) bool {
	return b.compactCountStart(start) && (end.K == "local" || end.K == "const") && b.compactFrame()
}

func (b *irBuilder) compactFrame() bool {
	if b.compactNest == 0 {
		b.compactNest = 1
		fd, _ := b.decl.(*syntax.FuncDecl)
		if fd == nil {
			return true
		}
		// Stop at the first nested loop and do not count a nested
		// function's own loops as part of this function.
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			if b.compactNest == 2 {
				return false
			}
			switch n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.ForStmt, *syntax.RangeStmt:
				syntax.Inspect(n, func(child syntax.Node) bool {
					if child == n {
						return true
					}
					switch child.(type) {
					case *syntax.FuncLit:
						return false
					case *syntax.ForStmt, *syntax.RangeStmt:
						b.compactNest = 2
						return false
					}
					return b.compactNest != 2
				})
				return false
			}
			return true
		})
	}
	return b.compactNest != 2
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
