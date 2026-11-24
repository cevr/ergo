package v1

import "ergo.local/prototype/runtime/rt"

// The static exit form (FLIP F7 STATIC-EXIT). A frame whose scope exists
// only for its exit actions (scope.OnExit), where no action starts a task
// or registers an action of its own and the frame owns no value, keeps
// its actions in an Exits, a Go local of the frame, not in a Scope's
// list. The frame defers Exits.End as its first defer, as it defers
// Scope.End:
//
//	var x v1.Exits
//	x.Begin(t)
//	defer x.End(&err) // or x.End(nil)
//	…
//	v1.ExitsOnExit(&x, func() { … }) // scope.OnExit() { … }
//
// End is Scope.End for such a frame, step for step: the body's panic is
// recovered and joined beside *err; the actions run last registered
// first, masked, each through the shared release boundary, each in a
// frame of its own whose end is an interruption point, every defect
// kept; the frame's own end lets a stop win; the exit is settled into
// *err or raised. The general form (NewScope, OnExit, Scope.End) stays the
// reference, selectable with lower.exitLowering = "scope"; the irspike
// property TestIRStaticExitsMatchSource holds the two equal.
//
// The cost the general form pays and this one does not: a heap Scope for
// each action's frame, a list entry and its closure per action, and the
// frame's route through owning withs and nested actions, none of which
// such a frame has.
//
// Prior art: Go's open-coded defers keep a frame's defers in its stack
// frame; Swift's defer and Rust's drop guards are scoped at compile time.

// exitsInline is the number of actions an Exits holds without a slice.
const exitsInline = 4

// Exits is the exit actions of a frame in the static exit form.
type Exits struct {
	t      *rt.Task
	n      int
	inline [exitsInline]func()
	more   []func()
	exits  rt.Cause
	ended  bool
}

// Begin starts the exit actions of a frame that runs on task t.
//
//ergo:wait none
func (x *Exits) Begin(t *rt.Task) { x.t = t }

// ExitsOnExit is scope.OnExit() { body } in a frame in the static exit
// form: it registers body on x, as OnExit registers it on the frame's
// scope. Registering on a frame that has returned is a defect.
//
//ergo:wait none
//ergo:release frame
func ExitsOnExit(x *Exits, body func()) {
	if x.ended {
		panic("scope.OnExit: the frame that would run this exit action has returned")
	}
	if x.n < exitsInline {
		x.inline[x.n] = body
	} else {
		x.more = append(x.more, body)
	}
	x.n++
}

// End ends the frame of x, as Scope.End ends a frame scope that holds
// only exit actions: see the file comment.
func (x *Exits) End(err *error) {
	p := recover()
	var e error
	if err != nil {
		e = *err
	}
	if p != nil {
		e = join(e, capture(x.t, p))
	}
	settle(err, x.end(e))
}

// end runs the actions, last first, and ends the frame (Scope.end).
func (x *Exits) end(e error) error {
	x.ended = true
	for x.n > 0 {
		x.n--
		var body func()
		if x.n < exitsInline {
			body, x.inline[x.n] = x.inline[x.n], nil
		} else {
			body = x.more[x.n-exitsInline]
			x.more = x.more[:x.n-exitsInline]
		}
		x.run(body)
	}
	if len(x.exits.Reasons) > 0 {
		e = join(e, x.exits.Err())
	}
	return x.t.ScopeExit(e) // stop wins (Exit causes 5)
}

// run is one action, as OnExit's entry runs it: masked, through the
// release boundary, in a frame of its own that holds nothing, whose end
// (actionEnd) lets a stop win.
func (x *Exits) run(body func()) {
	if !x.t.Traced() {
		x.Run(body)
		return
	}
	ended := false
	var nested error
	release(x.t, &x.exits, func() {
		body()
		ended = true
		nested = x.t.ScopeExit(nil)
	}, func(carried error) error {
		if ended {
			return carried // the frame's end itself panicked
		}
		ended = true
		return x.t.ScopeExit(carried)
	})
	x.exits.Add(nested)
}

// The defer form (FLIP F7 STATIC-EXIT2). A frame in the static exit form
// whose every action is registered in the frame's own Go function, outside
// a loop, registers each with a Go defer of Run (or Quiet) in place of
// ExitsOnExit:
//
//	var x v1.Exits
//	x.Begin(t)
//	defer x.End(&err)
//	…
//	defer x.Run(func() { … }) // scope.OnExit() { … }
//
// Go runs the defers last registered first, before End, which then finds
// no action of its own: the same order, and End recovers the body's panic
// and joins it before the actions' defects as it does with the slots. The
// action's closure does not escape, and Go open-codes the defers: no
// allocation.
//
// Run and run (the slots') are the action of the general form, step for
// step, on a task that is not traced: masked, its defect recovered and
// kept as a defect in release, the rest of the frame going on. The
// general form's action frame ends inside the mask, where a stop is not
// observed, so its end adds nothing and is left out; a traced task takes
// the general path (release, rt.Release), which raises the release events.

// Run runs body, an exit action of the frame of x, now: deferred at its
// registration (the defer form), or from end (the slots).
//
//ergo:wait none
func (x *Exits) Run(body func()) {
	if x.t.Traced() {
		x.run(body)
		return
	}
	m := x.t.Mask()
	x.call(body)
	m.Unmask()
}

// Quiet is Run for an action whose block the checker gives an empty
// footprint (no capability, task or unknown code, so no interruption
// point and nothing that reads the mask): the mask is left out.
//
//ergo:wait none
func (x *Exits) Quiet(body func()) {
	if x.t.Traced() {
		x.run(body)
		return
	}
	x.call(body)
}

// call runs body, keeping its defect on the frame's exits.
func (x *Exits) call(body func()) {
	defer x.caught()
	body()
}

// caught records the panic of the action that call runs, as rt.Release
// records one: through capture, the defect owner, a fresh defect as a
// defect in release, a carried exit as its reasons.
func (x *Exits) caught() {
	p := recover()
	if p == nil {
		return
	}
	e := capture(x.t, p)
	switch p := p.(type) {
	case *exit:
		x.exits.Add(e)
	case error:
		if p == rt.Interrupted {
			x.exits.Add(e)
			return
		}
		x.exits.AddReleased(e)
	default:
		x.exits.AddReleased(e)
	}
}
