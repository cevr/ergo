package v1

import (
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

// GoTraceEnv names the environment variable that binds a build made with
// --go-trace to Go's tracer (RT-OBS, gotrace_on.go): "flight:FILE" keeps a
// flight recorder that a defect writes to FILE; any other value is the
// file of a full Go execution trace. Each Ergo task is a Go user task, each
// capability call a region, and each task's goroutine carries the pprof
// label ergo.def. A build without --go-trace ignores it.
const GoTraceEnv = "ERGO_GO_TRACE"

// rootFor is the root task main runs on: traced when TraceEnv names a file
// (the recorder that keeps its events) or when a --go-trace build's
// GoTraceEnv is set (Go's tracer), or both.
func rootFor() (*rt.Task, func()) {
	goSink, goDone := goTraceSink()
	path := os.Getenv(TraceEnv)
	if path == "" {
		if goSink == nil {
			return rt.Root("main"), goDone
		}
		return rt.TracedRoot("main", goSink), goDone
	}
	rec := rttrace.New(traceCapacity)
	var sink rt.Tracer = rec
	if goSink != nil {
		sink = tee{rec, goSink}
	}
	return rt.TracedRoot("main", sink), func() {
		goDone()
		if err := rttrace.WriteFile(path, rec, "", "ergo", nil); err != nil {
			_, _ = stderrSink{}.Write([]byte(err.Error() + "\n"))
		}
	}
}

// UITraced is std ui's traced(): whether the running task's tree records
// a trace, so Play builds a step's line only when it is kept.
//
//ergo:wait none
func UITraced(t *rt.Task) bool { return t.Traced() }

// UITraceStep is std ui's traceStep(line): a step event of the running
// task (rt.TraceStep), which `ergo test --trace` prints. It reads the
// running task and reaches no host state: the recorder is the trace sink
// TraceEnv bound, written when main ends (stdlib.md, Host packages 7).
//
//ergo:wait none
func UITraceStep(t *rt.Task, line string) { t.TraceStep(line) }

// tee gives each event to both of its tracers, in order.
type tee [2]rt.Tracer

func (t tee) Event(e rt.Event) {
	t[0].Event(e)
	t[1].Event(e)
}
