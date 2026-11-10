package rt

import (
	"errors"
	"runtime"
	"sync"
	"testing"
	"weak"
)

// Reviewer probe (round 1, M4): the root runs through the same boundary as a
// child, so a stop beats success at the root too.
func TestRootStopWinsOverSuccess(t *testing.T) {
	t.Run("Run", func(t *testing.T) {
		root := Root("main")
		v, err := Run(root, func(task *Task) (int, error) {
			task.Interrupt()
			return 42, nil
		})
		if v != 0 || err != Interrupted {
			t.Fatalf("got %d, %v; want the interruption", v, err)
		}
		if ExitCode(err) != 130 {
			t.Fatalf("exit code %d, want 130", ExitCode(err))
		}
	})
	t.Run("empty scope", func(t *testing.T) {
		root := Root("main")
		v, err := Scoped(root, nil, func(*Scope) (int, error) {
			root.Interrupt()
			return 42, nil
		})
		if v != 0 || !errors.Is(err, Interrupted) {
			t.Fatalf("got %d, %v; want the interruption", v, err)
		}
	})
	t.Run("masked stop stays deferred", func(t *testing.T) {
		root := Root("main")
		m := root.Mask()
		v, err := Run(root, func(task *Task) (int, error) {
			task.Interrupt()
			return 42, nil
		})
		m.Unmask()
		if v != 42 || err != nil {
			t.Fatalf("got %d, %v; a stop inside a mask must not be observed", v, err)
		}
	})
}

// Reviewer probe (round 1, M4): a stop requested during a release is masked
// while the release runs and observed when the scope ends.
func TestStopDuringReleaseIsObservedAtScopeEnd(t *testing.T) {
	root := Root("main")
	var stoppedInRelease bool
	_, err := Scoped(root, nil, func(s *Scope) (int, error) {
		s.Defer(func() {
			root.Interrupt()
			stoppedInRelease = root.Stopped()
		})
		return 0, nil
	})
	if stoppedInRelease {
		t.Fatal("a stop was observable inside a release")
	}
	if err != Interrupted {
		t.Fatalf("exit %v, want Interrupted", err)
	}
}

func TestRunKeepsDefectAndExitCode(t *testing.T) {
	root := Root("main")
	_, err := Run(root, func(*Task) (int, error) { panic("main defect") })
	c, ok := err.(*Cause)
	if !ok || len(c.Reasons) != 1 || c.Reasons[0].Value != "main defect" || c.Reasons[0].Chain[0].Span != "main" {
		t.Fatalf("exit %v, want the defect with the root's chain", err)
	}
	if ExitCode(err) != 1 || ExitCode(nil) != 0 {
		t.Fatalf("exit codes %d, %d; want 1 and 0", ExitCode(err), ExitCode(nil))
	}
}

// Reviewer probe (round 1, M5): a finished task keeps no live link to its
// ancestors, so a 129-deep finished chain retains a bounded number of tasks.
func TestFinishedAncestryRetentionIsBounded(t *testing.T) {
	const depth = 129
	var mu sync.Mutex
	var handles []weak.Pointer[Task]
	var build func(*Task, int) (*Task, error)
	build = func(task *Task, n int) (*Task, error) {
		if n == 0 {
			return task, nil
		}
		return Scoped(task, nil, func(s *Scope) (*Task, error) {
			h := Go(s, "nested", func(child *Task) (*Task, error) { return build(child, n-1) })
			mu.Lock()
			handles = append(handles, weak.Make(h.Task()))
			mu.Unlock()
			return h.Wait()
		})
	}
	root := Root("main")
	leaf, err := Run(root, func(task *Task) (*Task, error) { return build(task, depth) })
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != depth {
		t.Fatalf("built %d tasks, want %d", len(handles), depth)
	}
	linked := 0
	for task := leaf; task != nil; task = task.Parent() {
		linked++
	}
	if linked != 1 {
		t.Fatalf("a finished leaf still links %d tasks through Parent", linked)
	}
	runtime.GC()
	runtime.GC()
	live := 0
	for _, w := range handles {
		if w.Value() != nil {
			live++
		}
	}
	// The leaf's own handle stays reachable through leaf; every ancestor must
	// be collectable.
	if live > 1 {
		t.Fatalf("%d of %d finished tasks are still reachable from the leaf", live, depth)
	}
	runtime.KeepAlive(leaf)
}
