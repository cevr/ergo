// Package sched holds the strategies of the schedule explorer (runtime.md,
// Explored schedules; stdlib.md, Testing): bounded depth-first search with
// partial-order reduction (DFS), PCT, a random walk and replay. A strategy
// is a Picker: at each scheduling point of a run it picks the next task
// among the enabled ones. An Executor runs one schedule; the explored test
// binary is one (a worker process, wire.go), and the package's tests use a
// value executor in process.
//
// The package knows the runtime's operations by their words (rt.OpKind's
// String), not by rt's types, so it builds without the ergoexplore tag and
// the command line links it without the runtime.
//
// Prior art: Loom's DPOR (after CDSChecker) and its preemption bound;
// Concuerror's source sets and sleep sets on the BEAM; Coons, Musuvathi
// and McKinley's bounded partial-order reduction (OOPSLA 2013) for a sound
// bound; Burckhardt et al.'s PCT (ASPLOS 2010); shuttle's schedule string
// for replay. Flanagan and Godefroid (POPL 2005) give the race rule with
// happens-before that DFS uses.
package sched

import "strconv"

// Step is an enabled task and the operation it does when it is picked:
// the task's spawn path, the operation's word ("push", "pop", ...) and the
// number of the handle it touches, in the order the run first met each
// handle (0 is none).
type Step struct {
	Task   string `json:"task"`
	Kind   string `json:"kind"`
	Handle int    `json:"handle"`
}

// String is the step in words: "task 0.1: push on #2".
func (s Step) String() string {
	out := "task " + s.Task + ": " + s.Kind
	if s.Handle != 0 {
		out += " on #" + strconv.Itoa(s.Handle)
	}
	return out
}

// Choice is one scheduling point: the enabled tasks in the order they
// were started, the index among them of the task that reached the point
// (-1 when it blocked or ended: a pick of another task is then no
// preemption), the number of picks before this one, and the bytes the run
// had written to its output before the point (a worker's report uses it
// to find the test that was running).
type Choice struct {
	Enabled []Step `json:"enabled"`
	Current int    `json:"current"`
	Step    int    `json:"step"`
	Out     int64  `json:"out,omitempty"`
}

// Picker picks the next task at each scheduling point: the index in
// c.Enabled. An error stops the run there (a replay that left its
// schedule).
type Picker interface {
	Pick(c Choice) (int, error)
}

// Outcome is what one run did.
type Outcome struct {
	// Failure is the run's failure in words: a failed test, a defect, a
	// deadlock, a step outside the scheduler; "" when the run passed.
	Failure string `json:"failure,omitempty"`
	// Deadlock holds the tasks that had not ended when none could run,
	// each with the operation it waits to do.
	Deadlock []Step `json:"deadlock,omitempty"`
	// Stopped is the picker's error that stopped the run.
	Stopped string `json:"stopped,omitempty"`
	// Value is the run's observable result, compared between runs (the
	// value executor's final state; a worker's output).
	Value string `json:"value,omitempty"`
	// Abandoned reports a run that left a task that had not ended (a
	// stop, a deadlock, an escape): its worker is not reused.
	Abandoned bool `json:"abandoned,omitempty"`
}

// Executor runs one schedule, asking p at each scheduling point.
type Executor interface {
	Run(p Picker) Outcome
}

// The operation words of rt.OpKind, and how each depends on the others.
// A barrier is dependent with every step: its effect is not one handle's
// (a task's end, a spawn, a join, a stop, a sleep, a clock handshake, the
// resume after a wake, whose wait source is not always the handle its
// point named). A change (push, pop, close, write) is dependent with every
// step on its handle; a read with every change on its handle. A task's
// start is local: the new task runs its own code up to its first point,
// so it is dependent only with barriers (the spawn that enabled it is
// one). TestDependencyTableTotal keeps the table equal to rt's list.
const (
	barrier = iota + 1
	change
	read
	local
)

var kinds = map[string]int{
	"start": local, "resume": barrier, "end": barrier, "spawn": barrier,
	"join": barrier, "stop": barrier, "sleep": barrier, "await": barrier,
	"push": change, "pop": change, "close": change, "write": change,
	"read": read,
}

// Dependent reports whether the order of a and b, steps of two tasks, can
// show: swapping them may change a value, an outcome or which task is
// enabled. A step of an unknown kind, or with no handle, is dependent with
// every step.
func Dependent(a, b Step) bool {
	if a.Task == b.Task {
		return true
	}
	ka, kb := kinds[a.Kind], kinds[b.Kind]
	if ka == 0 || kb == 0 || ka == barrier || kb == barrier || a.Handle == 0 || b.Handle == 0 {
		return true
	}
	if ka == local || kb == local || a.Handle != b.Handle {
		return false
	}
	return ka == change || kb == change
}
