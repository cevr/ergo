package eval

import (
	"math"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Actors (ORACLE O10; runtime.md, Machines and actors 6, 7, 11 to 18;
// std actor). std actor builds its helpers from a prelude Machine and
// calls its Go body `start`; the evaluator's start is written here from
// the runner contract the std package and runtime.md state (rules 1 to
// 12, 16 and 17), on the cooperative scheduler of sched.go, and shares no
// code with runtime/v1:
//
//   - One runner task steps one envelope at a time from one FIFO inbox of
//     capacity envelopes (a queueV, whose closed failure is actor.Stopped):
//     Sends, Asks, State reads, timer and work signals, Feeds.
//   - Enter, Stay and Emit commit next(s, d); Ignore and Reject keep the
//     state; Done keeps it, gives its output and ends the actor. The
//     output goes to the sink before anything else acts on the decision.
//   - The first state, an Enter and an Emit open an entry with a new
//     epoch: the old entry's timer and work are stopped and drained first;
//     the new entry's timer sleeps on the starter's Scheduler and its work
//     runs on a task of its own. A timer or work signal of an ended entry
//     is dropped and counted.
//   - A step's typed failure goes to its asker, and after a Send or a
//     timer it ends the actor; a defect ends it. Stop and Drain return the
//     typed failure and raise the defect.
//   - Changes gives the state at its start, then each published state.
//   - Supervise restarts an incarnation a defect ended, from fresh's
//     state, up to its limit, while admission is open.
//
// Children, hosts and a durable actor's recovery are not run yet: their
// externs have no native, so a program that reaches one is a skip.

// The envelope kinds of the inbox.
const (
	envSend = iota
	envAsk
	envState
	envTimer
	envWork
	envWorkDefect
	envFeed
)

// The decisions, in the order std actor's kind gives them.
const (
	decEnter = iota
	decStay
	decEmit
	decIgnore
	decReject
	decDone
)

type envelope struct {
	kind  int
	sig   Value
	epoch uint64
	at    Value // a timer's: the state its entry opened in
	reply *actorReply
	err   *abrupt // a work's defect
	run   Value   // a Feed's source
}

// actorReply is the answer to one Ask or State read.
type actorReply struct {
	done    bool
	d, s    Value
	fail    *abrupt // the step's typed failure
	stopped bool
}

// actorV is a running actor (std actor's Actor).
type actorV struct {
	fn    *check.Func // std actor's start, for its records
	at    syntax.Node
	home  *frame // the starter's frame: the callbacks' caps
	sched Value  // the starter's Scheduler provider

	initial, fresh                                 Value
	step, fire, delay, workOf, hasWork, work, kind Value
	next, out                                      Value
	limit                                          int64

	inbox *queueV
	task  *etask
	ready bool

	state     Value
	ended     bool
	fail      *abrupt // the typed failure that ended it
	defect    *abrupt // the first defect that ended it
	subs      []*changeSub
	latest    Value
	hasLatest bool
	final     Value
	finished  bool
	dropped   int64
	restarts  int64

	// The runner's own: the envelope in a step, the entry's epoch, its
	// timer and work tasks, and the started sources.
	cur     *envelope
	epoch   uint64
	timer   *etask
	working *etask
	feeds   []*etask
}

func (a *actorV) describe() string { return "an actor.Actor" }

func init() {
	natives["actor.start"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return in.actorStart(fr, at, fn, args)
	}
	natives["actor.noTimer"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return int64(math.MinInt64), nil
	}
	actorOf := func(v Value) *actorV {
		a, ok := v.(*actorV)
		if !ok {
			ice("an actor's value is %T", v)
		}
		return a
	}
	natives["actor.Restarts"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return actorOf(args[0]).restarts, nil
	}
	natives["actor.hasLatest"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return actorOf(args[0]).hasLatest, nil
	}
	natives["actor.latest"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return actorOf(args[0]).latest, nil
	}
	natives["actor.final"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return actorOf(args[0]).final, nil
	}
	natives["actor.failure"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		a := actorOf(args[0])
		if a.fail == nil {
			return unit{}, nil
		}
		return nil, fr.abrupt(ctlFail, at, a.fail.val, "")
	}
	natives["actor.exitKind"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		a := actorOf(args[0])
		if ab := in.block(fr, at, "an actor to end", func() bool { return a.ended }); ab != nil {
			return nil, ab
		}
		switch {
		case a.fail != nil:
			return int64(2), nil
		case a.defect != nil:
			return int64(3), nil
		case a.finished:
			return int64(0), nil
		}
		return int64(1), nil
	}
	natives["actor.live"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		a := actorOf(args[0])
		r, ab := a.ask(in, fr, at, &envelope{kind: envState})
		if ab != nil {
			return nil, ab
		}
		if r.stopped {
			return nil, a.stoppedFailure(fr, at)
		}
		return r.s, nil
	}
	natives["actor.feed"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		a := actorOf(args[0])
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		if ab := in.queuePush(fr, at, a.inbox, &envelope{kind: envFeed, run: args[1]}); ab != nil {
			return nil, ab
		}
		return unit{}, nil
	}
}

// actorStart is std actor's start: the actor, once its runner has
// opened the first entry (its timer armed, its work started).
func (in *interp) actorStart(fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
	if len(args) != 13 {
		ice("actor.start with %d arguments", len(args))
	}
	if in.sched == nil {
		return nil, fr.unsupported(at, "an actor outside a run's tasks")
	}
	capacity := args[12].(int64)
	if capacity < 0 {
		return nil, fr.defect(at, "actor.StartMachine: negative capacity %d", capacity)
	}
	limit := args[2].(int64)
	if limit < 0 {
		return nil, fr.defect(at, "actor.Supervise: a negative restart limit %d", limit)
	}
	sched, ok := fr.caps.lookup(typeKey(rtype{t: check.PreludeType("Scheduler")}))
	if !ok {
		return nil, fr.unsupported(at, "an actor whose Scheduler no with of the run binds")
	}
	if ab := in.build(fr, at, 1, 8, 0); ab != nil {
		return nil, ab
	}
	a := &actorV{
		fn: fn, at: at, home: fr, sched: sched,
		initial: args[0], fresh: args[1], limit: limit,
		step: args[3], fire: args[4], delay: args[5], workOf: args[6], hasWork: args[7], work: args[8],
		kind: args[9], next: args[10], out: args[11],
		inbox: &queueV{capacity: capacity, closed: stdRecord(fn, "Stopped", nil).t},
		state: args[0],
	}
	a.task = in.start(in.siteOf(fr, at, "actor.StartMachine"), func(t *etask) (Value, *abrupt) {
		return unit{}, a.runner(in)
	})
	if ab := in.block(fr, at, "an actor's first entry", func() bool { return a.ready || a.task.state == done }); ab != nil {
		return nil, ab
	}
	return a, nil
}

// frame is a frame for the actor's callbacks on the running task: the
// starter's package and capabilities.
func (a *actorV) frame() *frame {
	h := a.home
	return &frame{pkg: h.pkg, info: h.info, decl: h.decl, key: h.key, vars: map[*check.Var]*slot{}, env: h.env, owner: h.owner, caps: h.caps}
}

func (a *actorV) stoppedFailure(fr *frame, at syntax.Node) *abrupt {
	return fr.abrupt(ctlFail, at, &record{t: a.inbox.closed}, "")
}

// runner is the runner task's body: the incarnations' loops, then the
// end. Its own exit is a stop of the run only.
func (a *actorV) runner(in *interp) *abrupt {
	fr := a.frame()
	var exit *abrupt
	for runs := int64(1); ; runs++ {
		if runs > 1 {
			exit = a.renew(in, fr)
		}
		if exit == nil {
			exit = a.loop(in, fr)
		}
		if stops(exit) {
			a.ready = true
			return exit
		}
		restart := a.fresh != nil && a.limit > 0 && endedByDefect(exit) && !a.task.stopped && runs <= a.limit && !a.inbox.ended
		if !restart {
			break
		}
		if ab := a.crash(in, fr); ab != nil {
			if stops(ab) {
				return ab
			}
			exit = ab
			break
		}
		a.restarts++
	}
	a.ready = true
	return a.finish(in, fr, exit)
}

// endedByDefect reports an exit that a defect ended: neither a value, a typed
// failure nor an interruption.
func endedByDefect(ab *abrupt) bool {
	if ab == nil {
		return false
	}
	switch ab.kind {
	case ctlFail, ctlEscape, ctlInterrupt:
		return false
	}
	return true
}

// renew gives a new incarnation fresh's state, committed and shown.
func (a *actorV) renew(in *interp, fr *frame) *abrupt {
	s, ab := in.callValue(fr, a.at, a.fresh, nil)
	if ab != nil {
		if ab.kind == ctlFail || ab.kind == ctlEscape {
			return fr.unsupported(a.at, "the failure of a supervised actor's restart state yet")
		}
		return ab
	}
	a.publish(s)
	return nil
}

// loop is one incarnation: the first entry, then one envelope at a time.
func (a *actorV) loop(in *interp, fr *frame) *abrupt {
	s := a.state
	if ab := a.open(in, fr, s, false); ab != nil {
		return ab
	}
	a.ready = true
	for {
		v, ok, ab := in.queuePop(fr, a.at, a.inbox)
		if ab != nil {
			return ab // a stop while it waited: the interruption
		}
		if !ok {
			return nil // closed and empty: a drained end
		}
		env := v.(*envelope)
		a.cur = env
		var end bool
		s, end, ab = a.handle(in, fr, s, env)
		if ab != nil || end {
			return ab
		}
		a.cur = nil
	}
}

// call runs the callback f on args, on the running task.
func (a *actorV) call(in *interp, fr *frame, f Value, args ...Value) (Value, *abrupt) {
	return in.callValue(fr, a.at, f, args)
}

// handle steps env in the state s: the state after it, and whether the
// actor ends, with the exit that ends it.
func (a *actorV) handle(in *interp, fr *frame, s Value, env *envelope) (Value, bool, *abrupt) {
	if ab := in.charge(fr, a.at, 1); ab != nil {
		return s, true, ab
	}
	switch env.kind {
	case envState:
		a.answer(env, &actorReply{s: s})
		return s, false, nil
	case envWorkDefect:
		return s, true, env.err
	case envFeed:
		a.startFeed(in, fr, env.run)
		a.cur = nil
		return s, false, nil
	}
	if (env.kind == envTimer || env.kind == envWork) && env.epoch != a.epoch {
		a.dropped++
		a.cur = nil
		return s, false, nil
	}
	var d Value
	var ab *abrupt
	if env.kind == envTimer {
		d, ab = a.call(in, fr, a.fire, env.at, s)
	} else {
		d, ab = a.call(in, fr, a.step, s, env.sig)
	}
	if ab != nil {
		if (ab.kind == ctlFail || ab.kind == ctlEscape) && env.kind == envAsk {
			a.cur = nil
			a.answer(env, &actorReply{fail: ab})
			return s, false, nil
		}
		return s, true, ab
	}
	kv, ab := a.call(in, fr, a.kind, d)
	if ab != nil {
		return s, true, ab
	}
	k := kv.(int64)
	if k == decStay {
		ns, ab := a.call(in, fr, a.next, s, d)
		if ab != nil {
			return s, true, ab
		}
		s = ns
		a.latest, a.hasLatest = d, true
		a.publish(s)
		a.cur = nil
		a.answer(env, &actorReply{d: d})
		return s, false, nil
	}
	// The output goes first (rule 17).
	if k == decEmit || k == decDone {
		if _, ab := a.call(in, fr, a.out, d); ab != nil {
			return s, true, ab
		}
	}
	a.latest, a.hasLatest = d, true
	if k == decDone {
		a.final, a.finished = d, true
	}
	if k == decEnter || k == decEmit {
		if ab := a.stopEntry(in, fr); ab != nil {
			return s, true, ab
		}
		ns, ab := a.call(in, fr, a.next, s, d)
		if ab != nil {
			return s, true, ab
		}
		s = ns
		a.epoch++
		if ab := a.open(in, fr, s, true); ab != nil {
			return s, true, ab
		}
	}
	a.cur = nil
	a.answer(env, &actorReply{d: d})
	return s, k == decDone, nil
}

// answer gives an Ask or a State read its answer; a Send has none.
func (a *actorV) answer(env *envelope, r *actorReply) {
	if env.reply != nil {
		*env.reply = *r
		env.reply.done = true
	}
}

// open opens the entry of s: it arms its timer, starts its work, and
// shows s (when commit; the first entry's state is shown already). When
// the arm or the start fails, s is still shown.
func (a *actorV) open(in *interp, fr *frame, s Value, commit bool) *abrupt {
	ab := a.arm(in, fr, s)
	if ab == nil {
		ab = a.startWork(in, fr, s)
	}
	if commit {
		a.publish(s)
	}
	return ab
}

// arm starts the timer of the entry that opened in s, when its delay is
// not noTimer, and returns once the timer's sleep waits (or it ended).
func (a *actorV) arm(in *interp, fr *frame, s Value) *abrupt {
	dv, ab := a.call(in, fr, a.delay, s)
	if ab != nil {
		return ab
	}
	d := dv.(int64)
	if d == math.MinInt64 {
		return nil
	}
	if d <= 0 {
		return fr.unsupported(a.at, "a machine timer that is not positive (its defect prints the state as Go formats it)")
	}
	env := &envelope{kind: envTimer, epoch: a.epoch, at: s}
	call, _ := a.at.(*syntax.CallExpr)
	t := in.start(in.siteOf(fr, a.at, "an actor's timer"), func(t *etask) (Value, *abrupt) {
		tfr := a.frame()
		if _, ab := in.dispatch(tfr, call, a.sched, "Sleep", []Value{d}); ab != nil {
			return nil, ab
		}
		if ab := in.queuePush(tfr, a.at, a.inbox, env); ab != nil && ab.kind != ctlFail {
			return nil, ab // closed: the actor ended, and no one is told
		}
		return unit{}, nil
	})
	a.timer = t
	return in.block(fr, a.at, "an actor's timer to wait", func() bool { return t.state == blocked || t.state == done })
}

// startWork starts the work of the entry that opened in s, when s has
// one, on a task of its own with a send of the entry's epoch.
func (a *actorV) startWork(in *interp, fr *frame, s Value) *abrupt {
	w, ab := a.call(in, fr, a.workOf, s)
	if ab != nil {
		return ab
	}
	has, ab := a.call(in, fr, a.hasWork, w)
	if ab != nil {
		return ab
	}
	if has != true {
		return nil
	}
	epoch := a.epoch
	a.working = in.start(in.siteOf(fr, a.at, "an actor's work"), func(t *etask) (Value, *abrupt) {
		wfr := a.frame()
		send := &nativeFunc{name: "send", call: func(in *interp, sfr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
			ab := in.queuePush(sfr, at, a.inbox, &envelope{kind: envWork, sig: args[0], epoch: epoch})
			if ab != nil && ab.kind != ctlFail {
				return nil, ab
			}
			return unit{}, nil // closed: the actor ended
		}}
		_, ab := a.call(in, wfr, a.work, w, send)
		return unit{}, a.taskDefect(in, wfr, ab)
	})
	return nil
}

// taskDefect is the exit of a work's or a source's task whose body ended
// with ab: a defect is pushed into the inbox, never dropped, or, when the
// push cannot be made, it is the task's exit, which the stop raises.
func (a *actorV) taskDefect(in *interp, fr *frame, ab *abrupt) *abrupt {
	if ab == nil || ab.kind == ctlInterrupt || stops(ab) {
		return ab
	}
	if !endedByDefect(ab) {
		return nil
	}
	if in.stoppedNow() || a.inbox.ended {
		return ab
	}
	if pab := in.queuePush(fr, a.at, a.inbox, &envelope{kind: envWorkDefect, err: ab}); pab != nil {
		if stops(pab) {
			return pab
		}
		return ab
	}
	return nil
}

// startFeed starts a Feed's source on a task of its own, whose send
// admits a signal as a Send does and ends the source once the actor has
// ended.
func (a *actorV) startFeed(in *interp, fr *frame, run Value) {
	t := in.start(in.siteOf(fr, a.at, "an actor's feed"), func(t *etask) (Value, *abrupt) {
		ffr := a.frame()
		send := &nativeFunc{name: "send", call: func(in *interp, sfr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
			ab := in.queuePush(sfr, at, a.inbox, &envelope{kind: envSend, sig: args[0]})
			if ab != nil && ab.kind == ctlFail {
				return nil, sfr.interrupt(at) // the actor ended: the source ends here
			}
			return unit{}, ab
		}}
		_, ab := a.call(in, ffr, run, send)
		return unit{}, a.taskDefect(in, ffr, ab)
	})
	a.feeds = append(a.feeds, t)
}

// stopTask stops t and waits for its end, uninterruptibly: its defect,
// if one ended it.
func (a *actorV) stopTask(in *interp, fr *frame, t *etask) *abrupt {
	if t == nil {
		return nil
	}
	stopTree(t)
	if ab := in.waitDone(fr, a.at, t, "an actor's task to stop"); ab != nil {
		return ab
	}
	if endedByDefect(t.exit) {
		return t.exit
	}
	return nil
}

// stopEntry ends the open entry: its timer, then its work.
func (a *actorV) stopEntry(in *interp, fr *frame) *abrupt {
	t, w := a.timer, a.working
	a.timer, a.working = nil, nil
	if ab := a.stopTask(in, fr, t); ab != nil {
		return ab
	}
	return a.stopTask(in, fr, w)
}

// publish commits s as the actor's state and hands it to each Changes
// subscription.
func (a *actorV) publish(s Value) {
	a.state = s
	for _, c := range a.subs {
		c.buf = append(c.buf, s)
	}
}

// release answers env as an ended actor does.
func (a *actorV) release(env *envelope) {
	switch env.kind {
	case envAsk:
		a.answer(env, &actorReply{stopped: true})
	case envState:
		a.answer(env, &actorReply{s: a.state, stopped: true})
	}
}

// crash ends an incarnation a defect ended, admission left open: the
// entry stops, the envelope in the step and the queued ones are released
// (a State read gets the state, a Feed starts), and the epoch moves on.
func (a *actorV) crash(in *interp, fr *frame) *abrupt {
	if ab := a.stopEntry(in, fr); ab != nil {
		return ab
	}
	if a.cur != nil {
		a.release(a.cur)
		a.cur = nil
	}
	for n := len(a.inbox.buf) + len(a.inbox.pushers); n > 0; n-- {
		v, ok := a.inbox.take()
		if !ok {
			break
		}
		env := v.(*envelope)
		switch env.kind {
		case envFeed:
			a.startFeed(in, fr, env.run)
		case envState:
			a.answer(env, &actorReply{s: a.state})
		default:
			a.release(env)
		}
	}
	a.epoch++
	return nil
}

// finish ends the actor with exit: admission closes, the entry and the
// sources stop (their defects join the exit), the envelope in the step
// and every queued one are released, and the subscriptions end.
func (a *actorV) finish(in *interp, fr *frame, exit *abrupt) *abrupt {
	a.inbox.end()
	a.note(exit)
	in.mask(1)
	defer in.mask(-1)
	for _, t := range []*etask{a.timer, a.working} {
		ab := a.stopTask(in, fr, t)
		if stops(ab) {
			return ab
		}
		a.note(ab)
	}
	a.timer, a.working = nil, nil
	for _, t := range a.feeds {
		ab := a.stopTask(in, fr, t)
		if stops(ab) {
			return ab
		}
		a.note(ab)
	}
	a.feeds = nil
	if a.cur != nil {
		a.release(a.cur)
		a.cur = nil
	}
	for {
		v, ok := a.inbox.take()
		if !ok {
			break
		}
		env := v.(*envelope)
		if env.kind == envWorkDefect {
			a.note(env.err)
			continue
		}
		a.release(env)
	}
	a.ended = true
	return nil
}

// note joins ab to the actor's exit: its first typed failure and its
// first defect.
func (a *actorV) note(ab *abrupt) {
	switch {
	case ab == nil || ab.kind == ctlInterrupt:
	case ab.kind == ctlFail || ab.kind == ctlEscape:
		if a.fail == nil {
			a.fail = ab
		}
	default:
		if a.defect == nil {
			a.defect = ab
		}
	}
}

// ask admits env with a reply and waits for the answer; an inbox that is
// closed answers at once as an ended actor does.
func (a *actorV) ask(in *interp, fr *frame, at syntax.Node, env *envelope) (*actorReply, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	env.reply = &actorReply{}
	if ab := in.queuePush(fr, at, a.inbox, env); ab != nil {
		if ab.kind != ctlFail {
			return nil, ab
		}
		return &actorReply{stopped: true}, nil
	}
	r := env.reply
	if ab := in.block(fr, at, "an actor's answer", func() bool { return r.done }); ab != nil {
		return nil, ab
	}
	return r, nil
}

// result is what Stop and Drain return once the actor has ended.
func (a *actorV) result(fr *frame, at syntax.Node) *abrupt {
	switch {
	case a.fail != nil:
		return fr.abrupt(ctlFail, at, a.fail.val, "")
	case a.defect != nil:
		return a.defect
	}
	return nil
}

func (a *actorV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	switch name {
	case "Send":
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		if ab := in.queuePush(fr, at, a.inbox, &envelope{kind: envSend, sig: args[0]}); ab != nil {
			return nil, ab
		}
		return unit{}, nil
	case "Ask":
		r, ab := a.ask(in, fr, at, &envelope{kind: envAsk, sig: args[0]})
		switch {
		case ab != nil:
			return nil, ab
		case r.stopped:
			return nil, a.stoppedFailure(fr, at)
		case r.fail != nil:
			return nil, fr.abrupt(ctlFail, at, r.fail.val, "")
		}
		return r.d, nil
	case "State":
		r, ab := a.ask(in, fr, at, &envelope{kind: envState})
		if ab != nil {
			return nil, ab
		}
		if r.stopped && !r.done {
			// Ended or ending: the last state, once the runner ended.
			if ab := in.block(fr, at, "an actor to end", func() bool { return a.ended }); ab != nil {
				return nil, ab
			}
			return a.state, nil
		}
		return r.s, nil
	case "Stop", "Drain":
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		if name == "Stop" && a.task.state != done {
			a.task.stopped = true
		}
		a.inbox.end()
		in.mask(1)
		ab := in.block(fr, at, "an actor to end", func() bool { return a.ended })
		in.mask(-1)
		if ab != nil {
			return nil, ab
		}
		if ab := a.result(fr, at); ab != nil {
			return nil, ab
		}
		return unit{}, nil
	case "Dropped":
		return a.dropped, nil
	case "Changes":
		return &changesSeq{a: a}, nil
	}
	ice("the Actor operation %s", name)
	return nil, nil
}

// workLit is a Work literal of the work rule's forms (check/work.go;
// runtime.md, Machines and actors 12): the std machines.Work whose Start
// runs Run (its result a signal through OnSuccess, or itself) or Listen
// with send; a typed failure of either becomes a signal through
// OnFailure, or is one; an interruption and a defect pass through.
func (in *interp) workLit(fr *frame, x *syntax.CompositeLit, lit *check.WorkLit) (Value, *abrupt) {
	fnx := lit.Run
	if fnx == nil {
		fnx = lit.Listen
	}
	var fns [3]Value
	for i, e := range []syntax.Expr{fnx, lit.OnSuccess, lit.OnFailure} {
		if e == nil {
			continue
		}
		v, ab := in.expr(fr, e)
		if ab != nil {
			return nil, ab
		}
		fns[i] = v
	}
	run, onSuccess, onFailure := fns[0], fns[1], fns[2]
	t := fr.rtypeOf(x)
	n, ok := t.t.(*check.Named)
	if !ok || structOf(n) == nil {
		ice("a Work literal of %s", t.t)
	}
	if ab := in.build(fr, x, 1, 2, 0); ab != nil {
		return nil, ab
	}
	start := &nativeFunc{name: "Start", call: func(in *interp, sfr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
		send := args[0]
		var r Value
		var ab *abrupt
		if lit.Run != nil {
			r, ab = in.callValue(sfr, at, run, nil)
		} else {
			_, ab = in.callValue(sfr, at, run, []Value{send})
		}
		var e Value
		switch {
		case ab != nil && (ab.kind == ctlFail || ab.kind == ctlEscape):
			e = ab.val
			if onFailure != nil {
				if e, ab = in.callValue(sfr, at, onFailure, []Value{ab.val}); ab != nil {
					return nil, ab
				}
			}
		case ab != nil:
			return nil, ab
		case lit.Run == nil:
			return unit{}, nil // a listener's end sends nothing
		default:
			e = r
			if onSuccess != nil {
				if e, ab = in.callValue(sfr, at, onSuccess, []Value{r}); ab != nil {
					return nil, ab
				}
			}
		}
		return in.callValue(sfr, at, send, []Value{e})
	}}
	rec := &record{t: n, key: argsKey(n, t.env), fields: make([]Value, len(structOf(n).Fields))}
	rec.fields[fieldIndex(n, "Start")] = start
	return rec, nil
}

// changesSeq is a.Changes(): a for over it subscribes at its first step.
type changesSeq struct {
	a   *actorV
	sub *changeSub
}

// changeSub is one subscription: the states it has not given yet.
type changeSub struct {
	buf   []Value
	ended bool
}

func (c *changesSeq) next(in *interp, fr *frame, at syntax.Node) (Value, bool, *abrupt) {
	a := c.a
	if c.sub == nil {
		c.sub = &changeSub{buf: []Value{a.state}}
		if !a.ended {
			a.subs = append(a.subs, c.sub)
		}
	}
	sub := c.sub
	if ab := in.block(fr, at, "an actor's next state", func() bool { return len(sub.buf) > 0 || a.ended }); ab != nil {
		return nil, false, ab
	}
	if len(sub.buf) == 0 {
		a.unsubscribe(sub)
		return nil, false, nil
	}
	v := sub.buf[0]
	sub.buf = sub.buf[1:]
	return v, true, nil
}

func (a *actorV) unsubscribe(sub *changeSub) {
	for i, x := range a.subs {
		if x == sub {
			a.subs = append(a.subs[:i:i], a.subs[i+1:]...)
			return
		}
	}
}
