package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Durable actors (ORACLE O19; runtime.md, Machines and actors 16.1 and
// 17; std actor StartDurable, SuperviseDurable): recovering runs the
// recover with a collection bound to the task, so each host the recover
// makes (in a replay of the step) is unbound; the first actor its start
// opens takes them, and that actor's first entry binds each one its first
// state holds, in the order made, and closes the rest. Every host the
// recover made is closed when it fails, and when the start opened no
// actor. A supervised restart's fresh runs the same way on the runner's
// task. keep runs a committed transition's save in the step.
//
// The failure of a save, and of a restart's fresh, is a defect whose text
// is the failure's Go Error text, which the evaluator does not write: a
// skip.

// recovery collects the hosts made while a recover runs.
type recovery struct {
	hosts []*hostV
	ended bool
}

// recovering finds the collection a host made on t joins: t's or an
// ancestor's, while its recover runs.
func recovering(t *etask) *recovery {
	for x := t; x != nil; x = x.parent {
		if x.recov != nil {
			if x.recov.ended {
				return nil
			}
			return x.recov
		}
	}
	return nil
}

// recoverWith runs f, the recover, with a collection bound to the running
// task: the hosts it made, in the order made, or none (all closed) when it
// did not return a value.
func (in *interp) recoverWith(f func() (Value, *abrupt)) (Value, []*hostV, *abrupt) {
	t := in.cur
	rec := &recovery{}
	prev := t.recov
	t.recov = rec
	v, ab := f()
	t.recov = prev
	rec.ended = true
	if ab != nil {
		for _, h := range rec.hosts {
			h.close()
		}
		return nil, nil, ab
	}
	return v, rec.hosts, nil
}

// bindRecovered binds, to the entry opening in s, each recovered host s
// holds, in the order made, and closes the others.
func (a *actorV) bindRecovered(s Value) {
	if len(a.recovered) == 0 {
		return
	}
	held := map[*hostV]bool{}
	holdsHosts(s, held, map[any]bool{})
	for _, h := range a.recovered {
		if held[h] {
			a.newHosts = append(a.newHosts, h)
		} else {
			h.close()
		}
	}
	a.recovered = nil
}

// holdsHosts marks in held each host the value v reaches.
func holdsHosts(v Value, held map[*hostV]bool, seen map[any]bool) {
	switch v := v.(type) {
	case *hostV:
		held[v] = true
	case *record:
		if seen[v] {
			return
		}
		seen[v] = true
		for _, f := range v.fields {
			holdsHosts(f, held, seen)
		}
	case tuple:
		for _, e := range v.elems {
			holdsHosts(e, held, seen)
		}
	case sliceV:
		for _, e := range v.elems() {
			holdsHosts(e, held, seen)
		}
	case mapV:
		for _, e := range v.b.m {
			holdsHosts(e, held, seen)
		}
	case *operation:
		holdsHosts(v.value, held, seen)
	}
}

func init() {
	natives["actor.recovering"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if in.cur == nil {
			return nil, fr.unsupported(at, "a durable actor outside a run's tasks")
		}
		s, hosts, ab := in.recoverWith(func() (Value, *abrupt) { return in.callValue(fr, at, args[0], nil) })
		if ab != nil {
			return nil, ab
		}
		t := in.cur
		prev := t.handoff
		t.handoff = &hosts
		a, ab := in.callValue(fr, at, args[1], []Value{s})
		left := *t.handoff
		t.handoff = prev
		// The actor took the hosts; a start that opened none leaves them.
		for _, h := range left {
			h.close()
		}
		return a, ab
	}
	natives["actor.keep"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		_, ab := in.callValue(fr, at, args[0], args[1:])
		if ab != nil && (ab.kind == ctlFail || ab.kind == ctlEscape) {
			return nil, fr.unsupported(at, "a durable actor's failed save (its defect writes the failure's Go Error text)")
		}
		return unit{}, ab
	}
}
