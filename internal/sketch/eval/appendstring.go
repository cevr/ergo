package eval

import "ergo.local/prototype/internal/sketch/check"

// appendString runs Go's append(out, s...) (check.AppendsString): the
// bytes of s added at the end of out, in a new slice (interop.md, Lowering 13).
// Concrete operands give the concrete bytes. In a symbolic run the result
// is the term fn(base, text) in normal form: base is never itself such a
// term and holds no known bytes (they move into text), and text is a
// string in concatString's normal form. So append(append(o, a), b) and
// append(o, a + b) are one term, as `+` on strings is.
func appendString(fn *check.Func, out, s Value) Value {
	if s == "" {
		return out
	}
	if sl, ok := out.(sliceV); ok {
		if str, ok := s.(string); ok {
			elems := append([]Value(nil), sl.elems()...)
			for i := 0; i < len(str); i++ {
				elems = append(elems, str[i])
			}
			return newSlice(elems)
		}
		if known, ok := byteString(sl); ok {
			return &extTerm{fn: fn, args: []Value{newSlice(nil), concatString(known, s)}}
		}
	}
	if e, ok := out.(*extTerm); ok && e.fn == fn {
		return &extTerm{fn: fn, args: []Value{e.args[0], concatString(e.args[1], s)}}
	}
	return &extTerm{fn: fn, args: []Value{out, s}}
}

// byteString is the string of a slice of known bytes.
func byteString(sl sliceV) (string, bool) {
	b := make([]byte, 0, sl.n)
	for _, e := range sl.elems() {
		c, ok := e.(uint8)
		if !ok {
			return "", false
		}
		b = append(b, c)
	}
	return string(b), true
}
