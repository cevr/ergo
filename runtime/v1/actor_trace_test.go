package v1

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// The machine events (actor_trace.go, MU15) against the reference: the
// model-based property TestActorModelAgreesWithTimedFold runs each case on
// a traced root, and the reference fold (ref, actor_test.go) writes the
// events the runner must raise, each decision annotated with the arm that
// the reference finds in the generated machine's arms. The arm the runner
// reports comes from the registered graph (MachineGraph.arm), a different
// reading of the same arms: over strings, where the reference reads ints.

// machineLog is a Tracer that keeps the machine events of a tree, in the
// order the runtime raised them (their occurrence IDs).
type machineLog struct {
	mu  sync.Mutex
	evs []rt.Event
}

func (l *machineLog) Event(e rt.Event) {
	if e.Kind != rt.TraceMachine {
		return
	}
	l.mu.Lock()
	l.evs = append(l.evs, e)
	l.mu.Unlock()
}

// diff is "" when the log's events are want, in order, and otherwise both
// lists from the first difference.
func (l *machineLog) diff(want []rt.MachineEvent) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	got := make([]rt.MachineEvent, len(l.evs))
	for i, e := range l.evs {
		got[i] = *e.Machine()
	}
	// Raised from more than one goroutine (a Stop's), so order by ID.
	ids := make([]uint64, len(l.evs))
	for i, e := range l.evs {
		ids[i] = e.ID
	}
	for i := 1; i < len(got); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
			got[j], got[j-1] = got[j-1], got[j]
		}
	}
	n := 0
	for n < len(got) && n < len(want) && got[n] == want[n] {
		n++
	}
	if n == len(got) && n == len(want) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "first difference at event %d\n  got:\n", n)
	for _, e := range got[n:] {
		b.WriteString("    " + e.String() + "\n")
	}
	b.WriteString("  want:\n")
	for _, e := range want[n:] {
		b.WriteString("    " + e.String() + "\n")
	}
	return b.String()
}

// stateVariant and sigVariant name a generated machine's variants: a
// state's v is its variant, as a signal's is; the gate is its own.
func stateVariant(s tstate) string { return "S" + strconv.Itoa(s.v) }

func sigVariant(e tsig) string {
	if e.v == gate {
		return "gate"
	}
	return "E" + strconv.Itoa(e.v)
}

// registerTestGraph registers m's graph for the next traced run: its arms
// as the compiler would read them, and its timers' signal variants. Each
// case replaces the last (the generated machines share their Go types).
func registerTestGraph(m tmachine) {
	g := &MachineGraph{Timers: map[string]string{}, Variant: func(v any) string {
		switch x := v.(type) {
		case tstate:
			return stateVariant(x)
		case tsig:
			return sigVariant(x)
		}
		return ""
	}}
	for i, a := range m.arms {
		arm := MachineArm{Site: armSite(i)}
		if a.state >= 0 {
			arm.States = []string{stateVariant(tstate{v: a.state})}
		}
		if a.sig >= 0 {
			arm.Signals = []string{sigVariant(tsig{v: a.sig})}
		}
		g.Arms = append(g.Arms, arm)
	}
	for v, tm := range m.timers {
		if tm.has {
			g.Timers[stateVariant(tstate{v: v})] = sigVariant(tsig{v: tm.sig})
		}
	}
	graphs.Store(graphKey{reflect.TypeFor[tstate](), reflect.TypeFor[tsig]()}, g)
}

// armSite is the site of a generated machine's i-th arm.
func armSite(i int) string { return "gen.ergo:" + strconv.Itoa(10+i) }

// armOf is the site of the first of the machine's arms that takes the
// pair (s, e), "" when none does.
func (r *ref) armOf(s tstate, e tsig) string {
	for i, a := range r.m.arms {
		if (a.state < 0 || a.state == s.v) && (a.sig < 0 || a.sig == e.v) {
			return armSite(i)
		}
	}
	return ""
}

// take records the runner's take of e in the open entry: a timer's fire,
// or a signal from an asker or a sender.
func (r *ref) take(e tsig, timer, asked bool) {
	m := rt.MachineEvent{What: rt.MachineSignal, Epoch: r.epoch, Source: "send", State: stateVariant(r.s), Signal: sigVariant(e)}
	switch {
	case timer:
		m.What, m.Source = rt.MachineTimerFired, "timer"
	case asked:
		m.Source = "ask"
	}
	r.events = append(r.events, m)
}

// decided records the decision d of e in r.s, before r commits it.
func (r *ref) decided(e tsig, d tdec) {
	m := rt.MachineEvent{What: rt.MachineDecide, Epoch: r.epoch, State: stateVariant(r.s), Signal: sigVariant(e),
		Decision: decisionNames[d.kind], Arm: r.armOf(r.s, e)}
	switch d.kind {
	case decideEnter, decideEmit:
		m.Epoch++
		m.Next = stateVariant(d.next)
	case decideStay:
		m.Next = stateVariant(d.next)
	}
	r.events = append(r.events, m)
}

// exit records the actor's end, how, in r.s.
func (r *ref) exit(how string) {
	r.events = append(r.events, rt.MachineEvent{What: rt.MachineExit, Exit: how, State: stateVariant(r.s)})
}
