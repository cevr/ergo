package v1

import "testing"

var bufferSink string

// TestBorrowedBufferIsReused: a writer's cycle of std strings' Borrow, its
// appends, FromBytes and Give allocates its text and no buffer, as
// encoding/json's pooled encodeState does: Give puts the array back for
// the next Borrow.
func TestBorrowedBufferIsReused(t *testing.T) {
	cycle := func() {
		b := StringsBorrow()
		for range 100 {
			b = append(b, 'x')
		}
		bufferSink = StringsFromBytes(b)
		b = StringsGive(b)
		_ = b
	}
	cycle()
	if n := testing.AllocsPerRun(100, cycle); n != 1 {
		t.Errorf("a borrow, write, text and give allocates %v times; want 1, the text", n)
	}
}
