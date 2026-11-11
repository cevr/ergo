package v1

import (
	"reflect"
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// Hosts in a recovered state (wf10 DH1; rule 15 of the runner contract in
// actor.go; runtime.md, Machines and actors 16 and 17). A host is entry
// data, not durable data. A durable actor's recover replays its signals
// with the pure step, so the step's NewHost runs where no actor runs a
// step: the host it makes is unbound (hostMade), kept by the recovery that
// collects it. The actor the durable start opens takes those hosts, and its
// first entry binds each one its first state holds, in the order made, as
// bindNewHosts binds a step's; it closes the rest, which only an earlier
// state of the replay held. So when the durable start returns, each host
// the recover made is bound to the first entry or closed, and none holds a
// child: the first entry's work fills the bound ones with fresh children,
// and a journal never stores a live child, only the cell's place.
// SuperviseDurable's restart runs its recover (the fresh state) the same
// way, on the runner's task.

// recovery collects the hosts made while a durable start's recover runs.
// It is bound to the recover's task under hostKey, so NewHost finds it as
// it finds a runner; a task the recover starts inherits it.
type recovery struct {
	mu    sync.Mutex
	hosts []hostCell
	ended bool // the recover returned: a host made now is closed at once
}

// takeHost keeps h unbound for the recovered actor's first entry, while
// the recover runs.
func (r *recovery) takeHost(_ *rt.Task, h hostCell) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return false
	}
	r.hosts = append(r.hosts, h)
	return true
}

// take ends the collection and gives the hosts made, in the order made.
func (r *recovery) take() []hostCell {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended = true
	hs := r.hosts
	r.hosts = nil
	return hs
}

// handoffKey binds the hosts a recover made to the task that starts the
// recovered actor, while ActorRecovering's start runs.
type handoffKey struct{}

// handoff holds the hosts for the first actor the start opens.
type handoff struct {
	mu    sync.Mutex
	hosts []hostCell
}

// take gives the hosts once: to the first actor started on the task.
func (h *handoff) take() []hostCell {
	h.mu.Lock()
	defer h.mu.Unlock()
	hs := h.hosts
	h.hosts = nil
	return hs
}

// ActorRecovering is the body of std actor's recovering, the frame of
// StartDurable and SuperviseDurable: it runs recover on t with a recovery
// bound, so each host recover makes (in a replay by machines.From, or any
// other fold) is unbound; then start(s) with those hosts handed to the
// actor that start opens, whose first entry binds the ones s holds. A host
// that no entry binds is closed when start returns, and every host recover
// made is closed when recover fails or raises.
func ActorRecovering[S, A any](t *rt.Task, recover func(*rt.Task) (S, error), start func(*rt.Task, S) A) (A, error) {
	rec := &recovery{}
	var hosts []hostCell
	defer func() { closeHosts(hosts) }()
	s, err := recoverWith(t, rec, recover)
	hosts = rec.take()
	if err != nil {
		var zero A
		return zero, err
	}
	h := &handoff{hosts: hosts}
	c := t.WithValue(handoffKey{}, h)
	defer c.End()
	a := start(t, s)
	// The actor took the hosts (it bound or closed each one before it
	// returned); a start that opened none leaves them, closed above.
	hosts = h.take()
	return a, nil
}

// recoverWith runs recover on t with rec bound under hostKey, and ends the
// binding on every path.
func recoverWith[S any](t *rt.Task, rec *recovery, recover func(*rt.Task) (S, error)) (S, error) {
	c := t.WithValue(hostKey{}, hostRunner(rec))
	defer c.End()
	return recover(t)
}

// closeHosts closes each host of hs.
func closeHosts(hs []hostCell) {
	for _, h := range hs {
		h.close()
	}
}

// handedHosts takes the hosts ActorRecovering hands to the actor that t
// starts now, or nil.
func handedHosts(t *rt.Task) []hostCell {
	if h, ok := t.Value(handoffKey{}).(*handoff); ok {
		return h.take()
	}
	return nil
}

// bindRecovered binds, to the entry that opens in s, each recovered host
// that s holds, in the order made, and closes the others. kidsOpen is set
// for the entry (openKids).
func (a *Actor[S, E, D]) bindRecovered(s S) {
	if len(a.recovered) == 0 {
		return
	}
	held, rest := heldHosts(s, a.recovered)
	clear(a.recovered)
	a.recovered = nil
	closeHosts(rest)
	a.newHosts = append(a.newHosts, held...)
	a.bindNewHosts()
}

// closeRecovered closes the recovered hosts no entry bound: the runner
// ended before its first entry opened.
func (a *Actor[S, E, D]) closeRecovered() {
	closeHosts(a.recovered)
	clear(a.recovered)
	a.recovered = nil
}

// heldHosts splits made into the hosts the value s holds and the rest,
// each in the order of made. It walks s as data: structs, arrays, slices,
// maps, interfaces and pointers, each pointer once. It does not enter a
// function (a host a closure captures is not held as data) or a pointer
// to a value of the runtime's own types (an actor, a queue, a Ref, or a
// host that is not in made), which is a handle, not data.
func heldHosts[S any](s S, made []hostCell) (held, rest []hostCell) {
	want := make(map[uintptr]bool, len(made))
	for _, h := range made {
		want[reflect.ValueOf(h).Pointer()] = false
	}
	found := 0
	seen := map[uintptr]bool{}
	stack := []reflect.Value{reflect.ValueOf(&s).Elem()}
	for len(stack) > 0 && found < len(want) {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				continue
			}
			p := v.Pointer()
			if was, ok := want[p]; ok {
				if !was {
					want[p] = true
					found++
				}
				continue
			}
			if seen[p] || runtimeType(v.Type().Elem()) {
				continue
			}
			seen[p] = true
			stack = append(stack, v.Elem())
		case reflect.Interface:
			if !v.IsNil() {
				stack = append(stack, v.Elem())
			}
		case reflect.Struct:
			for i := v.NumField() - 1; i >= 0; i-- {
				stack = append(stack, v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			if !mayHold(v.Type().Elem()) {
				continue
			}
			for i := v.Len() - 1; i >= 0; i-- {
				stack = append(stack, v.Index(i))
			}
		case reflect.Map:
			if !mayHold(v.Type().Key()) && !mayHold(v.Type().Elem()) {
				continue
			}
			it := v.MapRange()
			for it.Next() {
				stack = append(stack, it.Key(), it.Value())
			}
		}
	}
	for _, h := range made {
		if want[reflect.ValueOf(h).Pointer()] {
			held = append(held, h)
		} else {
			rest = append(rest, h)
		}
	}
	return held, rest
}

// runtimePkgs are the packages whose values heldHosts does not enter.
var runtimePkgs = map[string]bool{
	reflect.TypeFor[recovery]().PkgPath(): true,
	reflect.TypeFor[rt.Task]().PkgPath():  true,
}

// runtimeType reports a type of the runtime's own packages.
func runtimeType(t reflect.Type) bool { return runtimePkgs[t.PkgPath()] }

// mayHold reports whether a value of type t may hold a pointer: an
// element of a basic kind holds no host.
func mayHold(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
		return true
	}
	return false
}
