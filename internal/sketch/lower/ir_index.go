package lower

// IR builder: an index of a value that is no slice (FLIP F7, "index of a
// non-slice"): a string's byte and an array's element. Both are Go's own
// index, as the emitter's (expr.go, index): the IR's "index" reads them
// with the decoder's element type (ergoir_f7.go's ergoIndexElem).

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// indexOther is x, an index of a string or an array.
func (b *irBuilder) indexOther(x *syntax.IndexExpr, p irPos) *irExpr {
	t := b.l.info.Types[x.X]
	if n, ok := t.(*check.Named); ok {
		// A type defined over an array (`type Point [8]int32`): Go indexes
		// it as its array, and the decoder reads the element through it.
		if _, isArray := n.Defined().(*check.Array); isArray {
			b.goType(n, x)
			t = n.Defined()
		}
	}
	switch t := t.(type) {
	case *check.Array:
		b.goType(t, x)
	default:
		// A string, or a named type over one (std fs.Text): its byte.
		if bt := b.scalarBasic(t); bt == nil || bt.Kind != check.String {
			irNo("index of a non-slice")
		}
	}
	xs := b.inOrder([]syntax.Expr{x.X, x.Indices[0]}) // ir_order.go
	return &irExpr{K: "index", Pos: p, X: xs[0], Y: xs[1]}
}

// arrayPrefix is the "[N]" that starts the Go type spelling s of an
// array, or "".
func arrayPrefix(s string) string {
	if !strings.HasPrefix(s, "[") {
		return ""
	}
	i := strings.IndexByte(s, ']')
	if i < 2 {
		return ""
	}
	for _, c := range s[1:i] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return s[:i+1]
}
