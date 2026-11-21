package rt

import (
	"testing"
	"unsafe"
)

// TestTraceOffAddsNoAllocs checks the off path: with no tracer installed the
// poll, a spawn and wait, and an acquire and release allocate exactly what
// they did before the hook existed (MEASURED.md, Measured costs: 0, 3 and 0),
// less the done channel the park word (park.go) took from spawn and wait: 0, 2, 0.
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
	if poll != 0 || spawn != 2 || acquire != 0 {
		t.Fatalf("allocs/op with tracing off: poll %v, spawn+wait %v, acquire+release %v; want 0, 2, 0", poll, spawn, acquire)
	}
	if stopped || r.closed == 0 {
		t.Fatalf("stopped %v, releases %d", stopped, r.closed)
	}
}

// TestTraceHookKeepsTaskSize pins the layout the off path relies on: the
// tracer pointer fits in the word freed by moving shielded beside stopped, so
// a started task stays in its allocation size class (Handle[struct{}] 176 B).
// The auto-clock member pointer (idle.go) took the two words a region
// spill slice gave up for a pointer. The park word (park.go, PARK1) adds
// a word to Task. The waiting slot (waiter.go, WAITOR-WORD) adds one more,
// paid for by the handle's consumed and ended flags, which moved into the
// task's first word (Task.consumed, stopFlags.ended): Task is 144 B, in
// the 144 B class as before, and Handle[struct{}] is 176 B, still in the
// 176 B class.
func TestTraceHookKeepsTaskSize(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the pinned sizes are for 64-bit targets")
	}
	if got := unsafe.Sizeof(Task{}); got != 144 {
		t.Errorf("Task is %d bytes; want 144", got)
	}
	if got := unsafe.Sizeof(Handle[struct{}]{}); got != 176 {
		t.Errorf("Handle[struct{}] is %d bytes; want 176", got)
	}
}
