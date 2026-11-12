package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Children and hosts of an actor (ORACLE O11; runtime.md, Machines and
// actors 14 and 16; std actor Child, ChildIn, Host, NewHost), on the
// runner of runner.go:
//
//   - actor.Child, called in an entry's work, starts a machine actor the
//     entry owns: its Emit and Done outputs reach the parent as signals of
//     that entry (dropped once the entry ended), and its defect ends the
//     parent. When the entry ends, and when the parent ends, its children
//     are stopped, last started first, before its timer and work; a work
//     that started children ends after their stop.
//   - A host is an empty slot for a child: NewHost in a step keeps it for
//     the entry the step enters (an Enter or an Emit) or keeps (a Stay),
//     and closes it otherwise; ChildIn fills it once; Get waits for the
//     fill and fails Stopped once the entry ended. An entry with hosts and
//     a work is shown once the work filled them, waited, or ended.
//
// A Child no entry owns, and a ChildIn on a host its entry cannot fill,
// are defects whose text names the Go build's call site: not run (a skip).

// The states of a host.
const (
	hostMade = iota
	hostOpen
	hostFilled
	hostClosed
)

// hostV is a host (std actor.Host): none is the host Child passes, which
// is no host.
type hostV struct {
	state   int
	owner   *actorV
	child   Value
	none    bool
	stopped *check.Named // actor.Stopped
}

func (h *hostV) describe() string { return "an actor.Host" }

func (h *hostV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if name != "Get" {
		ice("the Host operation %s", name)
	}
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	if ab := in.block(fr, at, "a host's child", func() bool { return h.state == hostFilled || h.state == hostClosed }); ab != nil {
		return nil, ab
	}
	if h.state == hostClosed {
		return nil, fr.abrupt(ctlFail, at, &record{t: h.stopped}, "")
	}
	return h.child, nil
}

func (h *hostV) close() {
	h.state, h.child, h.owner = hostClosed, nil, nil
}

// bindNewHosts binds the hosts the last step made to the open entry: the
// one the step entered, or the current one after a Stay.
func (a *actorV) bindNewHosts() {
	for _, h := range a.newHosts {
		if a.entry != nil && a.entry.open && a.entry.epoch == a.epoch && h.state == hostMade {
			h.state, h.owner = hostOpen, a
			a.hosts = append(a.hosts, h)
			a.hostsLeft++
		} else {
			h.close()
		}
	}
	a.newHosts = nil
}

// closeHosts closes each host of *hs and empties the list.
func (a *actorV) closeHosts(hs *[]*hostV) {
	for _, h := range *hs {
		h.close()
	}
	*hs = nil
}

// ownerOf is the entry that owns a Child called on t: the first of t and
// its ancestors that runs a work (its entry) or a runner (none).
func ownerOf(t *etask) *entryRef {
	for x := t; x != nil; x = x.parent {
		if x.runs != nil {
			return nil
		}
		if x.entry != nil {
			return x.entry
		}
	}
	return nil
}

func init() {
	stoppedOf := func(fn *check.Func) *check.Named { return stdRecord(fn, "Stopped", nil).t }
	natives["actor.NewHost"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.build(fr, at, 1, 2, 0); ab != nil {
			return nil, ab
		}
		h := &hostV{stopped: stoppedOf(fn)}
		if t := in.cur; t != nil && t.runs != nil && t.runs.cur != nil {
			t.runs.newHosts = append(t.runs.newHosts, h)
			return h, nil
		}
		h.state = hostClosed // no entry owns it
		return h, nil
	}
	natives["actor.noHost"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return &hostV{none: true, state: hostClosed, stopped: stoppedOf(fn)}, nil
	}
	natives["actor.child"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return in.actorChild(fr, at, fn, args)
	}
}

// actorChild is std actor's child: a machine actor that the entry of the
// calling work owns, filling host unless it is none.
func (in *interp) actorChild(fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
	if len(args) != 12 {
		ice("actor.child with %d arguments", len(args))
	}
	host, ok := args[0].(*hostV)
	if !ok {
		ice("a child's host is %T", args[0])
	}
	ref := ownerOf(in.cur)
	if ref == nil {
		return nil, fr.unsupported(at, "an actor.Child that no machine entry owns (its defect names the Go build's call site)")
	}
	parent := ref.a
	if !ref.open || parent.entry != ref {
		return nil, fr.interrupt(at) // the entry's exit began: the work is being stopped
	}
	if !host.none && (host.state != hostOpen || host.owner != parent) {
		return nil, fr.unsupported(at, "an actor.ChildIn on a host its entry cannot fill (its defect names the Go build's call site)")
	}
	outFn := args[10]
	c := &actorV{
		initial: args[1], step: args[2], fire: args[3], delay: args[4], workOf: args[5], hasWork: args[6], work: args[7],
		kind: args[8], next: args[9], owner: ref,
	}
	// deliver gives a child output to the parent as a signal of the entry
	// that owns the child, on the child's task; once the parent ended,
	// no one is told.
	deliver := &nativeFunc{name: "deliver", call: func(in *interp, dfr *frame, dat syntax.Node, dargs []Value) (Value, *abrupt) {
		ab := in.queuePush(dfr, dat, parent.inbox, &envelope{kind: envChild, sig: dargs[0], epoch: ref.epoch})
		if ab != nil && ab.kind != ctlFail {
			return nil, ab
		}
		return unit{}, nil
	}}
	c.out = &nativeFunc{name: "out", call: func(in *interp, ofr *frame, oat syntax.Node, oargs []Value) (Value, *abrupt) {
		return in.callValue(ofr, oat, outFn, []Value{oargs[0], deliver})
	}}
	// The entry keeps the child before it starts, so the entry's end,
	// whenever it comes, stops it.
	parent.kids = append(parent.kids, c)
	ref.adopted = true
	if !host.none {
		host.state, host.child = hostFilled, c
		parent.hostsLeft--
		if parent.hostsLeft == 0 {
			parent.settled = true
		}
	}
	if ab := in.launch(fr, at, fn, c, args[11].(int64)); ab != nil {
		return nil, ab
	}
	return c, nil
}
