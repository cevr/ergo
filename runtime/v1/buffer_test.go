package v1

import (
	"runtime"
	"testing"
)

var bufferSink string

type (
	hintWriter struct{}
	hintSmall  struct{}
	hintLarge  struct{}
)

// writeCycle is a writer's cycle of std strings' Borrow, n appends,
// FromBytes and Give under hint h.
func writeCycle(h, n int) {
	b := StringsBorrow(h)
	for range n {
		b = append(b, 'x')
	}
	bufferSink = StringsFromBytes(b)
	b = StringsGive(b, h)
	_ = b
}

// TestBorrowedBufferIsReused: a writer's cycle allocates its text and no
// buffer, as encoding/json's pooled encodeState does, for a small text and
// for one that outgrew the least pool size: Give puts the array back in
// the pool its size takes, for the next Borrow.
func TestBorrowedBufferIsReused(t *testing.T) {
	for _, n := range []int{100, 50_000} {
		h := StringsHint[hintWriter, [1]int]()
		if n > 100 {
			h = StringsHint[hintWriter, [2]int]()
		}
		writeCycle(h, n)
		if got := testing.AllocsPerRun(20, func() { writeCycle(h, n) }); got != 1 {
			t.Errorf("a cycle of %d bytes allocates %v times; want 1, the text", n, got)
		}
	}
}

// TestBorrowAfterGCTakesItsHint: after two GCs empty the pools, a borrow
// under a hint that saw a large text allocates one buffer of that size,
// not a 1 KiB one regrown by append (the cold case); and each pair of a
// writer type and a value type keeps its own hint, so a small text of one
// type does not shrink the buffer of another.
func TestBorrowAfterGCTakesItsHint(t *testing.T) {
	large, small := StringsHint[hintWriter, hintLarge](), StringsHint[hintWriter, hintSmall]()
	if large == small || StringsHint[hintWriter, hintLarge]() != large {
		t.Fatalf("hints %d and %d: want one handle per pair, the same each time", large, small)
	}
	writeCycle(large, 50_000)
	writeCycle(small, 10)
	if b := StringsBorrow(large); cap(b) < 50_000 {
		t.Errorf("a borrow after a 50000-byte text has capacity %d; the hint is lost", cap(b))
	} else {
		StringsGive(b[:50_000], large)
	}
	// After the GCs, a cycle pays sync.Pool's own setup, its text and one
	// buffer, whatever its size: the 50000-byte cycle allocates as often
	// as the 10-byte one.
	cold := func(h, n int) float64 {
		return testing.AllocsPerRun(5, func() {
			runtime.GC()
			runtime.GC()
			writeCycle(h, n)
		})
	}
	if l, s := cold(large, 50_000), cold(small, 10); l != s {
		t.Errorf("after two GCs, a cycle of 50000 bytes allocates %v times and one of 10 bytes %v; want the same, one buffer of the hint's size", l, s)
	}
}
