package obs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// recorder is a goTrace that writes down what the sink asked of Go's
// tracer: "task NAME under PARENT", "end NAME", "region NAME in TASK",
// "end region NAME", "log CATEGORY in TASK". A context names the Go task
// it carries (the "task" key below).
type recorder struct {
	mu  sync.Mutex
	got []string
}

type taskKey struct{}

func nameOf(ctx context.Context) string {
	if n, ok := ctx.Value(taskKey{}).(string); ok {
		return n
	}
	return "-"
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.got = append(r.got, s)
	r.mu.Unlock()
}

func (r *recorder) NewTask(ctx context.Context, name string) (context.Context, func()) {
	r.add("task " + name + " under " + nameOf(ctx))
	return context.WithValue(ctx, taskKey{}, name), func() { r.add("end " + name) }
}

func (r *recorder) StartRegion(ctx context.Context, name string) func() {
	r.add("region " + name + " in " + nameOf(ctx))
	return func() { r.add("end region " + name) }
}

func (r *recorder) Log(ctx context.Context, category, message string) {
	r.add("log " + category + " in " + nameOf(ctx))
}

// tree runs a parent with one child on root: the child makes a capability
// call that fails, and the parent waits for it and then fails with a
// defect.
func tree(root *rt.Task) error {
	_, err := rt.Run(root, func(t *rt.Task) (int, error) {
		s := rt.NewScope(t)
		h := rt.Go(&s, "app.child", func(ct *rt.Task) (int, error) {
			call := ct.TraceCall("app.Store.Get", "app.ergo:3:4")
			failure := errors.New("missing")
			ct.TraceCallExit(call, "app.Store.Get", "app.ergo:3:4", failure)
			return 0, failure
		})
		_, _ = h.Wait()
		var err error
		s.End(&err)
		panic("broken invariant")
	})
	return err
}

// TestObsMapsTheTree: each Ergo task becomes a Go task under its parent's
// Go task, each capability call a region of its task, and a defect a log
// entry of its task followed by the defect hook. Mutants: obs-flat-tasks
// (each Go task starts under the background context, so the child is not
// under app.main) and obs-no-region (a call opens no region).
func TestObsMapsTheTree(t *testing.T) {
	r := &recorder{}
	defects := 0
	s := newSink(r, func(context.Context) {}, func() { defects++ })
	if err := tree(rt.TracedRoot("app.main", s)); err == nil {
		t.Fatalf("the tree ended with no exit; want its defect; the sink asked for\n%s", strings.Join(r.got, "\n"))
	}
	want := []string{
		"task app.main under -",
		"task app.child under app.main",
		"region app.Store.Get in app.child",
		"end region app.Store.Get",
		"end app.child",
		"log ergo.defect in app.main",
		"end app.main",
	}
	if !slices.Equal(r.got, want) {
		t.Fatalf("the sink asked Go's tracer for\n%s\nwant\n%s", strings.Join(r.got, "\n"), strings.Join(want, "\n"))
	}
	if defects != 1 {
		t.Fatalf("the defect hook ran %d times; want once", defects)
	}
	if n := s.Unpaired.Load(); n != 0 {
		t.Fatalf("%d regions unpaired; want none", n)
	}
}

// TestObsLabelsTaskGoroutines: a task's goroutine, and a goroutine it
// starts, carry the label ergo.def with the task's definition, which a
// goroutine profile prints. Mutant: obs-no-labels (start sets no labels).
func TestObsLabelsTaskGoroutines(t *testing.T) {
	s := New(nil)
	release := make(chan struct{})
	parked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			_, err := rt.Run(rt.TracedRoot("app.labelled", s), func(*rt.Task) (int, error) {
				inner := make(chan struct{})
				go func() { close(inner); <-release }()
				<-inner
				close(parked)
				<-release
				return 0, nil
			})
			return err
		}()
	}()
	<-parked
	var out bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&out, 1); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	label := fmt.Sprintf("%q:%q", DefLabel, "app.labelled")
	if n := strings.Count(out.String(), label); n < 1 {
		t.Fatalf("no goroutine carries %s; want the task's and its child's\n%s", label, out.String())
	}
}

// TestObsFlightWritesOnDefect: with a flight recorder, a defect writes the
// recorder's window once, and the window holds the defect's task by name.
// Mutant: obs-flight-no-dump (the defect hook writes nothing).
func TestObsFlightWritesOnDefect(t *testing.T) {
	if trace.IsEnabled() {
		t.Skip("a trace already runs")
	}
	f, err := StartFlight()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	var window bytes.Buffer
	var dumpErr error
	s := New(func() { _, dumpErr = f.Dump(&window) })
	_ = tree(rt.TracedRoot("app.flight", s))
	if dumpErr != nil {
		t.Fatal(dumpErr)
	}
	if window.Len() == 0 || !bytes.Contains(window.Bytes(), []byte("app.flight")) {
		t.Fatalf("the defect wrote %d bytes, naming app.flight %v; want the window with the task", window.Len(), bytes.Contains(window.Bytes(), []byte("app.flight")))
	}
	if wrote, _ := f.Dump(&window); wrote {
		t.Fatal("a second Dump wrote again; want one window per Flight")
	}
}
