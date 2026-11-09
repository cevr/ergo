package v1

import (
	"fmt"
	"iter"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The body of the std package actor (docs/spec/runtime.md, Machines and
// actors 6, 7 and 11; #57): a machine run live, on one task of its own,
// with an inbox. The std package builds the helpers below from a prelude
// Machine and calls MachineStart; the Go body never names the program's
// Decision, Option or Machine types (they are the program's), so it holds
// the step's decision as an opaque D and reads it through kind and next.
//
// The runner contract (survey §2.2; the Quint model
// docs/research/actor-machine/actor_machine.qnt):
//
//  1. Run to completion: one goroutine takes one envelope at a time and
//     steps it, commits, arms and answers before it takes the next.
//  2. Order: every signal enters one FIFO inbox (a Queue of capacity
//     signals), outside ones and timer ones alike, so each sender's
//     signals are stepped in the order it sent them.
//  3. Commit: Enter, Stay and Emit commit next(s, d); Ignore and Reject
//     keep the state; Done keeps it, gives its output and ends the actor.
//  4. Entry: the first state, an Enter and an Emit open an entry, with a
//     new epoch. Stay keeps the entry, its timer and its work.
//  5. Exit before entry: when an entry ends, its timer child is stopped and
//     its work is interrupted, and both are drained, before the next entry
//     arms its timer and starts its work.
//  6. No stale signal: a timer envelope and a work's signal carry their
//     entry's epoch; one of an ended entry is dropped, never stepped, and
//     counted (Dropped). The epoch is the runner's: a work's send is bound
//     to it when the work starts, so no program code checks it.
//  7. Timer: delay(s) of an opening state, unless it is ActorNoTimer(),
//     arms a child that sleeps on the program's Scheduler and then pushes
//     the timer envelope; fire(at, s) steps it, where at is the state its
//     entry opened in. A delay that is not positive is a defect
//     (ActorTimerDefect; fs2's rule for machines.RunTimed): it would fire
//     at its entry's instant, so a state whose timer re-enters it would
//     never let time pass.
//  8. End: after Done, a failure after a Send or a timer, a defect, or
//     Stop, no signal is stepped: the inbox closes, so later Sends and Asks
//     fail ActorStopped, queued Asks are answered ActorStopped, queued
//     Sends are dropped, and queued State reads get the last state.
//  9. Failure: a step failure goes to the asker and the actor goes on;
//     after a Send or a timer it ends the actor, and Stop returns it. A
//     defect ends the actor, and Stop raises it.
//  10. Work (MU4): an entry whose state has work (hasWork(workOf(s)))
//     starts it as a child task after the commit, after its timer is
//     armed, and before observers see the state (Changes, the Ask's
//     answer, the first State; MachineStart returns after the first
//     entry's work started). The work gets send, which pushes a signal
//     tagged with the entry's epoch and waits for room as a Send does; an
//     interrupted work's send raises at its check, so a work that the end
//     of its entry interrupted sends nothing. The typed failure of a work
//     is a signal already (the program's map, std actor); a defect of a
//     work ends the actor whenever it happens, and Stop raises it: it is
//     pushed as an envelope that is never dropped, or, when admission has
//     closed, it is the work's exit, which the drain reads.
type Actor[S, E, D any] struct {
	inbox   *Queue[envelope[S, E, D]]
	task    *rt.Task
	sleeper Sleeper
	span    string

	step  func(*rt.Task, S, E) (D, error)
	fire  func(*rt.Task, S, S) (D, error)
	delay func(S) time.Duration
	kind  func(D) int
	next  func(S, D) S
	out   func(*rt.Task, D)
	// work is the work of a state's entry, and false when it has none
	// (rule 10): the program's workOf, hasWork and run bound together, so
	// the Actor needs no type parameter for the program's Work value.
	work func(S) (func(*rt.Task, func(E)), bool)

	dropped atomic.Int64
	// handled counts the envelopes the runner took and handled, so a test
	// on a virtual clock can wait until the runner is idle.
	handled atomic.Int64
	done    chan struct{}

	mu    sync.Mutex
	state S // the last committed state
	ended bool
	exit  error // the runner's exit, read after done closes
	subs  map[*changes[S]]struct{}

	// The runner's own fields: only its goroutine reads or writes them.
	cur     *envelope[S, E, D] // the envelope in a step, nil between steps
	timer   *rt.Handle[struct{}]
	working *rt.Handle[struct{}] // the entry's work, nil when it has none
	epoch   uint64
}

// The decisions of the prelude Decision, in the order kind gives them.
const (
	decideEnter = iota
	decideStay
	decideEmit
	decideIgnore
	decideReject
	decideDone
)

// The envelope kinds of the inbox.
const (
	sendSignal = iota
	askSignal
	stateRead
	timerSignal
	workSignal // a signal a work sent, tagged with its entry's epoch
	workDefect // a work's defect: never dropped
)

type envelope[S, E, D any] struct {
	kind  uint8
	sig   E
	epoch uint64
	at    S // a timer's: the state its entry opened in
	reply chan answer[S, D]
	err   error // a work defect's exit
}

type answer[S, D any] struct {
	d   D
	s   S
	err error
}

// ActorStopped is actor.Stopped, the typed failure of Send and Ask on an
// actor that has ended or whose Stop was requested, and the answer of an
// Ask that was queued or in a step when it ended (runtime.md, Machines and
// actors 6). An ended actor is a normal end of the protocol, so it is in
// the operations' error rows and a catch handles it.
type ActorStopped struct{}

// Error is the failure's text, its Ergo name.
func (ActorStopped) Error() string { return "actor.Stopped" }

// MachineStart is actor.StartMachine's body. span, t and sl are the hidden
// site, task and scheduler arguments: the actor's task is a child of t (a
// stop of t stops it), and its timers sleep on sl. initial is the first
// state; capacity is the inbox's (0 makes each Send wait for the runner to
// take it; a negative capacity is a defect). workOf, hasWork and work are
// the entries' work (rule 10): workOf(s) is the program's Work value of a
// state, hasWork reports whether it is one, and work runs it on the work's
// task with send. It returns once the runner has armed the first entry's
// timer and started its work; the runner then waits for signals.
func MachineStart[S, E, D, W any](span Site, t *rt.Task, sl Sleeper, initial S,
	step func(*rt.Task, S, E) (D, error), fire func(*rt.Task, S, S) (D, error),
	delay func(S) time.Duration, workOf func(S) W, hasWork func(W) bool,
	work func(*rt.Task, W, func(E)), kind func(D) int, next func(S, D) S,
	out func(*rt.Task, D), capacity int) *Actor[S, E, D] {
	if capacity < 0 {
		panic(fmt.Sprintf("actor.StartMachine: negative capacity %d", capacity))
	}
	a := &Actor[S, E, D]{
		inbox: New[envelope[S, E, D]](capacity), sleeper: sl, span: string(span),
		step: step, fire: fire, delay: delay, kind: kind, next: next, out: out,
		done: make(chan struct{}), state: initial, subs: map[*changes[S]]struct{}{},
	}
	a.work = func(s S) (func(*rt.Task, func(E)), bool) {
		w := workOf(s)
		if !hasWork(w) {
			return nil, false
		}
		return func(t *rt.Task, send func(E)) { work(t, w, send) }, true
	}
	holder := rt.NewScope(t)
	ready := make(chan struct{})
	var once sync.Once
	started := func() { once.Do(func() { close(ready) }) }
	rt.Go(&holder, a.span, func(at *rt.Task) (struct{}, error) {
		a.task = at
		_, err := guarded(at, func(at *rt.Task) (struct{}, error) { return a.loop(at, started) })
		started() // the first entry's arm or work raised
		a.finish(err)
		return struct{}{}, nil
	})
	<-ready
	return a
}

// loop is the runner (rules 1 to 7 and 10). started tells MachineStart
// that the first entry's children run.
func (a *Actor[S, E, D]) loop(at *rt.Task, started func()) (struct{}, error) {
	s := a.state
	a.arm(at, s)
	a.startWork(at, s)
	started()
	for {
		env, err := a.inbox.Pop(at)
		if err != nil {
			return struct{}{}, nil // closed and empty
		}
		a.cur = &env
		var end bool
		s, end, err = a.handle(at, s, &env)
		a.cur = nil
		if err != nil || end {
			return struct{}{}, err
		}
		a.handled.Add(1)
	}
}

// handle steps one envelope taken from the inbox in the state s: it
// returns the state after it, and whether the actor ends, with the
// failure that ends it.
func (a *Actor[S, E, D]) handle(at *rt.Task, s S, env *envelope[S, E, D]) (S, bool, error) {
	var d D
	var err error
	switch env.kind {
	case stateRead:
		env.reply <- answer[S, D]{s: s}
		return s, false, nil
	case timerSignal:
		if env.epoch != a.epoch {
			a.dropped.Add(1)
			return s, false, nil
		}
		d, err = a.fire(at, env.at, s)
	case workSignal:
		if env.epoch != a.epoch {
			a.dropped.Add(1)
			return s, false, nil
		}
		d, err = a.step(at, s, env.sig)
	case workDefect:
		return s, true, env.err
	default:
		d, err = a.step(at, s, env.sig)
	}
	if err != nil {
		if env.kind == askSignal {
			a.cur = nil
			env.reply <- answer[S, D]{err: err}
			return s, false, nil
		}
		return s, true, err
	}
	k := a.kind(d)
	switch k {
	case decideEnter, decideEmit:
		a.stopTimer()
		a.stopWork()
		s = a.next(s, d)
		a.epoch++
		a.open(at, s)
		if k == decideEmit {
			a.out(at, d)
		}
	case decideStay:
		s = a.next(s, d)
		a.publish(s)
	case decideDone:
		a.out(at, d)
	}
	a.cur = nil
	if env.kind == askSignal {
		env.reply <- answer[S, D]{d: d}
	}
	return s, k == decideDone, nil
}

// open opens the entry of the committed state s, whose epoch is a.epoch:
// it arms the entry's timer, starts its work, and then shows s to the
// observers (rule 10). When the arm or the start raises, s is still the
// actor's last state: the deferred publish does not recover, so the panic
// keeps its stack.
func (a *Actor[S, E, D]) open(at *rt.Task, s S) {
	opened := false
	defer func() {
		if !opened {
			a.publish(s)
		}
	}()
	a.arm(at, s)
	a.startWork(at, s)
	opened = true
	a.publish(s)
}

// startWork starts the work of the entry that opened in s, when s has one
// (rule 10). Its send pushes a work signal with the entry's epoch on the
// work's task, so an interrupted work raises at the push and sends
// nothing. Its defect is pushed as a workDefect envelope, never dropped;
// when the push cannot be made (admission closed, or the work is being
// stopped), the defect is the work's exit, which stopWork or finish
// raises.
func (a *Actor[S, E, D]) startWork(at *rt.Task, s S) {
	run, ok := a.work(s)
	if !ok {
		return
	}
	epoch := a.epoch
	holder := rt.NewScope(at)
	a.working = rt.Go(&holder, a.span+" (work)", func(wt *rt.Task) (struct{}, error) {
		send := func(e E) {
			// Closed: the actor ended; the work has no one to tell.
			_ = a.inbox.Push(wt, envelope[S, E, D]{kind: workSignal, sig: e, epoch: epoch})
		}
		_, err := guarded(wt, func(wt *rt.Task) (struct{}, error) {
			run(wt, send)
			return struct{}{}, nil
		})
		if err == nil || err == rt.Interrupted {
			return struct{}{}, nil
		}
		_, pushed := guarded(wt, func(wt *rt.Task) (struct{}, error) {
			return struct{}{}, a.inbox.Push(wt, envelope[S, E, D]{kind: workDefect, err: err})
		})
		if pushed == nil {
			return struct{}{}, nil
		}
		return struct{}{}, err
	})
}

// stopWork interrupts the current entry's work and waits for its end
// (rule 5). A defect the work could not push ends the actor.
func (a *Actor[S, E, D]) stopWork() {
	h := a.working
	if h == nil {
		return
	}
	a.working = nil
	if err := h.Stop(); err != nil {
		raise(err)
	}
}

// arm starts the timer child of the entry that opened in s, when delay(s)
// is not negative. The child sleeps on the program's Scheduler, then
// pushes the timer envelope with the entry's epoch; a stop ends it first.
func (a *Actor[S, E, D]) arm(at *rt.Task, s S) {
	d := a.delay(s)
	if d == noTimer {
		return
	}
	if d <= 0 {
		panic(&ActorTimerDefect{State: stateText(s), In: d})
	}
	env := envelope[S, E, D]{kind: timerSignal, epoch: a.epoch, at: s}
	holder := rt.NewScope(at)
	a.timer = rt.Go(&holder, a.span+" (timer)", func(ct *rt.Task) (struct{}, error) {
		return guarded(ct, func(ct *rt.Task) (struct{}, error) {
			a.sleeper.Sleep(ct, d)
			// Closed: the actor ended; the timer has no one to tell.
			_ = a.inbox.Push(ct, env)
			return struct{}{}, nil
		})
	})
}

// noTimer is the delay of a state with no timer: no timer waits it.
const noTimer = time.Duration(math.MinInt64)

// ActorNoTimer is the delay std actor gives a state whose After is None.
func ActorNoTimer() time.Duration { return noTimer }

// ActorTimerDefect is the defect of an actor whose machine armed a timer
// with an In that is not positive (rule 7). Its text is the text of
// machines.RunTimed's defect (fs2), with the state as fmt's %+v writes it.
type ActorTimerDefect struct {
	State string
	In    time.Duration
}

// String is the defect's text, which the entry's report writes after
// "panic: ".
func (d *ActorTimerDefect) String() string {
	return "an actor's machine: the timer of state " + d.State + " has In " + d.In.String() +
		", which is not positive; a timer waits a positive duration"
}

// stateText is s's text: its Go type's name and fields, Idle{n:1}.
func stateText[S any](s S) string {
	v := any(s)
	name := ""
	if t := reflect.TypeOf(v); t != nil {
		name = t.Name()
	}
	return name + fmt.Sprintf("%+v", v)
}

// stopTimer stops and drains the current entry's timer child (rule 5). A
// defect of the child (its Sleep's) ends the actor.
func (a *Actor[S, E, D]) stopTimer() {
	h := a.timer
	if h == nil {
		return
	}
	a.timer = nil
	if err := h.Stop(); err != nil {
		raise(err)
	}
}

// publish commits s as the actor's state and hands it to every Changes
// subscriber.
func (a *Actor[S, E, D]) publish(s S) {
	a.mu.Lock()
	a.state = s
	for c := range a.subs {
		c.buf = append(c.buf, s)
		c.signal()
	}
	a.mu.Unlock()
}

// release answers env as an ended actor does: an Ask with ActorStopped, a
// State read with the last state; a Send or a timer is dropped.
func (a *Actor[S, E, D]) release(env *envelope[S, E, D]) {
	switch env.kind {
	case askSignal:
		env.reply <- answer[S, D]{err: ActorStopped{}}
	case stateRead:
		a.mu.Lock()
		s := a.state
		a.mu.Unlock()
		env.reply <- answer[S, D]{s: s}
	}
}

// finish ends the actor (rule 8) on the runner's goroutine, after the loop
// returned or raised: admission closes, the timer child and the work are
// stopped and drained (a defect of the work joins the exit), the envelope
// of an interrupted step and every queued one are released (a queued work
// defect joins the exit), and the Changes subscribers end. It waits for nothing a stop can interrupt.
func (a *Actor[S, E, D]) finish(exit error) {
	a.inbox.Close()
	for _, h := range []*rt.Handle[struct{}]{a.timer, a.working} {
		if h == nil {
			continue
		}
		if err := h.Stop(); err != nil {
			exit = join(exit, err)
		}
	}
	a.timer, a.working = nil, nil
	if a.cur != nil {
		a.release(a.cur)
		a.cur = nil
	}
	for {
		env, ok := a.inbox.Poll()
		if !ok {
			break
		}
		if env.kind == workDefect {
			// A work's defect is never dropped (rule 10).
			exit = join(exit, env.err)
			continue
		}
		a.release(&env)
	}
	a.mu.Lock()
	a.ended = true
	a.exit = exit
	for c := range a.subs {
		c.ended = true
		c.signal()
	}
	a.mu.Unlock()
	close(a.done)
}

// Send is a.Send(e) on the caller's task t: it admits e, waiting while the
// inbox is full, and returns once e is queued, before it is stepped. It
// fails ActorStopped when the actor has ended or its Stop was requested.
// It raises rt.Interrupted when t is stopped before or while it waits.
func (a *Actor[S, E, D]) Send(t *rt.Task, e E) error {
	if err := a.inbox.Push(t, envelope[S, E, D]{kind: sendSignal, sig: e}); err != nil {
		return ActorStopped{}
	}
	return nil
}

// Ask is a.Ask(e) on t: it admits e and waits until the runner has
// stepped it, committed and answered: the step's decision, or the step's
// typed failure (the actor goes on). It fails ActorStopped when the actor
// ended before e was stepped, and raises rt.Interrupted when t is stopped
// while it waits (the step, once taken, still runs).
func (a *Actor[S, E, D]) Ask(t *rt.Task, e E) (D, error) {
	reply := make(chan answer[S, D], 1)
	var zero D
	if err := a.inbox.Push(t, envelope[S, E, D]{kind: askSignal, sig: e, reply: reply}); err != nil {
		return zero, ActorStopped{}
	}
	r := a.await(t, reply)
	return r.d, r.err
}

// State is a.State() on t: the state after every signal admitted before
// it, read in inbox order, so a sender's State follows its own Sends. On an
// ended actor it is the last committed state. It never fails; it raises
// rt.Interrupted when t is stopped while it waits.
func (a *Actor[S, E, D]) State(t *rt.Task) S {
	reply := make(chan answer[S, D], 1)
	if err := a.inbox.Push(t, envelope[S, E, D]{kind: stateRead, reply: reply}); err != nil {
		// Ended or ending: the last state, once the runner has stopped.
		a.waitDone(t)
		a.mu.Lock()
		s := a.state
		a.mu.Unlock()
		return s
	}
	return a.await(t, reply).s
}

// await waits for an answer on t: a stop of t raises rt.Interrupted.
func (a *Actor[S, E, D]) await(t *rt.Task, reply chan answer[S, D]) answer[S, D] {
	t.Check()
	select {
	case r := <-reply:
		return r
	case <-t.Done():
		panic(rt.Interrupted)
	}
}

// waitDone waits on t until the runner has ended.
func (a *Actor[S, E, D]) waitDone(t *rt.Task) {
	select {
	case <-a.done:
	case <-t.Done():
		panic(rt.Interrupted)
	}
}

// Stop is a.Stop(): it interrupts the runner, closes admission and waits,
// uninterruptibly, until it has ended, its timer and its work drained and
// its queued Asks answered ActorStopped (rule 8). It returns the typed
// failure that ended the actor, if one did, and raises its defects, a
// work's included (rule 10). A Stop of an ended
// actor returns the same at once; every Stop is idempotent.
func (a *Actor[S, E, D]) Stop() error {
	// The interruption comes first: once it is set, the runner's next Pop
	// raises at its check, so no envelope is taken after the request, also
	// one queued before it. A Send admitted between the two is queued
	// before the close and released (dropped) by finish.
	a.task.Interrupt()
	a.inbox.Close()
	<-a.done
	a.mu.Lock()
	exit := a.exit
	a.mu.Unlock()
	if exit == nil || exit == rt.Interrupted {
		return nil
	}
	if f, ok := rt.Failure(exit); ok {
		return f
	}
	if d := defectsOnly(exit); d != nil {
		raise(d)
	}
	return nil
}

// defectsOnly is err's defects, nil when it has none (an interruption).
func defectsOnly(err error) error {
	c, ok := err.(*rt.Cause)
	if !ok || c == nil {
		return nil
	}
	var d rt.Cause
	for _, r := range c.Reasons {
		if r.Kind == rt.KindDefect {
			d.Reasons = append(d.Reasons, r)
		}
	}
	return d.Err()
}

// Dropped is a.Dropped(): how many timer signals and work signals of ended
// entries the runner dropped (rule 6; decision MQ6).
func (a *Actor[S, E, D]) Dropped() int { return int(a.dropped.Load()) }

// Changes is a.Changes() on the iterating task t: the actor's state when
// the loop begins, then each state the runner commits after it (an Enter,
// a Stay, an Emit), in commit order; it ends when the actor has ended and
// the states committed before are given. A break ends the subscription.
// Each step waits on t: a stop of t raises rt.Interrupted. A subscriber
// that does not read keeps every state in its buffer.
func (a *Actor[S, E, D]) Changes(t *rt.Task) iter.Seq[S] {
	return func(yield func(S) bool) {
		c := &changes[S]{}
		a.mu.Lock()
		c.buf = append(c.buf, a.state)
		c.ended = a.ended
		a.subs[c] = struct{}{}
		a.mu.Unlock()
		defer func() {
			a.mu.Lock()
			delete(a.subs, c)
			a.mu.Unlock()
		}()
		for {
			t.Check()
			s, ok := a.nextChange(t, c)
			if !ok || !yield(s) {
				return
			}
		}
	}
}

// changes is one Changes subscription: the committed states it has not
// given yet, guarded by the actor's mu.
type changes[S any] struct {
	buf   []S
	ended bool
	wake  chan struct{}
}

// signal wakes the subscriber's wait. The actor's mu is held.
func (c *changes[S]) signal() {
	if c.wake != nil {
		close(c.wake)
		c.wake = nil
	}
}

// nextChange is c's next state, waiting on t; false once the actor ended
// and c gave every state.
func (a *Actor[S, E, D]) nextChange(t *rt.Task, c *changes[S]) (S, bool) {
	a.mu.Lock()
	for {
		if len(c.buf) > 0 {
			s := c.buf[0]
			var zero S
			c.buf[0] = zero
			c.buf = c.buf[1:]
			a.mu.Unlock()
			return s, true
		}
		if c.ended {
			a.mu.Unlock()
			var zero S
			return zero, false
		}
		if c.wake == nil {
			c.wake = make(chan struct{})
		}
		ch := c.wake
		a.mu.Unlock()
		select {
		case <-ch:
		case <-t.Done():
			panic(rt.Interrupted)
		}
		a.mu.Lock()
	}
}
