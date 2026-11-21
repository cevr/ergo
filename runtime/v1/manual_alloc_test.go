package v1

import (
	"runtime"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// TestManualClockAllocatesNothing is the counting guard of time.Manual's
// hot calls (std-spike C1 item d; stores EX5a Found 2): a Read, and an
// Advance that wakes no sleeper, allocate nothing, so a program may read
// and move a virtual clock once per turn without feeding the collector.
// It counts the heap allocations of 1,000 of each, all of them: a cost
// that only some calls pay (a slice that grows) is counted too.
func TestManualClockAllocatesNothing(t *testing.T) {
	task := rt.Root("alloc")
	line := NewTimeline(task, 1_700_000_000, 0)
	var sum int64
	count := func(f func()) uint64 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for range 1000 {
			f()
		}
		runtime.ReadMemStats(&after)
		return after.Mallocs - before.Mallocs
	}
	if n := count(func() { sum += line.Read().Mono }); n != 0 {
		t.Errorf("1,000 Reads allocated %d times", n)
	}
	if n := count(func() { line.Advance(task, time.Millisecond) }); n != 0 {
		t.Errorf("1,000 Advances allocated %d times", n)
	}
	if got := line.Read().Mono; got != int64(1000*time.Millisecond) || sum < 0 {
		t.Fatalf("the clock read %d after 1,000 Advances of 1ms", got)
	}
}
