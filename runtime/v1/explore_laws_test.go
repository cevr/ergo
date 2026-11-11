//go:build ergoexplore

package v1

import (
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestChildLawsReadEvents gives the law reader the smallest event stream
// that breaks each law, and checks the law it names: the runner mutants
// show only that some law breaks, and a correct runner breaks none, so
// only these rows see a reader that misses one (the x7-law-* mutants).
// Task 1 is an actor P, task 2 its entry 0's work, tasks 3 and 4 its
// children, in start order.
func TestChildLawsReadEvents(t *testing.T) {
	start := func(task, parent uint64) rt.Event { return rt.Event{Kind: rt.TraceStart, Task: task, Parent: parent} }
	mach := func(task uint64, what rt.MachineWhat, epoch uint64, f ...string) rt.Event {
		m := &rt.MachineEvent{What: what, Epoch: epoch}
		if len(f) > 0 {
			m.Source = f[0]
		}
		if len(f) > 1 {
			m.Decision = f[1]
		}
		return rt.Event{Kind: rt.TraceMachine, Task: task, Defect: m}
	}
	// seq is the events of each part, in order, in a new slice.
	seq := func(parts ...[]rt.Event) []rt.Event {
		var out []rt.Event
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	one := func(e ...rt.Event) []rt.Event { return e }
	actor := func(task, parent uint64) []rt.Event {
		return one(start(task, parent), mach(task, rt.MachineStart, 0))
	}
	p := seq(actor(1, 0), one(start(2, 1), mach(2, rt.MachineWorkStarted, 0)))
	// P with its entry 0's work and one child.
	tree := seq(p, actor(3, 2))
	reopen := one(mach(1, rt.MachineDecide, 1, "", "Enter"), mach(1, rt.MachineSignal, 1, "send"))
	rows := []struct {
		name, law string
		events    []rt.Event
	}{
		{"an output with no child output given", "NoLateChildOutput", seq(tree, one(mach(1, rt.MachineSignal, 0, "child")))},
		{"the next entry opens while a child lives", "ChildInsideEntry", seq(tree, reopen)},
		{"the work is stopped while a child lives", "ChildInsideEntry", seq(tree, one(mach(2, rt.MachineWorkInterrupted, 0)))},
		{"a child starts after its entry closed", "ChildInsideEntry", seq(p, reopen, actor(3, 2))},
		{"the first child ends before the second", "ChildrenStopFirst",
			seq(tree, actor(4, 2), one(mach(1, rt.MachineStop, 0, "stop"), mach(3, rt.MachineExit, 0)))},
		{"the parent ends before its child", "ChildrenStopFirst", seq(tree, one(mach(1, rt.MachineExit, 0)))},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			l := newChildLaws()
			for _, e := range r.events {
				l.Event(e)
			}
			if got := l.verdict(); !strings.Contains(got, "law "+r.law+" broken") {
				t.Fatalf("verdict %q, want law %s broken", got, r.law)
			}
		})
	}
}
