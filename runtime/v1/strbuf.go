package v1

// StrBuf is a string local that the IR builds by appending (MAPCOPY RU12,
// lower/strbuf.go): a let mut string whose writes are `s = s + p…` and
// `s = e`, with some concatenation in a loop. Go's `s + p` makes a new
// string of the whole text at each concatenation; a StrBuf appends p to a
// byte buffer that it keeps across the loop, and makes a string only when
// the local is read after it changed.
//
// The value is s while dirty is false; while dirty is true it is the
// bytes of b. When inB is true, b holds the value's bytes (dirty) or s's
// (clean), so the next StrCat appends in place; a StrSet points s at its
// operand and leaves b's bytes stale (inB false), and the next StrCat
// copies s into b first. A string StrGet returned is a copy, so b's array
// is never seen through a string and is reused.
//
// The allocations: one string per read after a change (Go's own
// concatenation made one per change), and the buffer's: the first StrCat
// of a StrBuf makes it with room for strBufMin bytes or twice the text,
// and append grows it.
type StrBuf struct {
	s     string
	b     []byte
	inB   bool
	dirty bool
}

// strBufMin is the room a StrBuf's first buffer makes: bytes.Buffer's
// smallBufferSize, the room its first write makes.
const strBufMin = 64

// StrSet makes x's value s, sharing s's bytes.
//
//ergo:wait none
func StrSet(x *StrBuf, s string) {
	x.s = s
	x.inB = false
	x.dirty = false
}

// StrCat appends p to x's value.
//
//ergo:wait none
func StrCat(x *StrBuf, p string) {
	if !x.inB {
		if n := len(x.s) + len(p); cap(x.b) < n {
			x.b = make([]byte, 0, max(strBufMin, 2*n))
		}
		x.b = append(x.b[:0], x.s...)
		x.inB = true
	}
	x.b = append(x.b, p...)
	x.dirty = true
}

// StrGet is x's value.
//
//ergo:wait none
func StrGet(x *StrBuf) string {
	if x.dirty {
		x.s = string(x.b)
		x.dirty = false
	}
	return x.s
}
