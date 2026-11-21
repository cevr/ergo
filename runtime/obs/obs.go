// Package obs shows an Ergo program's task tree to Go's own tools (RT-OBS).
// Sink is an rt.Tracer that turns the tree's events into a Go execution
// trace and into pprof labels: each Ergo task is a Go user task
// (trace.NewTask) under its parent's, each capability call a region
// (trace.StartRegion) inside it, and each task's goroutine carries the
// label ergo.def, the task's definition, which the goroutines it starts
// inherit, so a CPU profile groups samples by Ergo definition. A defect is
// a trace log entry; with a flight recorder, it also writes the recorder's
// window to a file.
//
// The package is linked only into a build that asks for it (ergo build
// --go-trace): runtime/trace and runtime/pprof stay out of every other
// program. Its init installs it in runtime/v1, and ERGO_GO_TRACE picks the
// mode at run time (see Install).
package obs

import (
	"context"
	"io"
	"runtime/pprof"
	"runtime/trace"
	"sync"
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

// DefLabel is the pprof label each task's goroutine carries: the task's
// definition ID.
const DefLabel = "ergo.def"

// goTrace is the part of runtime/trace the sink uses, so a test can record
// what the sink asks of Go's tracer without parsing a trace.
type goTrace interface {
	NewTask(ctx context.Context, name string) (context.Context, func())
	StartRegion(ctx context.Context, name string) func()
	Log(ctx context.Context, category, message string)
}

// runtimeTrace is goTrace over runtime/trace.
type runtimeTrace struct{}

func (runtimeTrace) NewTask(ctx context.Context, name string) (context.Context, func()) {
	ctx, t := trace.NewTask(ctx, name)
	return ctx, t.End
}

func (runtimeTrace) StartRegion(ctx context.Context, name string) func() {
	return trace.StartRegion(ctx, name).End
}

func (runtimeTrace) Log(ctx context.Context, category, message string) {
	trace.Log(ctx, category, message)
}

// Sink is the rt.Tracer of one task tree. The runtime calls Event on the
// goroutine that raised the event: a task's start and body-exit on the
// task's own goroutine, a call and its exit on the caller's. Event never
// blocks on the program and never calls back into a task.
type Sink struct {
	tr goTrace
	// labels sets the calling goroutine's pprof labels.
	labels func(ctx context.Context)
	// defect runs after a panic event is logged (the flight recorder's
	// dump), or is nil.
	defect func()

	mu    sync.Mutex
	tasks map[uint64]*task

	// Unpaired counts call-exits whose region was not the innermost open
	// one of their task (an unwind that skipped it): such a region is
	// dropped, never ended on another goroutine.
	Unpaired atomic.Int64
}

// task is one live Ergo task: its Go context (its Go task and labels), the
// end of its Go task, and its open regions, innermost last.
type task struct {
	ctx     context.Context
	end     func()
	regions []region
}

type region struct {
	call uint64
	end  func()
}

// New is a Sink over Go's runtime/trace, which sets each task goroutine's
// pprof labels. defect, if not nil, runs after each panic event.
func New(defect func()) *Sink {
	return newSink(runtimeTrace{}, pprof.SetGoroutineLabels, defect)
}

func newSink(tr goTrace, labels func(context.Context), defect func()) *Sink {
	return &Sink{tr: tr, labels: labels, defect: defect, tasks: map[uint64]*task{}}
}

// Event maps one Ergo event to Go's tracer (rt.Tracer).
func (s *Sink) Event(e rt.Event) {
	switch e.Kind {
	case rt.TraceStart:
		s.start(e)
	case rt.TraceBodyExit:
		s.exit(e)
	case rt.TraceCall:
		s.call(e)
	case rt.TraceCallExit:
		s.callExit(e)
	case rt.TracePanic:
		s.panicked(e)
	}
}

func (s *Sink) start(e rt.Event) {
	s.mu.Lock()
	parent := context.Background()
	if p := s.tasks[e.Parent]; p != nil {
		parent = p.ctx
	}
	s.mu.Unlock()
	ctx, end := s.tr.NewTask(parent, e.Def)
	ctx = pprof.WithLabels(ctx, pprof.Labels(DefLabel, e.Def))
	s.labels(ctx)
	s.mu.Lock()
	s.tasks[e.Task] = &task{ctx: ctx, end: end}
	s.mu.Unlock()
}

func (s *Sink) exit(e rt.Event) {
	s.mu.Lock()
	t := s.tasks[e.Task]
	delete(s.tasks, e.Task)
	s.mu.Unlock()
	if t == nil {
		return
	}
	for i := len(t.regions) - 1; i >= 0; i-- {
		t.regions[i].end()
	}
	t.end()
}

func (s *Sink) call(e rt.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tasks[e.Task]; t != nil {
		t.regions = append(t.regions, region{call: e.ID, end: s.tr.StartRegion(t.ctx, e.Def)})
	}
}

func (s *Sink) callExit(e rt.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tasks[e.Task]
	if t == nil {
		return
	}
	n := len(t.regions)
	if n > 0 && t.regions[n-1].call == e.Link {
		t.regions[n-1].end()
		t.regions = t.regions[:n-1]
		return
	}
	for i := n - 1; i >= 0; i-- {
		if t.regions[i].call == e.Link {
			s.Unpaired.Add(int64(n - i))
			t.regions = t.regions[:i]
			return
		}
	}
}

func (s *Sink) panicked(e rt.Event) {
	s.mu.Lock()
	ctx := context.Background()
	if t := s.tasks[e.Task]; t != nil {
		ctx = t.ctx
	}
	s.mu.Unlock()
	s.tr.Log(ctx, "ergo.defect", rt.Text(e.Defect))
	if s.defect != nil {
		s.defect()
	}
}

// Flight keeps the last seconds of the Go execution trace in memory
// (trace.FlightRecorder) and writes the window to w on a defect: at most
// once per Flight, so a cascade of panics writes one window.
type Flight struct {
	fr      *trace.FlightRecorder
	written atomic.Bool
}

// StartFlight starts a flight recorder.
func StartFlight() (*Flight, error) {
	fr := trace.NewFlightRecorder(trace.FlightRecorderConfig{})
	if err := fr.Start(); err != nil {
		return nil, err
	}
	return &Flight{fr: fr}, nil
}

// Dump writes the recorder's window to w the first time it is called and
// reports whether it wrote.
func (f *Flight) Dump(w io.Writer) (bool, error) {
	if !f.written.CompareAndSwap(false, true) {
		return false, nil
	}
	_, err := f.fr.WriteTo(w)
	return true, err
}

// Stop stops the recorder.
func (f *Flight) Stop() { f.fr.Stop() }
