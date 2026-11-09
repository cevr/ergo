package rt

import (
	"strconv"
	"time"
)

// MachineWhat is what a machine event reports (TraceMachine; runtime.md,
// Machines and actors 15). The runner of runtime/v1 raises each one at one
// point of its contract.
type MachineWhat uint8

const (
	// MachineStart: an incarnation of the actor began in State, epoch 0.
	MachineStart MachineWhat = iota + 1
	// MachineSignal: the runner took Signal from the inbox to step it;
	// Source is send, ask, work or child.
	MachineSignal
	// MachineTimerFired: the runner took the timer of the open entry
	// (Epoch) to step it; Signal is the timer's signal.
	MachineTimerFired
	// MachineDropped: the runner dropped a signal of an ended entry
	// (Epoch is the signal's, the stale one); Source is timer, work or
	// child.
	MachineDropped
	// MachineDecide: the step decided Decision in State at Arm; Next is
	// the state after it and Epoch the entry's after it.
	MachineDecide
	// MachineFail: the step failed in State.
	MachineFail
	// MachineTimerArmed: the entry Epoch, opened in State, armed its timer
	// for In.
	MachineTimerArmed
	// MachineWorkStarted, MachineWorkEnded, MachineWorkFailed and
	// MachineWorkInterrupted: the work of the entry Epoch, opened in
	// State, started; ended by returning; ended with a defect; or was
	// interrupted by the end of its entry or of the actor.
	MachineWorkStarted
	MachineWorkEnded
	MachineWorkFailed
	MachineWorkInterrupted
	// MachineChildStarted and MachineChildStopped: the entry Epoch started
	// a child, or its end stopped one.
	MachineChildStarted
	MachineChildStopped
	// MachineStop: Stop (Source stop), Drain (Source drain) or the owner
	// entry's end (Source parent) asked the actor to end.
	MachineStop
	// MachineExit: the actor ended; Exit is finished, halted, failed or
	// defected, and State its last state.
	MachineExit
)

var machineNames = [...]string{
	MachineStart:           "start",
	MachineSignal:          "signal",
	MachineTimerFired:      "timer-fired",
	MachineDropped:         "dropped",
	MachineDecide:          "decide",
	MachineFail:            "fail",
	MachineTimerArmed:      "timer-armed",
	MachineWorkStarted:     "work-started",
	MachineWorkEnded:       "work-ended",
	MachineWorkFailed:      "work-failed",
	MachineWorkInterrupted: "work-interrupted",
	MachineChildStarted:    "child-started",
	MachineChildStopped:    "child-stopped",
	MachineStop:            "stop",
	MachineExit:            "exit",
}

// String is the event's name.
func (w MachineWhat) String() string {
	if int(w) < len(machineNames) && machineNames[w] != "" {
		return machineNames[w]
	}
	return "unknown"
}

// MachineEvent is what a machine event says. Like every trace event it
// names identities, never values: states and signals by their variant
// (the Go type of a union's value), the step's arm by its .ergo position.
// An empty field does not apply.
type MachineEvent struct {
	What        MachineWhat
	Incarnation int
	Epoch       uint64
	State       string
	Signal      string
	Next        string
	Decision    string
	// Arm is the .ergo position (file:line) of the step's arm that took
	// the signal, read from the machine's graph; "" when the program
	// registered no graph for the machine.
	Arm    string
	Source string
	In     time.Duration
	Exit   string
}

// String is the event in one line: its name, then each field that
// applies, as key=value.
func (m *MachineEvent) String() string {
	b := []byte(m.What.String())
	add := func(k, v string) {
		if v != "" {
			b = append(b, ' ')
			b = append(b, k...)
			b = append(b, '=')
			b = append(b, v...)
		}
	}
	if m.What == MachineStart {
		add("incarnation", strconv.Itoa(m.Incarnation))
	}
	if m.What != MachineStop && m.What != MachineExit {
		add("epoch", strconv.FormatUint(m.Epoch, 10))
	}
	add("source", m.Source)
	add("state", m.State)
	add("signal", m.Signal)
	add("decision", m.Decision)
	add("arm", m.Arm)
	add("next", m.Next)
	if m.What == MachineTimerArmed {
		add("in", m.In.String())
	}
	add("exit", m.Exit)
	return string(b)
}

// TraceMachine raises the machine event m of t, the actor's task, when
// t's tree is traced. The runner calls it only after it checked Traced,
// so an untraced actor builds no MachineEvent.
//
//go:noinline
func (t *Task) TraceMachine(m *MachineEvent) {
	if t.trace != nil {
		t.trace.raise(t, nil, Event{Kind: TraceMachine, Def: t.span, Defect: m})
	}
}
