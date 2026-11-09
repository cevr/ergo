package rt

import (
	"testing"
	"unsafe"
)

// TestTraceOffAddsNoAllocs checks the off path: with no tracer installed the
// poll, a spawn and wait, and an acquire and release allocate exactly what
// they did before the hook existed (MEASURED.md, Measured costs: 0, 3 and 0).
func TestTraceOffAddsNoAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under -race")
	}
	root := Root("off")
	if root.Traced() {
		t.Fatal("Root is traced; want no tracer by default")
	}
	stopped := false
	poll := testing.AllocsPerRun(1000, func() { stopped = stopped || root.Stopped() })
	spawn := testing.AllocsPerRun(1000, func() {
		s := NewScope(root)
		h := Go(&s, "empty", emptyBody)
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
		s.End(nil)
	})
	r := &noop{}
	open := openNoop(r)
	acquire := testing.AllocsPerRun(1000, func() {
		s := NewScope(root)
		defer s.End(nil)
		_, _ = Acquire(&s, open, closeNoop)
	})
	if poll != 0 || spawn != 3 || acquire != 0 {
		t.Fatalf("allocs/op with tracing off: poll %v, spawn+wait %v, acquire+release %v; want 0, 3, 0", poll, spawn, acquire)
	}
	if stopped || r.closed == 0 {
		t.Fatalf("stopped %v, releases %d", stopped, r.closed)
	}
}

// TestTraceHookKeepsTaskSize pins the layout the off path relies on: the
// tracer pointer fits in the word freed by moving shielded beside stopped, so
// a started task stays in its allocation size class (Handle[struct{}] 176 B).
// The auto-clock member pointer (idle.go) took the two words a region
// spill slice gave up for a pointer, so Task is 128 B and
// Handle[struct{}] is 168 B, in the same 176 B class.
func TestTraceHookKeepsTaskSize(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the pinned sizes are for 64-bit targets")
	}
	if got := unsafe.Sizeof(Task{}); got != 128 {
		t.Errorf("Task is %d bytes; want 128", got)
	}
	if got := unsafe.Sizeof(Handle[struct{}]{}); got != 168 {
		t.Errorf("Handle[struct{}] is %d bytes; want 168", got)
	}
}
