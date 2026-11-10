package rt

import (
	"runtime"
	"testing"
	"unsafe"
)

// stackProbe is a Tracer that measures how deep a new task's body-exit
// reaches into its goroutine's first stack. The task's body records the
// address of a local of its own; at the task's body-exit the probe records
// the address of a local in its Event frame, which the runtime calls through
// the same frames it calls a recorder through.
type stackProbe struct {
	task      uint64
	body, end uintptr
}

func (p *stackProbe) Event(e Event) {
	if e.Kind == TraceBodyExit && e.Task == p.task {
		var here byte
		p.end = uintptr(unsafe.Pointer(&here))
	}
}

// exitDepth returns the bytes between a new task's body frame and the
// tracer's frame at that task's body-exit. A stack copy between the two
// moves the second address to a new stack, and the result is then not a
// depth; the caller's bound catches that too.
func exitDepth() int64 {
	p := &stackProbe{}
	root := TracedRoot("app.main", p)
	s := NewScope(root)
	h := Go(&s, "app.child", func(c *Task) (struct{}, error) {
		var here byte
		p.task, p.body = c.ID(), uintptr(unsafe.Pointer(&here))
		return struct{}{}, nil
	})
	_, _ = h.Wait()
	s.End(nil)
	return int64(p.body) - int64(p.end)
}

// exitDepthBudget is the deepest the traced body-exit path may reach, in
// bytes from the body's frame to the tracer's, measured on go1.27.0
// linux/amd64 (trace-188 round 4). The path measured 512 bytes. Padding
// Event by n words lengthened it by 16n bytes (raiseExit holds the Event
// twice), and the traced spawn benchmark with the real recorder found the
// cliff between 624 bytes (7 words: 1,339 to 1,741 ns/op, the unpadded
// range) and 640 bytes (8 words: 2,997 to 3,495 ns/op, the stack copy). The
// budget is 576 bytes: it fails 48 bytes before the cliff. Since Run builds
// the Cause in exitOf's frame, the path measures 464 bytes, the same with
// and without -cover; Run's own frame, and with it the cliff, did not move.
const exitDepthBudget = 576

// A traced task's body-exit must stay within a new goroutine's first stack:
// past it, every traced spawn pays a stack copy (about 2.5 µs, measured
// twice in trace-188). The guard measures stack addresses, not time, so a
// loaded machine cannot fail it. It runs only where the budget was measured
// and without the race detector, which grows every frame.
func TestTraceExitStaysOnTheFirstStack(t *testing.T) {
	if raceEnabled || runtime.GOARCH != "amd64" {
		t.Skip("the budget was measured on amd64 without the race detector")
	}
	if size := unsafe.Sizeof(Event{}); size > 128 {
		t.Errorf("Event is %d bytes; the stack budget was measured at 128", size)
	}
	d := exitDepth()
	t.Logf("traced body-exit path: %d bytes from the body frame to the tracer frame (budget %d)", d, exitDepthBudget)
	if d <= 0 || d > exitDepthBudget {
		t.Fatalf("traced body-exit path is %d bytes deep (budget %d): a negative or huge value means the stack was copied; either way a traced spawn now risks a stack copy at its exit (see raiseExit)", d, exitDepthBudget)
	}
}
