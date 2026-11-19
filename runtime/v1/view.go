package v1

import "unsafe"

// The zero-copy view (LAWFFI LF2b; owner q193; docs/spec/interop.md,
// Foreign facts 8). Where Ergo passes the bytes of a string to a Go
// function whose `reads` fact on that parameter is validated and whose
// row in the reviewed table allows a view, the run build passes
// BytesView(s), the string's own bytes, where Go's []byte(s) would copy
// them. The fact says the function neither writes the bytes, keeps them
// nor returns a part of them, so the string stays as Ergo holds it. The
// test build passes a guarded copy instead (GuardBytes), which turns a
// write, a read past the end and a late read through a kept slice into a
// named defect at the access. No Ergo binding ever holds a view: the
// lowering writes BytesView only as a whole argument of the Go call.

// BytesView is the bytes of s, shared with s: nil for the empty string,
// and with capacity equal to length, so an append by the callee copies
// and never writes into the string. It is listed in
// internal/residue/testdata/unsafe.txt.
func BytesView(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
