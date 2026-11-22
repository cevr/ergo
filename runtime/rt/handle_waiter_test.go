package rt

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// TestSecondWaiterIsADefect: a Task has one owner, so one waiter
// (runtime.md Tasks, rule 11); checked code never makes a second, and a
// second task that waits on a handle another task already waits on is a
// defect, not a silent second park on the same end. The first waiter
// still ends on its own stop. Mutant stop-claim-always-first (claim
// reports every waiter first).
func TestSecondWaiterIsADefect(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	h := Go(&s, "held", func(task *Task) (int, error) {
		<-task.Done()
		return 0, Interrupted
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
	func() {
		defer func() { raised = recover() }()
		_, _ = h.WaitBy(root)
	}()
	if msg, _ := raised.(string); !strings.Contains(msg, "a second task waits") {
		t.Errorf("a second waiter: recovered %v, want the second-waiter defect", raised)
	}
	if err := first.Stop(); err != nil {
		t.Errorf("stopping the first waiter: %v", err)
	}
	_ = endScope(&s)
}
