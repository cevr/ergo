package v1

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// events records a traced tree's events.
type events struct {
	mu  sync.Mutex
	all []rt.Event
}

func (e *events) Event(ev rt.Event) {
	e.mu.Lock()
	e.all = append(e.all, ev)
	e.mu.Unlock()
}

// of returns the recorded events of kind k, in order.
func (e *events) of(k rt.EventKind) []rt.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []rt.Event
	for _, ev := range e.all {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

// shape renders an exit for comparison: each reason's kind, whether a
// release raised it, and a defect's panic value.
func shape(err error) string {
	if err == nil {
		return "ok"
	}
	var c *rt.Cause
	if !errors.As(err, &c) {
		return "failure " + err.Error()
	}
	var parts []string
	for _, r := range c.Reasons {
		switch r.Kind {
		case rt.KindDefect:
			v := r.Value
			if p, ok := v.(*Panic); ok {
				v = p.Value
			}
			parts = append(parts, fmt.Sprintf("defect(%v, release %v)", v, r.InRelease))
		case rt.KindInterruption:
			parts = append(parts, "interruption")
		default:
			parts = append(parts, fmt.Sprintf("failure %v", r.Err))
		}
	}
	return strings.Join(parts, "; ")
}

// runFrame runs body as one lowered frame on root, as the root task's body,
// and returns the frame's exit.
func runFrame(root *rt.Task, body func(t *rt.Task, s *Scope) error) error {
	_, err := rt.Run(root, func(t *rt.Task) (struct{}, error) {
		return guarded(t, func(t *rt.Task) (struct{}, error) {
			return struct{}{}, frame(t, func(s *Scope) error { return body(t, s) })
		})
	})
	return err
}

// TestTracedReleasesMatchUntraced (stage 3b carry, #230): the v1 frame
// list's releases, exit actions and task drains, report through rt's tracer
// with rt.Release. Each case runs untraced and traced: the exits are the
// same, and the traced run raises one release event per release, nested
// releases included, and a release defect's panic event marked as raised
// in a release.
func TestTracedReleasesMatchUntraced(t *testing.T) {
	cases := []struct {
		name     string
		body     func(t *rt.Task, s *Scope) error
		releases int // release events the traced run raises
		panics   int // panic events marked InRelease
	}{
		{"normal: an exit action and an unconsumed task", func(t *rt.Task, s *Scope) error {
			OnExit(s, func() {})
			ownedGo(s, "child at r.ergo:2:3", func(ct *rt.Task) (int, error) { sleepUntilStopped(ct); return 0, nil })
			return nil
		}, 2, 0},
		{"failing: an exit action panics", func(t *rt.Task, s *Scope) error {
			OnExit(s, func() { panic("release broke") })
			return bad{"body"}
		}, 1, 1},
		{"nested: an exit action with its own action and task", func(t *rt.Task, s *Scope) error {
			OnExit(s, func() {
				OnExit(s, func() { panic("inner broke") })
				ownedGo(s, "inner child at r.ergo:5:5", func(ct *rt.Task) (int, error) { sleepUntilStopped(ct); return 0, nil })
			})
			return nil
		}, 3, 1},
		{"a drained task's defect", func(t *rt.Task, s *Scope) error {
			c := ownedGo(s, "child at r.ergo:7:3", func(ct *rt.Task) (int, error) { return explode(), nil })
			_ = c
			return nil
		}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			untraced := runFrame(rt.Root("untraced"), tc.body)
			rec := &events{}
			traced := runFrame(rt.TracedRoot("traced", rec), tc.body)
			if shape(untraced) != shape(traced) {
				t.Fatalf("traced exit %s; untraced %s", shape(traced), shape(untraced))
			}
			if got := len(rec.of(rt.TraceRelease)); got != tc.releases {
				t.Errorf("%d release events; want %d", got, tc.releases)
			}
			inRelease := 0
			for _, ev := range rec.of(rt.TracePanic) {
				if ev.InRelease {
					inRelease++
				}
			}
			if inRelease != tc.panics {
				t.Errorf("%d release panic events; want %d", inRelease, tc.panics)
			}
			t.Logf("exit %s", shape(traced))
		})
	}
}

