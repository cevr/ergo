package v1

import (
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// Hosts (rule 16 of the runner contract in actor.go; std actor.Host, the
// model docs/research/actor-machine/host.qnt). A host is an empty slot for
// a child that a step makes and puts in the state it enters. The runner
// binds it to that entry; the entry's work fills it with actor.ChildIn;
// Get waits for the fill and fails ActorStopped once the entry ended. So a
// host of an ended entry never gives a child of a newer one: the lease
// check is the runner's, not the program's.

// The states of a host.
const (
	hostMade   = iota // made in a step, not bound yet
	hostOpen          // bound to an entry, empty
	hostFilled        // filled by its entry's work
	hostClosed        // its entry ended, or no entry owns it
)

// Host is std actor.Host[A]'s Go body: a slot for one A, owned by one
// entry of one actor.
type Host[A any] struct {
	mu    sync.Mutex
	state uint8
	owner any // the actor whose entry owns it
	child A
	wake  chan struct{} // made by a waiting Get, closed by the fill or the close
}

// hostCell is a host as the runner sees it, whatever its A.
type hostCell interface {
	// bind makes the host its owner's open entry's: every open host is
	// in its owner's list of the open entry, which closes them all when
	// the entry ends, so an open host is always of the current entry.
	bind(owner any)
	// close ends the host: Get fails ActorStopped from now on.
	close()
	// fillable reports whether the host is open and owner's.
	fillable(owner any) bool
}

// hostKey is the key of the runner's binding of its actor to its task:
// NewHost finds the actor whose step calls it through it.
type hostKey struct{}

// hostRunner is an actor's runner as NewHost sees it.
type hostRunner interface {
	// takeHost keeps h for the entry the running step decides, and is
	// false when t is not the runner's task in a step.
	takeHost(t *rt.Task, h hostCell) bool
}

// NewHost is std actor.NewHost on the caller's task t. In a step of an
// actor (t is its runner's task, in a step), the host is the actor's: the
// runner binds it to the entry the step enters (an Enter or an Emit), or
// to the current entry (a Stay), and closes it when the step enters none
// (Ignore, Reject, Done, a failure). Made anywhere else, no entry owns it,
// so it is closed at once: Get fails ActorStopped.
func NewHost[A any](t *rt.Task) *Host[A] {
	h := &Host[A]{}
	if r, ok := t.Value(hostKey{}).(hostRunner); ok && r.takeHost(t, h) {
		return h
	}
	h.state = hostClosed
	return h
}

// Get is h.Get() on the caller's task t: the child once h's entry's work
// filled h; ActorStopped once that entry ended (or when no entry owns h).
// It waits while h is empty; a stop of t raises rt.Interrupted.
func (h *Host[A]) Get(t *rt.Task) (A, error) {
	var zero A
	for {
		t.Check()
		h.mu.Lock()
		switch h.state {
		case hostClosed:
			h.mu.Unlock()
			return zero, ActorStopped{}
		case hostFilled:
			c := h.child
			h.mu.Unlock()
			return c, nil
		}
		if h.wake == nil {
			h.wake = make(chan struct{})
		}
		w := h.wake
		h.mu.Unlock()
		select {
		case <-w:
		case <-t.Done():
			panic(rt.Interrupted)
		}
	}
}

func (h *Host[A]) bind(owner any) {
	h.mu.Lock()
	if h.state == hostMade {
		h.state, h.owner = hostOpen, owner
	}
	h.mu.Unlock()
}

func (h *Host[A]) close() {
	h.mu.Lock()
	var zero A
	h.state, h.child, h.owner = hostClosed, zero, nil
	h.wakeLocked()
	h.mu.Unlock()
}

func (h *Host[A]) fillable(owner any) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state == hostOpen && h.owner == owner
}

// fill puts c in h; the caller checked fillable under its entry's lock.
func (h *Host[A]) fill(c A) {
	h.mu.Lock()
	h.state, h.child = hostFilled, c
	h.wakeLocked()
	h.mu.Unlock()
}

func (h *Host[A]) wakeLocked() {
	if h.wake != nil {
		close(h.wake)
		h.wake = nil
	}
}

// ActorNoHost is the host std actor.Child gives MachineChild: none.
func ActorNoHost[A any]() *Host[A] { return nil }

// ActorHostDefect is the defect of an actor.ChildIn whose host its work's
// entry cannot fill: the host is of another entry (a state carried it
// from an ended one), it is filled already, or no entry owns it.
type ActorHostDefect struct {
	Site string
}

// String is the defect's text.
func (d *ActorHostDefect) String() string {
	return "actor.ChildIn at " + d.Site + ": the host is not an empty host of this work's entry; " +
		"a step makes a host with actor.NewHost and puts it in the state it enters, and that entry's work fills it once"
}

// takeHost keeps h for the entry the running step decides (rule 16).
func (a *Actor[S, E, D]) takeHost(t *rt.Task, h hostCell) bool {
	if t != a.task || a.cur == nil {
		return false
	}
	a.newHosts = append(a.newHosts, h)
	return true
}

// bindNewHosts binds the hosts the last step made to the open entry, of
// a.epoch: the one the step entered, or the current one after a Stay.
func (a *Actor[S, E, D]) bindNewHosts() {
	if len(a.newHosts) == 0 {
		return
	}
	a.kidsMu.Lock()
	for _, h := range a.newHosts {
		if a.kidsOpen && a.kidsEpoch == a.epoch {
			h.bind(a)
			a.hosts = append(a.hosts, h)
			a.hostsLeft++
		} else {
			h.close()
		}
	}
	a.kidsMu.Unlock()
	clear(a.newHosts)
	a.newHosts = a.newHosts[:0]
}

// closeNewHosts closes the hosts the last step made when it entered no
// state, or failed.
func (a *Actor[S, E, D]) closeNewHosts() {
	for _, h := range a.newHosts {
		h.close()
	}
	clear(a.newHosts)
	a.newHosts = a.newHosts[:0]
}

// closeHostsLocked closes the open entry's hosts; kidsMu is held.
func (a *Actor[S, E, D]) closeHostsLocked() {
	for _, h := range a.hosts {
		h.close()
	}
	clear(a.hosts)
	a.hosts, a.hostsLeft = a.hosts[:0], 0
	if a.settle != nil {
		a.settle()
		a.settle = nil
	}
}

// filledHostLocked counts a fill of one of the open entry's hosts; when
// it was the last empty one, the runner's wait for the publish ends.
// kidsMu is held.
func (a *Actor[S, E, D]) filledHostLocked() {
	a.hostsLeft--
	if a.hostsLeft == 0 && a.settle != nil {
		a.settle()
		a.settle = nil
	}
}
