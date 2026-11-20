//go:build linux || darwin

package v1

import (
	"strings"
	"testing"
	"unsafe"

	"ergo.local/prototype/runtime/rt"
)

// kept is what the keeping fixture holds after its call; sink takes the
// fixtures' loads, so none is dropped.
var (
	kept []byte
	sink byte
)

// TestGuardedCopyCatches is LF2b's acceptance (1) (interop.md, Foreign
// facts 8): a callee given a guarded copy that writes, writes and
// restores, reads one byte past the end, or keeps the slice and reads it
// after the call faults at the access, and the fault is the ViewDefect
// with its verb; one that only reads runs clean. Then the ring holds no
// more than its bound of copies however many calls end (a count).
//
// Mutants it alone catches: view-guard-is-compare-only (the write that is
// restored passes), view-done-skipped (the late read passes) and
// view-ring-unbounded (every copy stays mapped).
func TestGuardedCopyCatches(t *testing.T) {
	const s = "abcdefgh"
	for _, c := range []struct {
		name  string
		call  func(p []byte)
		after func()
		verb  string
	}{
		{"writes", func(p []byte) { p[2] = 'z' }, nil, "wrote to"},
		{"writes and restores", func(p []byte) { old := p[2]; p[2] = 'z'; p[2] = old }, nil, "wrote to"},
		{"reads past the end", func(p []byte) {
			sink = *(*byte)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(p)), len(p)))
		}, nil, "read past the end of"},
		{"keeps", func(p []byte) { kept = p }, func() { sink = kept[1] }, "kept"},
		{"reads", func(p []byte) { _ = strings.IndexByte(string(p), 'h') }, nil, ""},
	} {
		got := func() (verb string) {
			defer func() {
				if p := recover(); p != nil {
					d, ok := rt.FaultValue(p).(*ViewDefect)
					if !ok {
						t.Errorf("%s: the panic %v is no ViewDefect", c.name, p)
						return
					}
					if !strings.Contains(d.String(), "fixture.F "+d.Verb+" its argument p") {
						t.Errorf("%s: the defect's text %q", c.name, d.String())
					}
					verb = d.Verb
				}
			}()
			g := GuardBytes(s, "fixture.F", "p", "main.ergo:1:1")
			if string(g.Bytes()) != s || cap(g.Bytes()) != len(s) {
				t.Fatalf("%s: the guarded copy is %q with capacity %d", c.name, g.Bytes(), cap(g.Bytes()))
			}
			c.call(g.Bytes())
			g.Done()
			if c.after != nil {
				c.after()
			}
			return ""
		}()
		if got != c.verb {
			t.Errorf("%s: the guard gives %q, want %q", c.name, got, c.verb)
		}
	}
	kept = nil
	if g := GuardBytes("", "fixture.F", "p", "at"); g.Bytes() != nil {
		t.Errorf("the guarded copy of the empty string is %v, want nil as BytesView gives", g.Bytes())
	}
	// The copies of the calls that faulted never reach Done: they stay
	// mapped, outside the ring.
	unfinished := guardsLive() - len(guardRing)
	for range 3 * guardRingLen {
		g := GuardBytes(s, "fixture.F", "p", "at")
		g.Done()
	}
	if n := guardsLive() - unfinished; n > guardRingLen {
		t.Errorf("%d guarded copies stay mapped after their calls, want at most %d", n, guardRingLen)
	}
}
