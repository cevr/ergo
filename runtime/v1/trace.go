package v1

import (
	"fmt"
	"os"

	"ergo.local/prototype/runtime/rt"
	"ergo.local/prototype/runtime/rt/rttrace"
)

// TraceEnv names the environment variable that binds a trace sink to a
// program (MU15): when it names a file, Main runs the program on a traced
// root and writes the trace there as JSON Lines (rttrace) when main ends.
// Unset, the root has no tracer, and every emission point costs one load
// and compare (rt.TracedRoot). The rest of the tracing API is #206.
const TraceEnv = "ERGO_TRACE"

// traceCapacity is how many events the recorder keeps; it counts the
// rest as dropped in the trace's header.
const traceCapacity = 1 << 16

// rootFor is the root task main runs on: traced, with the recorder that
// keeps its events, when TraceEnv names a file.
func rootFor() (*rt.Task, func()) {
	path := os.Getenv(TraceEnv)
	if path == "" {
		return rt.Root("main"), func() {}
	}
	rec := rttrace.New(traceCapacity)
	return rt.TracedRoot("main", rec), func() {
		if err := rttrace.WriteFile(path, rec, "", "ergo", nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
}

// UITraced is std ui's traced(): whether the running task's tree records
// a trace, so Play builds a step's line only when it is kept.
func UITraced(t *rt.Task) bool { return t.Traced() }

// UITraceStep is std ui's traceStep(line): a step event of the running
// task (rt.TraceStep), which `ergo test --trace` prints. It reads the
// running task and reaches no host state: the recorder is the trace sink
// TraceEnv bound, written when main ends (stdlib.md, Host packages 7).
func UITraceStep(t *rt.Task, line string) { t.TraceStep(line) }
