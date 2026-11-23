package rt

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// TestSecondWaiterIsADefect: a Task has one owner, so one waiter
// (runtime.md Tasks, rule 11); checked code never makes a second, and a
// second task that waits on a handle another task already waits on is a
// defect, not a silent second park on the same end. The handle ends on
// its own once the second waiter has started, so a second wait that is
// admitted returns (and the test fails) instead of parking forever. Mutant
// stop-claim-always-first (claim reports every waiter first).
func TestSecondWaiterIsADefect(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	release := make(chan struct{})
	h := Go(&s, "held", func(task *Task) (int, error) {
		select {
		case <-release:
			return 0, nil
		case <-task.Done():
			return 0, Interrupted
		}
	})
	claimed := func() bool { return h.task.stopped.v.Load()&flagClaimed != 0 }
	first := Go(&s, "first waiter", func(task *Task) (int, error) { return h.WaitBy(task) })
	if Exploring {
		Await(root, claimed)
	}
	if !testwait.Until(t, claimed) {
		t.Fatal("the first waiter never claimed the handle")
	}
	var raised any
	second := Go(&s, "second waiter", func(task *Task) (int, error) {
		defer func() { raised = recover() }()
		return h.WaitBy(task)
	})
	close(release)
	if _, err := second.WaitBy(root); err != nil {
		t.Errorf("the second waiter's task: %v", err)
	}
	if msg, _ := raised.(string); !strings.Contains(msg, "a second task waits") {
		// An admitted second waiter took the wake from the first, which would
		// now park forever: stop here.
		t.Fatalf("a second waiter: recovered %v, want the second-waiter defect", raised)
	}
	if _, err := first.WaitBy(root); err != nil {
		t.Errorf("the first waiter: %v", err)
	}
	_ = endScope(&s)
}
