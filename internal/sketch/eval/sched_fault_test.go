package eval

import (
	"strings"
	"testing"
)

// TestTaskFaultKeepsItsStack: a runtime fault on a task's goroutine, which
// the root raises again when the schedule ends (shutdown), carries the
// stack of the goroutine where it happened; the raise alone named only
// the root's (fallout flip-g4: the jobs panic showed sched.go, not its
// site). Any other panic value is kept as it is, since its recoverers
// match it by type. Mutant sched-fault-stack-dropped.
func TestTaskFaultKeepsItsStack(t *testing.T) {
	var got any
	func() {
		defer func() { got = withTaskStack(recover()) }()
		faultingTaskBody()
	}()
	p, ok := got.(taskPanic)
	if !ok {
		t.Fatalf("the fault is raised as %T, want a taskPanic with its stack", got)
	}
	if !strings.Contains(p.Error(), "faultingTaskBody") || !strings.Contains(p.Error(), "nil map") {
		t.Fatalf("the raised fault does not name its site:\n%s", p.Error())
	}
	ie := internalError{msg: "an internal error"}
	if r := withTaskStack(ie); r != any(ie) {
		t.Fatalf("an internalError is raised as %T, want it as it is", r)
	}
}

func faultingTaskBody() {
	var m map[string]int
	m["x"] = 1
}
