package v1

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// The defect owner's boundaries (runtime.md, G2; runtime-199 stage 4b m3),
// first probed by the stage 4b review: a defect of the root task alone
// still writes its context, a waiter that is interrupted keeps its child's
// release defect, and every release kind (an exit action, a nested one, a
// Close) adds its own defect after the body's.

// A root-task defect with context in force writes the chain line of main
// and the context under it, shadowed keys left out, outermost first.
func TestDefectRootContext(t *testing.T) {
	withSource(t)
	err := runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			s.OpenContext("trace", "span", "root")
			s.OpenContext("log", "x", "old")
			s.OpenContext("log", "y", "middle")
			s.OpenContext("log", "x", "new")
			boom()
			return nil
		})
	})
	r := reasons(err)
	if len(r) != 1 || len(r[0].Chain) != 1 {
		t.Fatalf("root exit: %v", err)
	}
	want := []Binding{{"trace", "span", "root"}, {"log", "y", "middle"}, {"log", "x", "new"}}
	if got := r[0].Value.(*Panic).Context; !slices.Equal(got, want) {
		t.Fatalf("snapshot %+v, want %+v", got, want)
	}
	var out strings.Builder
	if report(&out, err) != 2 || !strings.Contains(out.String(), " main\n\t  context trace.span=\"root\" log.y=\"middle\" log.x=\"new\"\n") {
		t.Fatalf("root report:\n%s", out.String())
	}
	// With no context in force, the root alone writes no chain.
	err = runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error { boom(); return nil })
	})
	out.Reset()
	if report(&out, err) != 2 || strings.Contains(out.String(), "task #") {
		t.Fatalf("root report without context:\n%s", out.String())
	}
}

// A waiter interrupted while it waits still receives its child's release
// defect, beside the interruption, untraced and traced; a catch admits
// neither. The release runs after the child's region ended, so its
// snapshot holds no binding.
func TestDefectInterruptedWait(t *testing.T) {
	for _, traced := range []bool{false, true} {
		root := rt.Root("root")
		if traced {
			root = rt.TracedRoot("root", &events{})
		}
		err := runOn(root, func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				ready := make(chan struct{})
				child := Go(s, "origin-child", func(ct *rt.Task) (int, error) {
					return frameValue(ct, func(cs *Scope) (int, error) {
						cs.OpenContext("log", "where", "child")
						OnExit(cs, func() { panic("release-after-interrupted-wait") })
						close(ready)
						<-ct.Done()
						ct.Check()
						return 0, nil
					})
				})
				<-ready
				root.Interrupt()
				_, err := child.Wait()
				return err
			})
		})
		r := reasons(err)
		if len(r) != 2 || r[0].Kind != rt.KindDefect || r[1].Kind != rt.KindInterruption || !r[0].InRelease || r[0].Chain[0].Span != "origin-child" {
			t.Fatalf("traced=%v exit %+v", traced, r)
		}
		if _, ok := rt.Failure(err); ok {
			t.Fatalf("traced=%v: a catch admits a defect and an interruption", traced)
		}
		if ctx := r[0].Value.(*Panic).Context; len(ctx) != 0 {
			t.Fatalf("traced=%v: the release kept an ended region: %v", traced, ctx)
		}
	}
}

// The body's defect, an exit action's, a nested exit action's and a
// Close's all survive, in that order, each after the first in a release.
func TestDefectEveryReleaseKind(t *testing.T) {
	err := runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			if err := s.WithAcquire(func() (func(), error) {
				return func() { Release(root, func() { panic("close") }) }, nil
			}); err != nil {
				return err
			}
			OnExit(s, func() {
				OnExit(s, func() { panic("nested") })
				panic("action")
			})
			panic("body")
		})
	})
	r := reasons(err)
	want := []string{"body", "action", "nested", "close"}
	if len(r) != len(want) {
		t.Fatalf("reasons %+v", r)
	}
	for i, reason := range r {
		if reason.Kind != rt.KindDefect || reason.Value.(*Panic).Value != want[i] || reason.InRelease != (i > 0) || len(reason.Chain) != 1 {
			t.Fatalf("reason %d: %+v", i, reason)
		}
	}
	var out strings.Builder
	if report(&out, err) != 2 || strings.Count(out.String(), "panic: ") != 4 || strings.Count(out.String(), "(in an exit action)") != 3 {
		t.Fatalf("report:\n%s", out.String())
	}
}
