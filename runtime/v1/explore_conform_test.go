//go:build ergoexplore

package v1

import (
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestChildrenConformReadsEvents feeds the smallest event streams breaking each
// models/children law, plus clean traces, stale child drops, and an unmapped
// rt.MachineWhat event, verifying every violation produces a conformance
// mismatch naming the models/children law (and never silently ignores unmapped
// events or empty candidate sets).
func TestChildrenConformReadsEvents(t *testing.T) {
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
	tree := seq(p, actor(3, 2))
	reopen := one(mach(1, rt.MachineDecide, 1, "", "Enter"), mach(1, rt.MachineSignal, 1, "send"))

	t.Run("valid-trace-with-stale-drop-conforms", func(t *testing.T) {
		valid := seq(
			tree,
			// Child 3 emits in epoch 0, parent re-enters, child stops, parent opens epoch 1 and drops stale child output.
			one(
				mach(3, rt.MachineDecide, 0, "", "Emit"),
				mach(1, rt.MachineDecide, 1, "", "Enter"),
				mach(3, rt.MachineStop, 0, "parent"),
				mach(3, rt.MachineExit, 0),
				mach(2, rt.MachineWorkInterrupted, 0),
				mach(1, rt.MachineSignal, 1, "send"),
				mach(1, rt.MachineDropped, 0, "child"),
				mach(1, rt.MachineStop, 1, "stop"),
				mach(1, rt.MachineExit, 1),
			),
		)
		c := newChildrenConformer()
		for _, e := range valid {
			c.Event(e)
		}
		if got := c.verdict(); got != "" {
			t.Fatalf("valid trace got mismatch %q, want empty", got)
		}
	})

	rows := []struct {
		name, law string
		events    []rt.Event
	}{
		{"an output with no child output given", "NoLateChildOutput", seq(tree, one(mach(1, rt.MachineSignal, 0, "child")))},
		{"stale child output stepped after re-entry", "NoLateChildOutput", seq(
			tree,
			one(
				mach(3, rt.MachineDecide, 0, "", "Emit"),
				mach(1, rt.MachineDecide, 1, "", "Enter"),
				mach(3, rt.MachineStop, 0, "parent"),
				mach(3, rt.MachineExit, 0),
				mach(2, rt.MachineWorkInterrupted, 0),
				mach(1, rt.MachineSignal, 1, "child"),
			),
		)},
		{"the next entry opens while a child lives", "ChildInsideEntry", seq(tree, reopen)},
		{"the work is stopped while a child lives", "ChildInsideEntry", seq(tree, one(mach(2, rt.MachineWorkInterrupted, 0)))},
		{"a child starts after its entry closed", "ChildInsideEntry", seq(p, reopen, actor(3, 2))},
		{"the first child ends before the second", "ChildrenStopFirst",
			seq(tree, actor(4, 2), one(mach(1, rt.MachineStop, 0, "stop"), mach(3, rt.MachineExit, 0)))},
		{"the parent ends before its child", "ChildrenStopFirst", seq(tree, one(mach(1, rt.MachineExit, 0)))},
		{"unmapped machine event is never ignored", "UnmappedEvent", seq(tree, one(mach(1, rt.MachineWhat(99), 0)))},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			c := newChildrenConformer()
			for _, e := range r.events {
				c.Event(e)
			}
			got := c.verdict()
			if !strings.Contains(got, "models/children conformance mismatch") || !strings.Contains(got, "law "+r.law+" broken") {
				t.Fatalf("verdict %q, want models/children conformance mismatch naming law %s", got, r.law)
			}
		})
	}
}
