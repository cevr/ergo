package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// builderAppendFrom is std strings' WriteFrom (std/strings/builder.ergo):
// n bytes added at the end of buf, byte k of them the byte at start+k of
// the result, as an LZ77 decoder copies a match. A negative n, a start
// outside buf, or a start at its end with n > 0 is Go's slice or index
// panic, a defect. Each byte is one append, charged as Append's.
func builderAppendFrom(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
	buf, ok := args[0].(sliceV)
	if !ok {
		return nil, fr.unsupported(at, "%s on bytes the evaluator does not hold", fn.Name)
	}
	start, n := int(args[1].(int64)), int(args[2].(int64))
	length := buf.n
	if bad := n < 0 || start < 0 || start > length || start == length && n > 0; bad {
		// Go's own message, from the same expression on a buffer of
		// buf's length.
		_, why := guard(func() Value {
			b := make([]byte, length)
			if end := start + n; end <= length {
				return b[start:end]
			}
			return b[start]
		})
		return nil, fr.defect(at, "%s", why)
	}
	var v Value = buf
	for k := 0; k < n; k++ {
		cur := v.(sliceV)
		var ab *abrupt
		if v, ab = in.appendTo(fr, at, cur, cur.elems()[start+k], nil); ab != nil {
			return nil, ab
		}
	}
	return v, nil
}
