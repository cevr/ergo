package v1

import (
	"testing"
	"unsafe"
)

// TestBytesViewShape is BytesView's contract (interop.md, Foreign facts
// 8): the string's own bytes with capacity equal to length, so an append
// by the callee copies, and nil for the empty string, as the guarded copy
// gives. Mutants it alone catches: view-empty-not-nil,
// view-capacity-past-length, view-copies (a copy is taken, the view's
// gain lost).
func TestBytesViewShape(t *testing.T) {
	if b := BytesView(""); b != nil {
		t.Errorf("BytesView(\"\") is %#v, want nil", b)
	}
	s := string([]byte("abcdef")[:4])
	b := BytesView(s)
	if string(b) != s || len(b) != len(s) || cap(b) != len(s) {
		t.Fatalf("BytesView(%q) is %q with length %d and capacity %d", s, b, len(b), cap(b))
	}
	if unsafe.SliceData(b) != unsafe.StringData(s) {
		t.Errorf("BytesView copies: its bytes are not the string's")
	}
}
