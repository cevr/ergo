package rt

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestChildDefectCarriesChain(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	var handleID, workerID uint64
	h := Go(&s, "handle", func(task *Task) (int, error) {
		handleID = task.ID()
		return Scoped(task, nil, func(s *Scope) (int, error) {
			w := Go(s, "worker", func(task *Task) (int, error) {
				workerID = task.ID()
				var xs []int
				return xs[3], nil
			})
			return w.Wait()
		})
	})
	_, err := h.Wait()
	c, ok := err.(*Cause)
	if !ok || len(c.Reasons) != 1 || c.Reasons[0].Kind != KindDefect {
		t.Fatalf("exit %v, want one defect", err)
	}
	r := c.Reasons[0]
	if _, isRuntime := r.Value.(runtime.Error); !isRuntime {
		t.Fatalf("defect value %v (%T), want the runtime panic", r.Value, r.Value)
	}
	want := []Frame{{workerID, "worker"}, {handleID, "handle"}, {root.ID(), "main"}}
	if !slices.Equal(r.Chain, want) {
		t.Fatalf("chain %v, want %v", r.Chain, want)
	}
	if msg := err.Error(); !strings.Contains(msg, fmt.Sprintf("task #%d worker\n  task #%d handle", workerID, handleID)) {
		t.Fatalf("message does not print the chain:\n%s", msg)
	}
	if _, lone := Failure(err); lone {
		t.Fatal("a catch would match a defect")
	}
	_ = endScope(&s)
}

func TestWaitReturnsValueOrFailure(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	ok := Go(&s, "ok", func(*Task) (string, error) { return "v", nil })
	failed := Go(&s, "failed", func(*Task) (string, error) { return "ignored", errBody })
	if v, err := ok.Wait(); v != "v" || err != nil {
		t.Fatalf("got %q, %v", v, err)
	}
	if v, err := failed.Wait(); v != "" || err != errBody {
		t.Fatalf("got %q, %v; want the lone typed failure as itself", v, err)
	}
	_ = endScope(&s)
}

func TestWaitIsAnInterruptionPoint(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	var drained bool
	waiter := Go(&s, "waiter", func(task *Task) (int, error) {
		return Scoped(task, nil, func(s *Scope) (int, error) {
			child := Go(s, "child", func(task *Task) (int, error) {
				<-task.Done()
				drained = true
				return 0, Interrupted
			})
			return child.Wait()
		})
	})
	if err := waiter.Stop(); err != nil {
		t.Fatalf("Stop returned %v", err)
	}
	if !drained {
		t.Fatal("an interrupted Wait returned before its task drained")
	}
	_ = endScope(&s)

	// Reviewer probe (round 1, M3): the owner's stop is checked before the
	// completion fast path, and the finished task's defect is kept.
	for _, tc := range []struct {
		name string
		body func(*Task) (int, error)
		want []Kind
	}{
		{"finished value", func(*Task) (int, error) { return 42, nil }, nil},
		{"finished failure", func(*Task) (int, error) { return 0, errBody }, nil},
		{"finished defect", func(*Task) (int, error) { panic("child defect") }, []Kind{KindDefect}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := Root("main")
			s := NewScope(root)
			h := Go(&s, "done", tc.body)
			<-h.done
			root.Interrupt()
			v, err := h.Wait()
			if v != 0 || !errors.Is(err, Interrupted) {
				t.Fatalf("a stopped owner joined a finished task: %d, %v", v, err)
			}
			if c, ok := err.(*Cause); ok {
				var kinds []Kind
				for _, r := range c.Reasons {
					kinds = append(kinds, r.Kind)
				}
				if !slices.Equal(kinds, append(tc.want, KindInterruption)) {
					t.Fatalf("reasons %v, want %v then the interruption", kinds, tc.want)
				}
			} else if tc.want != nil {
				t.Fatalf("exit %v dropped the task's defect", err)
			}
			_ = endScope(&s)
		})
	}
}

func TestStopKeepsDefect(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	h := Go(&s, "crasher", func(task *Task) (int, error) {
		<-task.Done()
		panic("cleanup defect")
	})
	err := h.Stop()
	c, ok := err.(*Cause)
	if !ok || len(c.Reasons) != 1 || c.Reasons[0].Value != "cleanup defect" {
		t.Fatalf("Stop returned %v, want the defect alone", err)
	}
	if again := h.Stop(); again != nil {
		t.Fatalf("a second Stop returned %v", again)
	}
	_ = endScope(&s)
}

func TestStopWinsOverSuccess(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	ready, proceed := make(chan struct{}), make(chan struct{})
	h := Go(&s, "late", func(*Task) (int, error) {
		close(ready)
		<-proceed
		return 42, nil
	})
	<-ready
	h.Task().Interrupt()
	close(proceed)
	if v, err := h.Wait(); v != 0 || !errors.Is(err, Interrupted) {
		t.Fatalf("got %d, %v; want the interruption", v, err)
	}
	_ = endScope(&s)
}
