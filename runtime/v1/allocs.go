package v1

import (
	"runtime"
	"strconv"

	"ergo.local/prototype/runtime/rt"
)

// AllocsPerRun is std testing's AllocCounter.PerRun (MX-P3 F6): the mean
// number of heap allocations of one call of f, over runs calls after one
// warm-up call, as Go's testing.AllocsPerRun counts them: GOMAXPROCS is 1
// while it counts, runtime.ReadMemStats reads the heap's Mallocs before and
// after, and the mean is an integer division, so a call that allocates
// nothing is 0 exactly. It does not import testing, so a test binary holds
// none of its flags. The count is the process's: an allocation of another
// goroutine in the window counts too, as in Go's, so a law over it runs no
// other task while it counts. Only the test's ambient makes the counter
// (std testing.NewAllocCounter). A runs below 1 is a defect.
//
//ergo:wait none
func AllocsPerRun(t *rt.Task, runs int, f func(*rt.Task)) int {
	if runs < 1 {
		panic("testing.AllocCounter.PerRun: runs " + strconv.Itoa(runs) + " is below 1")
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	f(t)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	before := ms.Mallocs
	for range runs {
		f(t)
	}
	runtime.ReadMemStats(&ms)
	return int((ms.Mallocs - before) / uint64(runs))
}
