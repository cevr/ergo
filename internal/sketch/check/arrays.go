package check

import (
	"go/constant"
	"go/token"
	"math"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Fixed arrays [N]T (196y; types.md, Arrays). An array is a value of N
// elements: its length is part of its type, it is copied as a whole, and
// it lowers to Go's [N]T.

// maxArrayLen bounds an array's length to what every Go target's int
// holds, so the Go build never refuses a length the checker took.
const maxArrayLen = math.MaxInt32

// arrayType resolves `[N]T` with the element type elem.
func (c *checker) arrayType(x *syntax.ArrayType, elem Type) Type {
	// The parser took only an integer literal (grammar.md ArrayType).
	at := made(c, &syntax.BasicLit{ValuePos: x.LenPos, Kind: syntax.INT, Value: x.Len, ValueEnd: x.LenPos + syntax.Pos(len(x.Len))}, syntax.Node(x))
	v := constant.MakeFromLiteral(x.Len, token.INT, 0)
	n, exact := constant.Int64Val(v)
	if v.Kind() != constant.Int || !exact || n > maxArrayLen {
		c.errorf(at, CodeType, "the array length %s is larger than %d; use a slice []T", x.Len, int64(maxArrayLen))
		return invalid
	}
	if isInvalid(elem) {
		return invalid
	}
	return &Array{Len: n, Elem: elem}
}

// arrayElements checks the literal of the fixed array t (types.md,
// Arrays 2): it lists every element in order, or none when the element
// type is defaultable, so no element is invented.
func (c *checker) arrayElements(x *syntax.CompositeLit, t Type, a *Array, lt func() litType) {
	for _, e := range x.Elts {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			c.errorf(kv, CodeType, "an array literal lists its elements in order, with no index")
			return
		}
	}
	n := int64(len(x.Elts))
	switch {
	case n == 0 && defaultable(a.Elem):
	case n == 0:
		c.errorf(x, CodeType, "a literal of %s lists its %d elements: %s has no zero value", c.localTypeText(t), a.Len, c.localTypeText(a.Elem))
	case n != a.Len:
		c.errorf(x, CodeType, "a literal of %s lists all %d elements, found %d", c.localTypeText(t), a.Len, n)
	}
	c.elements(x, a.Elem, lt)
}
