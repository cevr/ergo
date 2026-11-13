package eval

import (
	"runtime/debug"
	"sync"
)

// runGCPercent is the collector's target while an evaluation runs (ORACLE
// O32). The evaluator makes many short-lived values, and at Go's default
// of 100 the collector spent a quarter of its instructions; at 400 a step
// costs a sixth less. The heap stays bounded: each obligation's values are
// bounded by its byte budget (Budget.Bytes).
const runGCPercent = 400

var gcRaise struct {
	mu    sync.Mutex
	n     int // evaluations running
	saved int // the target before the first of them
}

// raiseGC sets the collector's target to runGCPercent while an evaluation
// runs; the func it returns restores the target the process had once the
// last evaluation running ends. A target the process set higher stays.
func raiseGC() func() {
	gcRaise.mu.Lock()
	defer gcRaise.mu.Unlock()
	if gcRaise.n == 0 {
		gcRaise.saved = debug.SetGCPercent(runGCPercent)
		if gcRaise.saved < 0 || gcRaise.saved > runGCPercent {
			debug.SetGCPercent(gcRaise.saved)
		}
	}
	gcRaise.n++
	return func() {
		gcRaise.mu.Lock()
		defer gcRaise.mu.Unlock()
		gcRaise.n--
		if gcRaise.n == 0 {
			debug.SetGCPercent(gcRaise.saved)
		}
	}
}
