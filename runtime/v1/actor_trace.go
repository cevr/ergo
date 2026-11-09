package v1

import (
	"reflect"
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// Machine events (runtime.md, Machines and actors 15; MU15). A traced
// actor reports each step of the runner contract to its tree's Tracer as
// an rt.TraceMachine event: start, each signal taken (a timer's as
// timer-fired), each stale signal dropped, each decision with the step's
// arm, each step failure, each timer armed, each work started, ended,
// failed or interrupted, each child started and stopped, each stop asked,
// and the exit. Each event has one hook in actor.go, written
//
//	if at.Traced() {
//		a.traceX(…)
//	}
//
// so an untraced actor pays one load and compare per hook and builds no
// event; the helpers are never inlined, so the runner's frames do not grow.
//
// The arm is not observed: Go cannot tell which return of the step gave
// its decision. It is read from the machine's graph, the step's arms by
// state and signal variant, which the compiler derives from the step's
// match (internal/sketch/machinegraph) and registers with the program, as
// it registers the source map (RegisterCalls). An arm of a timer step is
// found with the signal variant the graph gives the timer of the state the
// entry opened in.

// MachineArm is one arm of a machine's step, as the compiler read it: its
// .ergo position (file:line) and the state and signal variants it takes,
// nil for any. A partial arm's patterns test more than the variants (a
// field or a literal), so a pair it takes may pass on to a later arm.
type MachineArm struct {
	Site    string
	States  []string
	Signals []string
	Partial bool
}

// MachineGraph is what the traced runner reads of a machine's graph: its
// step's arms, in match order (the first arm that takes a pair is the one
// that steps it), and the signal variant of each state's timer, by state
// variant. Variant names a state's or a signal's variant; nil names it by
// its Go type (VariantOf).
type MachineGraph struct {
	Arms    []MachineArm
	Timers  map[string]string
	Variant func(v any) string
}

// graphKey is a machine's state and signal types: the key a runner finds
// its machine's graph by.
type graphKey struct{ state, signal reflect.Type }

// graphs holds each registered graph by its key; a key that two machines
// share holds nil, since the runner could not tell their arms apart.
var graphs sync.Map // graphKey -> *MachineGraph

// RegisterMachine registers g as the graph of the machine whose states
// are S and signals E. Emitted code calls it once per machine before main
// runs. A second machine with the same S and E makes both arm-less.
func RegisterMachine[S, E any](g MachineGraph) {
	k := graphKey{reflect.TypeFor[S](), reflect.TypeFor[E]()}
	if _, loaded := graphs.LoadOrStore(k, &g); loaded {
		graphs.Store(k, (*MachineGraph)(nil))
	}
}

// VariantOf is the variant name of a value of the Go type T, as a runner
// names one it holds: the type's name.
func VariantOf[T any]() string { return reflect.TypeFor[T]().Name() }

// graphOf is the registered graph of a, nil when none is.
func (a *Actor[S, E, D]) graphOf() *MachineGraph {
	g, _ := graphs.Load(graphKey{reflect.TypeFor[S](), reflect.TypeFor[E]()})
	gr, _ := g.(*MachineGraph)
	return gr
}

// variant is the variant name of v: g's Variant, else its Go type's name.
func variant(g *MachineGraph, v any) string {
	if g != nil && g.Variant != nil {
		return g.Variant(v)
	}
	if t := reflect.TypeOf(v); t != nil {
		return t.Name()
	}
	return ""
}

// arm is the site of g's first arm that takes the pair (state, signal),
// "" when g is nil or no arm does. After a partial arm it names each arm
// that may have taken the pair, up to the first whole one: "a:3 or a:5".
func (g *MachineGraph) arm(state, signal string) string {
	if g == nil {
		return ""
	}
	site := ""
	for _, a := range g.Arms {
		if takes(a.States, state) && takes(a.Signals, signal) {
			if site != "" {
				site += " or "
			}
			site += a.Site
			if !a.Partial {
				break
			}
		}
	}
	return site
}

// takes reports whether the variants vs (nil for any) hold v.
func takes(vs []string, v string) bool {
	if vs == nil {
		return true
	}
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

// decisionNames are the prelude Decision's variants, in kind order.
var decisionNames = [...]string{decideEnter: "Enter", decideStay: "Stay", decideEmit: "Emit",
	decideIgnore: "Ignore", decideReject: "Reject", decideDone: "Done"}

// sourceNames name an envelope's kind as a signal's source.
var sourceNames = [...]string{sendSignal: "send", askSignal: "ask", timerSignal: "timer",
	workSignal: "work", childSignal: "child"}

// traceStart reports the start of the actor in s.
//
//go:noinline
func (a *Actor[S, E, D]) traceStart(at *rt.Task, s S) {
	g := a.graphOf()
	at.TraceMachine(&rt.MachineEvent{What: rt.MachineStart, Incarnation: 1, Epoch: a.epoch, State: variant(g, s)})
}

// signalOf is the variant of the signal of env: a timer's from g's timers.
func (a *Actor[S, E, D]) signalOf(g *MachineGraph, env *envelope[S, E, D]) string {
	if env.kind == timerSignal {
		return a.timerSignal(g, env.at)
	}
	return variant(g, env.sig)
}

// drop drops the stale envelope in a step (a.cur) and counts it (rule
// 6); traced, it reports it with its own epoch, not the open entry's. It
// is a call of its own, so handle's frame does not grow (MU18's 2 KB
// runner stack).
//
//go:noinline
func (a *Actor[S, E, D]) drop(at *rt.Task) {
	a.dropped.Add(1)
	if at.Traced() {
		env := a.cur
		at.TraceMachine(&rt.MachineEvent{What: rt.MachineDropped, Epoch: env.epoch, Source: sourceNames[env.kind],
			Signal: a.signalOf(a.graphOf(), env)})
	}
}

// traceTake reports the envelope in a step (a.cur), taken in s: its
// signal, or a timer's fire.
//
//go:noinline
func (a *Actor[S, E, D]) traceTake(at *rt.Task, s S) {
	env := a.cur
	g := a.graphOf()
	m := &rt.MachineEvent{What: rt.MachineSignal, Epoch: a.epoch, Source: sourceNames[env.kind],
		State: variant(g, s), Signal: a.signalOf(g, env)}
	if env.kind == timerSignal {
		m.What = rt.MachineTimerFired
	}
	at.TraceMachine(m)
}

// timerSignal is the variant of the signal of the timer of an entry that
// opened in the state at, from g's timers.
func (a *Actor[S, E, D]) timerSignal(g *MachineGraph, at S) string {
	if g == nil {
		return ""
	}
	return g.Timers[variant(g, at)]
}

// traceStep reports the step of the envelope in a step (a.cur) in s: its
// failure err, or its decision d, with the arm, the state after it (Next)
// and the entry's epoch after it.
//
//go:noinline
func (a *Actor[S, E, D]) traceStep(at *rt.Task, s S, d D, err error) {
	g := a.graphOf()
	if err != nil {
		at.TraceMachine(&rt.MachineEvent{What: rt.MachineFail, Epoch: a.epoch, State: variant(g, s)})
		return
	}
	k := a.kind(d)
	m := &rt.MachineEvent{What: rt.MachineDecide, Epoch: a.epoch, State: variant(g, s), Decision: decisionNames[k],
		Signal: a.signalOf(g, a.cur)}
	m.Arm = g.arm(m.State, m.Signal)
	switch k {
	case decideEnter, decideEmit:
		m.Epoch++
		m.Next = variant(g, a.next(s, d))
	case decideStay:
		m.Next = variant(g, a.next(s, d))
	}
	at.TraceMachine(m)
}

// traceArmed reports the timer of the entry that opened in s, when it has
// one. The runner raises it once the entry's timer is armed and its work
// started, from open and loop: a hook in arm or startWork would grow
// their frames, and the runner's stack with them (MU18's 2 KB budget).
//
//go:noinline
func (a *Actor[S, E, D]) traceArmed(at *rt.Task, s S) {
	d := a.delay(s)
	if d == noTimer {
		return
	}
	g := a.graphOf()
	at.TraceMachine(&rt.MachineEvent{What: rt.MachineTimerArmed, Epoch: a.epoch, State: variant(g, s),
		Signal: a.timerSignal(g, s), In: d})
}

// traceWork reports what happened to the work of the entry epoch, on the
// work's task wt. It names no state: the work's closure would have to
// hold one, and the entry's epoch already names it (its decide or start
// event gives the state).
//
//go:noinline
func (a *Actor[S, E, D]) traceWork(wt *rt.Task, what rt.MachineWhat, epoch uint64) {
	wt.TraceMachine(&rt.MachineEvent{What: what, Epoch: epoch})
}

// workEnd is the event of a work's end with err: returned, interrupted,
// or a defect.
func workEnd(err error) rt.MachineWhat {
	switch err {
	case nil:
		return rt.MachineWorkEnded
	case rt.Interrupted:
		return rt.MachineWorkInterrupted
	}
	return rt.MachineWorkFailed
}

// traceChild reports a child of the entry epoch started, and gives its
// halt, which reports it stopped once it ended.
//
//go:noinline
func (a *Actor[S, E, D]) traceChild(halt func() error, epoch uint64) func() error {
	a.task.TraceMachine(&rt.MachineEvent{What: rt.MachineChildStarted, Epoch: epoch})
	return func() error {
		err := halt()
		a.task.TraceMachine(&rt.MachineEvent{What: rt.MachineChildStopped, Epoch: epoch})
		return err
	}
}

// traceStop reports a stop asked of the actor by source.
//
//go:noinline
func (a *Actor[S, E, D]) traceStop(source string) {
	a.task.TraceMachine(&rt.MachineEvent{What: rt.MachineStop, Source: source})
}

// traceExit reports the actor's end: how it ended and its last state.
//
//go:noinline
func (a *Actor[S, E, D]) traceExit() {
	a.mu.Lock()
	kind, s := a.exitKindLocked(), a.state
	a.mu.Unlock()
	names := [...]string{exitFinished: "finished", exitHalted: "halted", exitFailed: "failed", exitDefected: "defected"}
	a.task.TraceMachine(&rt.MachineEvent{What: rt.MachineExit, Exit: names[kind], State: variant(a.graphOf(), s)})
}
