package v1

import "unsafe"

// Unchecked loads (owner q112, 2026-11-11; docs/spec/facts.md, Lowering
// 3). The lowering calls these bodies only at an index or a slice of a
// string or a slice that a fact of the lowering proves in range: the
// bounds facts of lower/bounds.go, which come from a comparison that
// dominates the use in the same body (evidence "checked"), and only in the
// run build. The test build and the reference lowering
// (-X …/lower.factLowering=checked) keep Go's checked form at the same
// sites. Each body is small enough for gc to inline, so the call costs
// nothing; what it removes is Go's bounds check, which gc's prove pass
// cannot remove where the fact rests on Ergo's knowledge (a guard written
// with the length on the right, offsets that the guard's margin covers).
//
// Safety invariant (every body below): the caller has proved the bound
// that Go's check would test, 0 <= i < len for an element and
// 0 <= i <= j <= len for a slice, and the operand is not written between
// the proof and the load. So every address formed here lies inside the
// operand's array, as Go's checked form would form it.
// TestBoundsFactsSound (internal/sketch/lower) refuses a site the bound
// does not hold for on generated guards; TestBoundsFactsAgree runs the
// built programs of both lowerings and compares their output.
//
// Prior art: ReScript's Array.getUnsafe and OCaml's -unsafe trust the
// programmer; Rust's get_unchecked is an unsafe call the programmer
// justifies; Ada SPARK removes a run-time check only when GNATprove proves
// it. These bodies follow SPARK: the lowering calls one only on a proof.

// These are lowering helpers, as Let is, and bind no std extern: no Ergo
// declaration names them, so no program calls one but through a proof.

// ByteAt is s[i] with no bounds check; 0 <= i < len(s) holds.
func ByteAt(s string, i int) byte {
	return *(*byte)(unsafe.Add(unsafe.Pointer(unsafe.StringData(s)), i))
}

// StringAt is s[i:j] with no bounds check; 0 <= i <= j <= len(s) holds.
// An empty result is "", so no string points one past the end of s's
// bytes (Go's own slicing keeps such a pointer out of a result too).
func StringAt(s string, i, j int) string {
	if j == i {
		return ""
	}
	return stringOf(unsafe.Add(unsafe.Pointer(unsafe.StringData(s)), i), j-i)
}

// StringN is s[i:i+n] with no bounds check, for a constant n > 0;
// 0 <= i and i+n <= len(s) hold. The lowering passes n as a constant, so
// once gc inlines the call it knows the result's length and proves the
// result's own indexes.
func StringN(s string, i, n int) string {
	return stringOf(unsafe.Add(unsafe.Pointer(unsafe.StringData(s)), i), n)
}

// stringOf is the string of the n bytes at p, built as its two words (a
// pointer and a length, the layout of every Go string), not by
// unsafe.String: unsafe.String checks n and p+n again at run time (a
// compare and a branch per cut, measured on examples/onebrc), and the
// proof has already ruled out both failures. p points inside a live
// string's bytes and n > 0 stays inside them.
func stringOf(p unsafe.Pointer, n int) string {
	h := struct {
		p unsafe.Pointer
		n int
	}{p, n}
	return *(*string)(unsafe.Pointer(&h))
}

// AddrAt is &xs[i] with no bounds check; 0 <= i < len(xs) holds. The
// lowering reads the element through it, *AddrAt(xs, i), which is Go's
// xs[i] as an operand and as an addressable place.
func AddrAt[T any](xs []T, i int) *T {
	p := unsafe.SliceData(xs)
	return (*T)(unsafe.Add(unsafe.Pointer(p), uintptr(i)*unsafe.Sizeof(*p)))
}