// TestTracedReleaseUnwindsOnlyItsCalls (stage 3b carry): a capability call
// that an exit action opens and leaves open when it panics is closed by the
// release-local unwind, with the defect, and the release's panic event
// links it; a call open around the whole frame is not closed by the
// release. Without rt.Release's floor, v1's releases closed no call.
func TestTracedReleaseUnwindsOnlyItsCalls(t *testing.T) {
	rec := &events{}
	root := rt.TracedRoot("traced", rec)
	var outer, inner uint64
	_, _ = rt.Run(root, func(t *rt.Task) (struct{}, error) {
		outer = t.TraceCall("Outer.Op", "r.ergo:1:1")
		_, err := guarded(t, func(t *rt.Task) (struct{}, error) {
			return struct{}{}, frame(t, func(s *Scope) error {
				OnExit(s, func() {
					inner = t.TraceCall("Inner.Op", "r.ergo:3:5")
					panic("release broke")
				})
				return nil
			})
		})
		t.TraceCallExit(outer, "Outer.Op", "r.ergo:1:1", nil)
		return struct{}{}, err
	})
	exits := map[uint64][]rt.Event{}
	for _, ev := range rec.of(rt.TraceCallExit) {
		exits[ev.Link] = append(exits[ev.Link], ev)
	}
	if len(exits[inner]) != 1 || exits[inner][0].Defect == nil {
		t.Fatalf("the release's open call: exits %+v; want one, carrying the defect", exits[inner])
	}
	if len(exits[outer]) != 1 || exits[outer][0].Defect != nil {
		t.Fatalf("the outer call: exits %+v; want only its own normal exit", exits[outer])
	}
	panics := rec.of(rt.TracePanic)
	if len(panics) == 0 || !panics[0].InRelease || panics[0].Link != exits[inner][0].ID {
		t.Fatalf("panic events %+v; want the release defect, linking the unwound call %d", panics, exits[inner][0].ID)
	}
}

// TestReleaseDefectEventOrder (stage 3b-runtime review m1, ported from the
// reviewer's TestReviewReleaseDefectEventOrder): an exit action that
// registers a nested exit action and then panics reports in causal order:
// its panic event, then the nested action's release, then its own release,
// as an rt scope that ends a nested scope of its own reports. Round 1
// raised the panic event after the nested release.
func TestReleaseDefectEventOrder(t *testing.T) {
	rec := &events{}
	root := rt.TracedRoot("traced", rec)
	err := runFrame(root, func(t *rt.Task, s *Scope) error {
		OnExit(s, func() {
			OnExit(s, func() {})
			panic("outer broke")
		})
		return nil
	})
	var order []string
	rec.mu.Lock()
	for _, ev := range rec.all {
		switch {
		case ev.Kind == rt.TracePanic && ev.InRelease:
			order = append(order, "panic(in release)")
		case ev.Kind == rt.TraceRelease:
			order = append(order, "release")
		}
	}
	rec.mu.Unlock()
	want := []string{"panic(in release)", "release", "release"}
	if strings.Join(order, " ") != strings.Join(want, " ") || shape(err) != "defect(outer broke, release true)" {
		t.Fatalf("exit %s, event order %v; want %v", shape(err), order, want)
	}
}

// TestReleaseUnmasksOnEveryPath (stage 3b-runtime review m2): the frame's
// release unmasks its task on every path. rt.Release keeps every panic of a
// release (rt's TestReleaseKeepsAPanicOfItsFollowUp), so this test plants an
// entry whose action panics outside it, the one way a panic can leave the
// release loop, and checks that the task is unmasked after the panic.
func TestReleaseUnmasksOnEveryPath(t *testing.T) {
	task := rt.Root("unmask")
	s := NewScope(task)
	s.push(&entry{action: func() { panic("escaped") }})
	escaped := func() (p any) {
		defer func() { p = recover() }()
		s.release()
		return nil
	}()
	if escaped != "escaped" {
		t.Fatalf("recovered %v; want the planted panic", escaped)
	}
	if task.Masked() {
		t.Fatal("the task is still masked after a panic left the release")
	}
}
