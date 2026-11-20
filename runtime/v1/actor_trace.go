package v1

import (
	"strings"
	"sync"
	"unsafe"

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
// match (internal/sketch/machinegraph) and hands the runner as the hidden
// graph argument of its start (GraphOf; binsize BS7): no registry, and no
// work before main. An arm of a timer step is found with the signal
// variant the graph gives the timer of the state the entry opened in.

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

// MachineTimer is the signal variant of the timer of a state variant.
type MachineTimer struct {
	State, Signal string
}

// MachineGraph is what the traced runner reads of a machine's graph: its
// step's arms, in match order (the first arm that takes a pair is the one
// that steps it), and the signal variant of each state's timer. Its lists
// are slices, not a map, so a program holds a graph as static data that
// the Go linker lays out and no initializer builds. Variant names a
// state's or a signal's variant; nil names it by its Go type, as VariantOf
// does.
type MachineGraph struct {
	Arms    []MachineArm
	Timers  []MachineTimer
	Variant func(v any) string
}

// GraphOf is the hidden graph argument of a machine's start
// (docs/spec/stdlib.md, Hidden arguments): it gives the graph of the
// machine whose states and signals are the start's S and E, nil when the
// program has none for them. The runner calls it only when it traces.
type GraphOf func() *MachineGraph

// graphOf is a's graph, nil when it has none.
func (a *Actor[S, E, D]) graphOf() *MachineGraph {
	if a.graph == nil {
		return nil
	}
	return a.graph()
}

// The reference registry (lower's graphLowering "registry"): the
// lowering before BS7, kept selectable so a property holds the static
// graphs equal to it. A program lowered by default never names it, so the
// Go linker keeps none of it.

// graphKey is a machine's state and signal types: the key a registered
// graph is found by.
type graphKey struct{ state, signal unsafe.Pointer }

// graphs holds each registered graph by its key; a key that two machines
// share holds nil, since the runner could not tell their arms apart.
var graphs sync.Map // graphKey -> *MachineGraph

// RegisterMachine registers g as the graph of the machine whose states
// are S and signals E. The reference lowering calls it once per machine
// before main runs. A second machine with the same S and E makes both
// arm-less.
func RegisterMachine[S, E any](g MachineGraph) {
	k := graphKey{typeID[S](), typeID[E]()}
	if _, loaded := graphs.LoadOrStore(k, &g); loaded {
		graphs.Store(k, (*MachineGraph)(nil))
	}
}

// RegisteredGraph is the registered graph of the machine whose states are
// S and signals E, nil when none or two are: the reference lowering's
// hidden graph argument.
func RegisteredGraph[S, E any]() *MachineGraph {
	g, _ := graphs.Load(graphKey{typeID[S](), typeID[E]()})
	gr, _ := g.(*MachineGraph)
	return gr
}

// VariantOf is the variant name of a value of the Go type T, as a runner
// names one it holds: the type's name, under its Ergo names
// (ErgoTypeName). The reference lowering names a graph's variants with it
// at start-up; the default lowering writes the same names as constants. A
// generic flat union's ErgoVariant reads it lazily, on the trace path
// (binsize BS8): the variant's type arguments are the instance's, so no
// constant can spell them.
func VariantOf[T any]() string {
	var p *T
	return ErgoTypeName(rt.TypeName(strings.TrimPrefix(rt.TypeString(p), "*")), typePkg, typeNames)
}

// typeNames maps the Go name of each type of the program that the lowering
// suffixed to keep a Go name unique (Idle_1 beside the root's Idle) to its
// Ergo name, and typePkg is the Go package that declares them: the table
// a program that starts a machine actor registers (RegisterTypeNames), so
// a traced runner never names a variant by a suffixed Go name
// (TRACE-VARIANT-SUFFIX). Written once, by the program's init.
var (
	typeNames map[string]string
	typePkg   string
)

// RegisterTypeNames registers the program's suffixed type names: pairs
// holds each Go name and then its Ergo name; pkg is the import path of
// the Go package that declares them, as Go's type strings qualify a name
// (main for a program).
func RegisterTypeNames(pkg string, pairs ...string) {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	typePkg, typeNames = pkg, m
}

// ErgoTypeName is s, a type's name as reflect's Type.Name spells it
// (Box_1[main.Idle_1]), with each name names maps, the head and each type
// argument the package of import path pkg declares, spelled as its Ergo
// name (Box[main.Idle]). The
// lowering writes a graph's names through it with the table it registers,
// so a name it writes and one the runtime reads agree.
func ErgoTypeName(s, pkg string, names map[string]string) string {
	if len(names) == 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && identByte(s[j]) {
			j++
		}
		if j == i {
			b.WriteByte(s[i])
			i++
			continue
		}
		tok := s[i:j]
		q := i - 1 - len(pkg) // where a qualifier pkg. would start
		qualified := q >= 0 && s[i-1] == '.' && s[q:i-1] == pkg && (q == 0 || !pathByte(s[q-1]))
		if n, ok := names[tok]; ok && (i == 0 || qualified) {
			tok = n
		}
		b.WriteString(tok)
		i = j
	}
	return b.String()
}

// pathByte reports whether c may be a byte of an import path.
func pathByte(c byte) bool {
	return identByte(c) || c == '/' || c == '.' || c == '-'
}

// identByte reports whether c may be a byte of a Go identifier: a letter,
// a digit, an underscore, or a byte of a non-ASCII letter.
func identByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// typeID is the identity of the Go type T without reflect (binsize BS2):
// the type word of a nil *T, which the Go runtime keeps once per type.
func typeID[T any]() unsafe.Pointer {
	var p any = (*T)(nil)
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&p))[0]
}

// variant is the variant name of v: g's Variant, else the name v gives
// itself (a flat union's, whose Go type is the union's), else its Go
// type's name.
func variant(g *MachineGraph, v any) string {
	if g != nil && g.Variant != nil {
		return g.Variant(v)
	}
	if n, ok := v.(interface{ ErgoVariant() string }); ok {
		return n.ErgoVariant()
	}
	if v == nil {
		return ""
	}
	return ErgoTypeName(rt.TypeName(rt.TypeString(v)), typePkg, typeNames)
}

// arm is the site of g's first arm that takes the pair (state, signal),
// "" when g is nil or no arm does. After a partial arm it names each arm
// that may have taken the pair, up to the first whole one: "a:3 or a:5".
func (g *MachineGraph) arm(state, signal string) string {
	if g == nil {
		return ""
	}
	site, last := "", ""
	for _, a := range g.Arms {
		if takes(a.States, state) && takes(a.Signals, signal) {
			// The patterns of one case are arms at one site: the site
			// is named once.
			if a.Site != last {
				if site != "" {
					site += " or "
				}
				site += a.Site
				last = a.Site
			}
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
	decideIgnore: "Ignore", decideReject: "Reject", decideDone: "Done", decideTell: "Tell"}

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
	s := variant(g, at)
	for _, tm := range g.Timers {
		if tm.State == s {
			return tm.Signal
		}
	}
	return ""
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
	case decideStay, decideTell:
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
		Signal: a.timerSignal(g, s), In: rt.Span(d)})
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
func (a *Actor[S, E, D]) traceChild(halt func(*rt.Task) error, epoch uint64) func(*rt.Task) error {
	a.task.TraceMachine(&rt.MachineEvent{What: rt.MachineChildStarted, Epoch: epoch})
	return func(t *rt.Task) error {
		err := halt(t)
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
