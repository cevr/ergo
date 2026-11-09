package v1

import (
	"reflect"
	"sync"
	"time"

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

// traceTake reports the envelope env taken in s: its signal, a timer's
// fire, or, when dropped, a stale signal.
//
//go:noinline
func (a *Actor[S, E, D]) traceTake(at *rt.Task, s S, env *envelope[S, E, D], dropped bool) {
	g := a.graphOf()
	m := &rt.MachineEvent{What: rt.MachineSignal, Epoch: a.epoch, Source: sourceNames[env.kind]}
	if env.kind == timerSignal {
		m.What, m.Signal = rt.MachineTimerFired, a.timerSignal(g, env.at)
	} else {
		m.Signal = variant(g, env.sig)
	}
	if dropped {
		// The stale signal's own epoch, not the open entry's.
		m.What, m.Epoch = rt.MachineDropped, env.epoch
	} else {
		m.State = variant(g, s)
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

// traceDecide reports the decision d (of kind k) of the step of env in s.
// Next is the state after it; the epoch is the entry's after it.
//
//go:noinline
func (a *Actor[S, E, D]) traceDecide(at *rt.Task, s S, env *envelope[S, E, D], d D, k int) {
	g := a.graphOf()
	m := &rt.MachineEvent{What: rt.MachineDecide, Epoch: a.epoch, State: variant(g, s), Decision: decisionNames[k]}
	if env.kind == timerSignal {
		m.Signal = a.timerSignal(g, env.at)
	} else {
		m.Signal = variant(g, env.sig)
	}
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

// traceFail reports a step failure in s.
//
//go:noinline
func (a *Actor[S, E, D]) traceFail(at *rt.Task, s S) {
	at.TraceMachine(&rt.MachineEvent{What: rt.MachineFail, Epoch: a.epoch, State: variant(a.graphOf(), s)})
}

// traceArmed reports the timer of the entry that opened in s, armed for d.
//
//go:noinline
func (a *Actor[S, E, D]) traceArmed(at *rt.Task, s S, d time.Duration) {
	g := a.graphOf()
	at.TraceMachine(&rt.MachineEvent{What: rt.MachineTimerArmed, Epoch: a.epoch, State: variant(g, s),
		Signal: a.timerSignal(g, s), In: d})
}

// traceWork reports what happened to the work of the entry epoch, opened
// in s, on the work's task wt.
//
//go:noinline
func (a *Actor[S, E, D]) traceWork(wt *rt.Task, what rt.MachineWhat, epoch uint64, s S) {
	wt.TraceMachine(&rt.MachineEvent{What: what, Epoch: epoch, State: variant(a.graphOf(), s)})
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

// traceChild reports a child of the entry epoch started or stopped.
//
//go:noinline
func (a *Actor[S, E, D]) traceChild(what rt.MachineWhat, epoch uint64) {
	a.task.TraceMachine(&rt.MachineEvent{What: what, Epoch: epoch})
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
