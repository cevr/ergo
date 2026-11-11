package v1

import (
	"fmt"
	"iter"
	"math"
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
// The runner contract (survey §2.2; the model models/runner):
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
//     entry opened in. The sleep is registered with the Scheduler before
//     the entry is published, so an observer that sees the entry and then
//     advances a manual clock by the delay fires the timer. A delay that is not positive is a defect
//     (MachineTimerDefect, the defect of machines.RunTimed; fs2): it would fire
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
//  11. Observers (MU12): each Changes subscription gets the state shown at
//     its start, then every state published after it, once each, in commit
//     order, from a buffer of its own; a state is published when it is
//     committed (a Stay) or once its entry's children run (an entry),
//     and also when a stop overtakes the entry (open's deferred publish).
//     A subscription ends after the actor ended and it gave every state
//     published to it; nothing is published after the end. std actor's
//     WaitFor, SendAndWait and Select are loops over Changes.
//  12. Drain and exit (MU12): Drain closes admission, steps every queued
//     signal, then ends. The exit is one value, written once by finish:
//     Stop, Drain and every ActorExitKind caller read it. latest is the
//     decision of the last stepped signal, final the Done that ended it.
//  13. Children (MU5): actor.Child, called in an entry's work, starts a
//     machine actor that the entry owns (MachineChild). The work's task
//     is the entry's (the owners map), so the call finds it through its hidden
//     task; a runner binds no entry, so a step's Child is a defect. The
//     entry keeps its children in a list, in start order, by identity
//     (two children of one machine are two children). A child's Emit and
//     Done outputs reach the parent as signals tagged with the entry's
//     epoch (childSignal), so one of an ended entry is dropped as a stale
//     work signal is (rule 6). When the entry ends, and when the actor
//     ends, the runner stops its children one at a time, last started
//     first, each one stopping its own children first, before it stops
//     the timer and the work and before the next entry opens: a child
//     lives inside its parent's entry. A child's typed failure ends only
//     the child; its defect ends the parent, as a work's defect does
//     (pushed as a workDefect, or, when that cannot be pushed, raised by
//     the stop). The children's tasks are children of the work's task
//     and the work's task a child of the runner's, each shielded from its
//     parent's interruption (rt.GoShielded), so the runner stops them, in
//     order; a work that started children waits for their stop before it
//     ends, so no task ends before its children.
//  14. Feeds (MU10): Feed admits a source as a feedStart envelope, so a
//     Feed on an ended actor fails ActorStopped and its source never runs.
//     The runner starts the source as a child task of its own (startFeed):
//     it lives as long as the actor, not as its caller, and its sends are
//     a sender's (rule 2: in the source's order, untagged, admitted as a
//     Send is). When the actor ends, finish closes admission, so the
//     source's next send raises rt.Interrupted and the source ends there,
//     and then stops the source's task and waits for its end: no source
//     runs after the actor's end, and the actor's stop interrupts a
//     source that waits. A source that ends ends only its feed; a defect
//     of a source ends the actor, as a work's defect does (rule 10).
//  15. Hosts (mu-host; actor_host.go, the model models/host): NewHost in a
//     step (on the runner's task) keeps the host for the entry the step's
//     decision opens (Enter, Emit) or keeps (Stay); any other decision, a
//     failure or a defect closes it, and a host made anywhere else is
//     closed when made. MachineChild with a host fills it only when it is
//     open and the actor's (so of the open entry), under kidsMu with the
//     adoption. stopKids closes the entry's hosts under kidsMu before it
//     stops the children. An entry with hosts and a work is published once
//     the work filled them, reached its first wait (rt.WhenWaiting) or
//     ended.
//  16. Supervision (MU7; MachineSupervise, the model
//     models/supervise): a supervised actor's
//     incarnation that ends with a defect (of a step, a timer, a work, a
//     child, a source, a save, or of fresh) is restarted by tasks.Restart's
//     rule (restartable, as a Set's member is): while the actor's stop is
//     not requested, its admission is open (no Stop, no Drain) and fewer
//     than limit restarts were made. A crash ends the incarnation as rule
//     8 ends the actor, but admission stays open: the entry's children,
//     timer and work are stopped, the ask in the step and every queued
//     ask are answered ActorStopped, queued sends and signals are
//     dropped, queued State reads get the last state, and queued Feeds
//     start (a source lives as long as the actor, rule 14). The new
//     incarnation starts from fresh(), never from the last state, on a
//     new epoch, and Restarts counts it. A Done, a Stop, a Drain and a
//     typed failure end the actor for good; the defect that finds the
//     budget spent is the actor's exit.
//  17. Outputs (fua-a): an Emit's and a Done's output goes to the sink
//     first, once the step decided it (after a durable save): before the
//     decision is Latest, the old entry's exit (rule 5), the new entry's
//     open (rule 7, so a timer that is not positive raises after the
//     output), the state's publish, the Ask's answer and the actor's
//     end. A signal the old entry's children, timer or work send while
//     the sink runs is stale (rule 6). An observer that sees a state has
//     every output of the commits up to it. A child's sink is its
//     toParent (rule 13), so a child's output is in its parent's inbox
//     before the child's state is shown.
type Actor[S, E, D any] struct {
	inbox   mailbox[envelope[S, E, D]] // in the Actor: no allocation of its own
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

	// spare is a reply channel no Ask or State read holds (takeReply).
	spare   atomic.Pointer[replySlot[S, D]]
	dropped atomic.Int64
	// handled counts the envelopes the runner took and handled, so a test
	// on a virtual clock can wait until the runner is idle.
	handled atomic.Int64
	done    chan struct{}
	// ready is done once the first entry's children run (started): a
	// WaitGroup in the Actor, so a start allocates no channel for it.
	ready   sync.WaitGroup
	isReady atomic.Bool

	mu    sync.Mutex
	state S // the last committed state
	ended bool
	// doneWait are the waitDone callers parked in a clock's domain.
	doneWait []*rt.Task
	exit     error // the runner's exit, read after done closes
	subs     map[*changes[S]]struct{}
	// latest is the decision of the last signal stepped (rule 12), and
	// final the Done decision that ended the actor, when one did.
	latest    D
	hasLatest bool
	final     D
	finished  bool

	// Children (rule 13), guarded by kidsMu: the open entry's children in
	// start order, the epoch of the entry that takes them, whether it
	// still does, and a channel closed once they are stopped. kidsMu is
	// held while a child starts, so the stop of an entry's children waits
	// for a start in progress and then sees it.
	kidsMu sync.Mutex
	// adopting is set, in an explored run only, while adopt holds kidsMu
	// across a child's start, whose handshake is a scheduling point:
	// stopKids waits for it where the scheduler sees the wait (X7).
	adopting  atomic.Bool
	kids      []*kid
	kidsEpoch uint64
	kidsOpen  bool
	kidsDone  chan struct{}
	// The open entry's hosts (rule 15), guarded by kidsMu: the hosts, how
	// many are not filled yet, and the release of the runner's wait to
	// publish the entry (nil when it does not wait).
	hosts     []hostCell
	hostsLeft int
	settle    func()
	// report pushes a child's defect into its parent's inbox (nil for an
	// actor that is no child); reported is set when it did, so the
	// parent's stop does not raise it again.
	report   func(*rt.Task, error) bool
	reported atomic.Bool

	// feedQ holds the sources Feed admitted that the runner has not
	// started yet, by id, guarded by feedMu (rule 14). A feedStart
	// envelope names the one it starts.
	feedMu  sync.Mutex
	feedSeq uint64
	feedQ   map[uint64]func(*rt.Task, func(E))

	// Supervision (rule 16): fresh gives each new incarnation's first
	// state, nil for an actor that is not supervised; limit is the
	// restart budget; restarts counts the restarts made (Restarts).
	fresh    func(*rt.Task) (S, error)
	limit    int
	restarts atomic.Int64

	// The runner's own fields: only its goroutine reads or writes them.
	cur *envelope[S, E, D] // the envelope in a step, nil between steps
	// curEnv holds the envelope in a step, so taking one allocates
	// nothing (MU18); cur points to it while the step runs.
	curEnv  envelope[S, E, D]
	timer   *rt.Handle[struct{}]
	working *rt.Handle[struct{}] // the entry's work, nil when it has none
	feeds   []*feed              // the started sources that may still run (rule 14)
	// newHosts are the hosts the running step made (rule 15); hostSlot
	// binds the actor to its runner's task, so NewHost finds it.
	newHosts []hostCell
	hostSlot rt.Slot
	epoch    uint64
	bound    bool // the runner's task is in owners, as nil (runner)
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
	workSignal  // a signal a work sent, tagged with its entry's epoch
	workDefect  // a work's or a child's defect: never dropped
	childSignal // a child's output, tagged with its owner entry's epoch
	feedStart   // a Feed: start the oldest source of feedQ (rule 14)
)

type envelope[S, E, D any] struct {
	kind  uint8
	sig   E
	epoch uint64
	at    S // a timer's: the state its entry opened in
	reply *replySlot[S, D]
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
	return machineStart(span, t, sl, initial, step, fire, delay, workOf, hasWork, work, kind, next,
		out, capacity, nil, nil, 0)
}

// MachineSupervise is std actor's start: MachineStart for an actor that
// restarts after a defect (rule 16), at most limit times, each new
// incarnation from fresh's state; initial is the first incarnation's.
// StartMachine passes limit 0, which restarts none and never calls fresh. A
// typed failure of fresh is a defect of the incarnation (ActorFreshDefect).
// A negative limit is a defect, as tasks.Restart's is.
func MachineSupervise[S, E, D, W any](span Site, t *rt.Task, sl Sleeper, initial S,
	fresh func(*rt.Task) (S, error), limit int,
	step func(*rt.Task, S, E) (D, error), fire func(*rt.Task, S, S) (D, error),
	delay func(S) time.Duration, workOf func(S) W, hasWork func(W) bool,
	work func(*rt.Task, W, func(E)), kind func(D) int, next func(S, D) S,
	out func(*rt.Task, D), capacity int) *Actor[S, E, D] {
	if limit < 0 {
		panic(fmt.Sprintf("actor.Supervise: a negative restart limit %d", limit))
	}
	if limit == 0 {
		// No restart: the runner of MachineStart, which never calls fresh.
		fresh = nil
	}
	return machineStart(span, t, sl, initial, step, fire, delay, workOf, hasWork, work, kind, next,
		out, capacity, nil, fresh, limit)
}

// ActorFreshDefect is the defect of a supervised actor whose fresh state
// failed at a restart (a durable actor's recover): Reason is the failure.
type ActorFreshDefect struct {
	Reason string
}

// String is the defect's text.
func (d *ActorFreshDefect) String() string {
	return "actor.Supervise: the state of a restart failed: " + d.Reason
}

// ActorRestarts is std actor's Restarts(a): how many times a supervised
// actor restarted (rule 16); 0 for one that never did.
func ActorRestarts[S, E, D any](a *Actor[S, E, D]) int { return int(a.restarts.Load()) }

// MachineChild is actor.Child's body (rule 13): MachineStart for a child
// of the entry whose work calls it, found from t (ownerOf). out gets
// each decision and the entry's deliver, which pushes a child output as
// the entry's signal. The child's task is a child of t, shielded from t's
// interruption: its owner entry stops it. A call outside a work is a
// defect (ActorChildDefect); a call that races its entry's exit starts
// nothing and raises rt.Interrupted, as the work is being interrupted.
//
// host is actor.ChildIn's host (rule 15), nil for actor.Child: the child
// starts only when host is an empty host of the calling work's entry, and
// fills it; any other host is a defect (ActorHostDefect), with nothing
// started.
func MachineChild[S, E, D, W, P any](span Site, t *rt.Task, sl Sleeper, host *Host[*Actor[S, E, D]], initial S,
	step func(*rt.Task, S, E) (D, error), fire func(*rt.Task, S, S) (D, error),
	delay func(S) time.Duration, workOf func(S) W, hasWork func(W) bool,
	work func(*rt.Task, W, func(E)), kind func(D) int, next func(S, D) S,
	out func(*rt.Task, D, func(P)), capacity int) *Actor[S, E, D] {
	owner, ok := ownerOf(t).(childOwner[P])
	if !ok {
		panic(&ActorChildDefect{Site: string(span)})
	}
	var c *Actor[S, E, D]
	var cell hostCell
	if host != nil {
		cell = host
	}
	adopted, hostOK := owner.adopt(cell, func(report func(*rt.Task, error) bool, deliver func(*rt.Task, P)) func() error {
		c = machineStart(span, t, sl, initial, step, fire, delay, workOf, hasWork, work, kind, next,
			func(ct *rt.Task, d D) { out(ct, d, func(p P) { deliver(ct, p) }) }, capacity, report, nil, 0)
		return c.halt
	}, func() { host.fill(c) })
	if !hostOK {
		panic(&ActorHostDefect{Site: string(span)})
	}
	if !adopted {
		panic(rt.Interrupted)
	}
	return c
}

// ActorKeep is the body of std actor's keep (MU9; runtime.md, Machines and
// actors 17): it runs a durable actor's save of one committed transition,
// in the step, before the runner commits it. A typed failure of save is a
// defect (ActorSaveDefect) that ends the actor: the actor cannot keep its
// promise that a committed state outlives it. effect-machine's cluster
// journal defects an append failure the same way.
func ActorKeep[S, E any](t *rt.Task, save func(*rt.Task, S, S, E) error, prev, next S, sig E) {
	if err := save(t, prev, next, sig); err != nil {
		panic(&ActorSaveDefect{Reason: err.Error()})
	}
}

// ActorSaveDefect is the defect of a durable actor whose save failed:
// Reason is the save's failure.
type ActorSaveDefect struct {
	Reason string
}

// String is the defect's text.
func (d *ActorSaveDefect) String() string {
	return "actor.StartDurable: the save of a committed transition failed: " + d.Reason
}

// ActorChildDefect is the defect of an actor.Child called where no entry
// owns it: outside a machine's work (in a step, or on a task the program
// started elsewhere), or with a map whose result is not the entry's
// signal type, which only a send of another machine passed to Child can
// give.
type ActorChildDefect struct {
	Site string
}

// String is the defect's text.
func (d *ActorChildDefect) String() string {
	return "actor.Child at " + d.Site + ": no machine entry owns this call; call Child in a machine's work, with that work's send"
}

// machineStart starts an actor; report is the parent entry's for a child
// (rule 13), whose task then starts shielded from t's interruption, and
// nil otherwise.
func machineStart[S, E, D, W any](span Site, t *rt.Task, sl Sleeper, initial S,
	step func(*rt.Task, S, E) (D, error), fire func(*rt.Task, S, S) (D, error),
	delay func(S) time.Duration, workOf func(S) W, hasWork func(W) bool,
	work func(*rt.Task, W, func(E)), kind func(D) int, next func(S, D) S,
	out func(*rt.Task, D), capacity int, report func(*rt.Task, error) bool,
	fresh func(*rt.Task) (S, error), limit int) *Actor[S, E, D] {
	if capacity < 0 {
		panic(fmt.Sprintf("actor.StartMachine: negative capacity %d", capacity))
	}
	a := &Actor[S, E, D]{
		sleeper: sl, span: string(span),
		step: step, fire: fire, delay: delay, kind: kind, next: next, out: out,
		done: make(chan struct{}), state: initial,
		report: report, fresh: fresh, limit: limit,
	}
	a.work = func(s S) (func(*rt.Task, func(E)), bool) {
		w := workOf(s)
		if !hasWork(w) {
			return nil, false
		}
		return func(t *rt.Task, send func(E)) { work(t, w, send) }, true
	}
	holder := rt.NewScope(t)
	a.inbox.init(capacity, actorReference)
	a.ready.Add(1)
	if report != nil {
		// A child: t's interruption does not reach it; its owner entry
		// stops it, in order (rule 13).
		rt.GoShielded(&holder, a.span, func(at *rt.Task) (struct{}, error) {
			return a.endRunner(guarded(at, a.runner))
		})
	} else {
		rt.Go(&holder, a.span, func(at *rt.Task) (struct{}, error) {
			return a.endRunner(guarded(at, a.runner))
		})
	}
	if rt.Exploring {
		rt.Await(t, a.isReady.Load)
	}
	a.ready.Wait()
	return a
}

// isClosedTest is a test of ch for rt.Await: whether it is closed.
func isClosedTest(ch <-chan struct{}) func() bool {
	return func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
}

// started tells machineStart that the first entry's children run, once.
func (a *Actor[S, E, D]) started() {
	if a.isReady.CompareAndSwap(false, true) {
		a.ready.Done()
	}
}

// runner is the runner task's body, run by guarded: the loop, on a task
// that owns no entry, so a Child in a step finds none (rule 13), also
// when the actor is a child started in a work.
func (a *Actor[S, E, D]) runner(at *rt.Task) (struct{}, error) {
	a.task = at
	a.inbox.bindTaker(at)
	// Only an actor below a work's task binds nil: elsewhere the first of
	// the task and its ancestors in owners is none or nil already, and no
	// ancestor is added later (a work's task is in owners before it starts
	// a task), so ownerOf gives nil anyway (MU18: no map entry per
	// top-level actor).
	if ownerOf(at) != nil {
		owners.Store(at, nil)
		a.bound = true
	}
	// The binding NewHost reads (rule 15): its storage is in the Actor, so
	// it allocates nothing, and it lives as long as the runner's task.
	at.Bind(&a.hostSlot, hostKey{}, hostRunner(a))
	if a.fresh == nil {
		return a.loop(at)
	}
	return a.supervise(at)
}

// supervise runs a supervised actor's incarnations (rule 16): each one is
// a loop, and one that ends with a restartable defect while admission is
// open is cleaned up (crash) and followed by the next, from a fresh state.
func (a *Actor[S, E, D]) supervise(at *rt.Task) (struct{}, error) {
	for runs := 1; ; runs++ {
		_, err := guarded(at, func(at *rt.Task) (struct{}, error) {
			if runs > 1 {
				a.renew(at)
			}
			return a.loop(at)
		})
		if !restartable(at, err, runs, a.limit) || a.inbox.isClosed() {
			return struct{}{}, err
		}
		a.crash(at)
		a.restarts.Add(1)
	}
}

// renew gives a new incarnation its first state, fresh's, never the last
// one: it is committed and shown to the observers before the loop opens
// its entry.
func (a *Actor[S, E, D]) renew(at *rt.Task) {
	s, err := a.fresh(at)
	if err != nil {
		panic(&ActorFreshDefect{Reason: err.Error()})
	}
	a.publish(s)
}

// crash ends an incarnation that a defect ended (rule 16) as finish ends
// the actor, but leaves admission open: the step's hosts close, the
// children, the timer and the work stop (their defects are the crash's),
// the ask of the step and the queued asks are answered ActorStopped, the
// queued sends and signals are dropped, the queued State reads get the
// last state, and the queued Feeds start. Envelopes admitted after it
// counted the queue are the next incarnation's. The epoch moves on, so no
// signal of the ended incarnation's entries can be stepped.
func (a *Actor[S, E, D]) crash(at *rt.Task) {
	a.closeNewHosts()
	_ = a.stopKids()
	for _, h := range []*rt.Handle[struct{}]{a.timer, a.working} {
		if h != nil {
			_ = h.Stop()
		}
	}
	a.timer, a.working = nil, nil
	if a.cur != nil {
		a.release(a.cur)
		a.cur = nil
	}
	for n := a.inbox.Len(); n > 0; n-- {
		env, ok := a.inbox.Poll()
		if !ok {
			break
		}
		switch env.kind {
		case feedStart:
			a.startFeed(at, env.epoch)
		case stateRead:
			a.mu.Lock()
			s := a.state
			a.mu.Unlock()
			env.reply.give(answer[S, D]{s: s})
		default:
			a.release(&env)
		}
	}
	a.epoch++
}

// endRunner ends the runner task after its loop returned or raised.
func (a *Actor[S, E, D]) endRunner(_ struct{}, err error) (struct{}, error) {
	a.started() // the first entry's arm or work raised
	a.finish(err)
	a.inbox.unbindTaker()
	if a.bound {
		owners.Delete(a.task)
	}
	return struct{}{}, nil
}

// inboxExit is the runner's exit when its take fails: a stop while it
// waited is the interruption, the exit guarded gives a raised one, with no
// panic; a closed and empty inbox is a normal end. It is a call of its
// own, so loop's frame stays as small as before (an idle runner's stack
// fits 2 KB).
//
//go:noinline
func inboxExit(err error) error {
	if err == rt.Interrupted {
		return err
	}
	return nil
}

// loop is the runner (rules 1 to 7 and 10). a.started tells MachineStart
// that the first entry's children run.
func (a *Actor[S, E, D]) loop(at *rt.Task) (struct{}, error) {
	s := a.state
	if at.Traced() {
		a.traceStart(at, s)
	}
	a.openKids()
	a.arm(at, s)
	a.startWork(at, s)
	if at.Traced() {
		a.traceArmed(at, s)
	}
	a.started()
	for {
		// The envelope is taken into curEnv: one copy, no allocation; the
		// inbox clears it before it waits.
		if err := a.inbox.popInto(at, &a.curEnv); err != nil {
			return struct{}{}, inboxExit(err)
		}
		a.cur = &a.curEnv
		var end bool
		var err error
		s, end, err = a.handle(at, s, a.cur)
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
		env.reply.give(answer[S, D]{s: s})
		return s, false, nil
	case workDefect:
		return s, true, env.err
	case feedStart:
		a.startFeed(at, env.epoch)
		return s, false, nil
	}
	// A timer's, a work's and a child's signal carry their entry's epoch:
	// one of an ended entry is stale, dropped and counted (rule 6). A
	// feedStart's epoch is its source's id, and a send's or an ask's is 0.
	tagged := env.kind == timerSignal || env.kind == workSignal || env.kind == childSignal
	if tagged && env.epoch != a.epoch {
		a.drop(at)
		return s, false, nil
	}
	if at.Traced() {
		a.traceTake(at, s)
	}
	if env.kind == timerSignal {
		d, err = a.fire(at, env.at, s)
	} else {
		d, err = a.step(at, s, env.sig)
	}
	if at.Traced() {
		a.traceStep(at, s, d, err)
	}
	if err != nil {
		a.closeNewHosts()
		if env.kind == askSignal {
			a.cur = nil
			env.reply.give(answer[S, D]{err: err})
			return s, false, nil
		}
		return s, true, err
	}
	k := a.kind(d)
	// The step's hosts (rule 15): an Enter's and an Emit's are bound in
	// open, a Stay's to the current entry; any other decision enters no
	// state, so they close.
	if len(a.newHosts) > 0 {
		switch k {
		case decideStay:
			a.bindNewHosts()
		case decideIgnore, decideReject, decideDone:
			a.closeNewHosts()
		}
	}
	if k == decideStay {
		// One lock for the decision and the commit (MU18).
		s = a.next(s, d)
		a.mu.Lock()
		a.latest, a.hasLatest = d, true
		a.publishLocked(s)
		a.mu.Unlock()
		a.cur = nil
		if env.kind == askSignal {
			env.reply.give(answer[S, D]{d: d})
		}
		return s, false, nil
	}
	// The output goes first (rule 17): the sink has it before the runner
	// acts on the decision, so before any observer can see the decision
	// or the state it made (Latest, Changes, State, the answer, the exit).
	if k == decideEmit || k == decideDone {
		a.out(at, d)
	}
	a.mu.Lock()
	a.latest, a.hasLatest = d, true
	if k == decideDone {
		a.final, a.finished = d, true
	}
	a.mu.Unlock()
	if k == decideEnter || k == decideEmit {
		if err := a.stopKids(); err != nil {
			raise(err)
		}
		a.stopTimer()
		a.stopWork()
		s = a.next(s, d)
		a.epoch++
		a.open(at, s)
	}
	a.cur = nil
	if env.kind == askSignal {
		env.reply.give(answer[S, D]{d: d})
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
	a.openKids()
	a.bindNewHosts()
	a.arm(at, s)
	if settled := a.startWork(at, s); settled != nil {
		// The entry has hosts and a work (rule 15): it is published once
		// the work filled them, reached its first wait, or ended.
		if rt.Exploring {
			rt.Await(at, isClosedTest(settled))
		}
		<-settled
	}
	opened = true
	if at.Traced() {
		a.traceArmed(at, s)
	}
	a.publish(s)
}

// startWork starts the work of the entry that opened in s, when s has one
// (rule 10). Its send pushes a work signal with the entry's epoch on the
// work's task, so an interrupted work raises at the push and sends
// nothing. Its defect is pushed as a workDefect envelope, never dropped;
// when the push cannot be made (admission closed, or the work is being
// stopped), the defect is the work's exit, which stopWork or finish
// raises.
//
// When the entry has hosts that are not filled (rule 15), it returns a
// channel that is closed once the work filled them, reached its first
// wait (rt.WhenWaiting), or ended, and nil otherwise.
func (a *Actor[S, E, D]) startWork(at *rt.Task, s S) <-chan struct{} {
	run, ok := a.work(s)
	if !ok {
		return nil
	}
	epoch := a.epoch
	var settled chan struct{}
	var settle func()
	a.kidsMu.Lock()
	a.kidsDone = make(chan struct{})
	done := a.kidsDone
	if a.hostsLeft > 0 {
		settled = make(chan struct{})
		var once sync.Once
		settle = func() { once.Do(func() { close(settled) }) }
		a.settle = settle
	}
	a.kidsMu.Unlock()
	owner := &entryRef[S, E, D]{a: a, epoch: epoch, done: done}
	holder := rt.NewScope(at)
	// Shielded from the runner's interruption (a Stop): the runner's end
	// stops the entry's children first and then the work (rule 13).
	a.working = rt.GoShielded(&holder, a.span+" (work)", func(wt *rt.Task) (struct{}, error) {
		// The entry the work's Child calls find (rule 13).
		owners.Store(wt, owner)
		defer owners.Delete(wt)
		if wt.Traced() {
			a.traceWork(wt, rt.MachineWorkStarted, epoch)
		}
		// A work that started children ends after their stop (rule 13).
		defer owner.waitKids()
		if settle != nil {
			// The runner publishes the entry at the work's first wait, or
			// at its end, whichever comes first (rule 15); this defer runs
			// before waitKids waits.
			cancel := rt.WhenWaiting(wt, settle)
			defer func() {
				cancel()
				settle()
			}()
		}
		send := func(e E) {
			// Closed: the actor ended; the work has no one to tell.
			_ = a.inbox.Push(wt, envelope[S, E, D]{kind: workSignal, sig: e, epoch: epoch})
		}
		_, err := guarded(wt, func(wt *rt.Task) (struct{}, error) {
			run(wt, send)
			return struct{}{}, nil
		})
		if wt.Traced() {
			a.traceWork(wt, workEnd(err), epoch)
		}
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
	return settled
}

// owners maps the task of a running work to its entry, and the task of a
// running actor's runner to nil (rule 13). actor.Child finds its entry
// from its hidden task: the first of the task and its ancestors that is
// in the map. A step's Child (on a runner's task, or below it) finds nil.
// It is keyed by task identity, holds an entry only while its work runs,
// and no program code reads it: the program names no owner. It is not a
// context value, which a defect's report would print.
var owners sync.Map // *rt.Task -> any: a childOwner of the entry's signal type, or nil

// ownerOf is the entry that owns a Child called on t, nil when none does.
func ownerOf(t *rt.Task) any {
	for x := t; x != nil; x = x.Parent() {
		if v, ok := owners.Load(x); ok {
			return v
		}
	}
	return nil
}

// childOwner is an entry that takes children whose outputs are signals
// P: entryRef[S, P, D] is one.
type childOwner[P any] interface {
	// adopt runs start, which starts a child with the entry's report and
	// deliver and gives its halt, and keeps the child in the entry's
	// list; false, with nothing started, when the entry no longer takes
	// children (its exit began). With a host h (rule 15), it starts the
	// child only when h is an empty host of this entry, and then fills it
	// (fill) under the same lock, so the entry's end, which closes its
	// hosts, comes before the check or after the fill; hostOK is false,
	// with nothing started, when h is not one.
	adopt(h hostCell, start func(report func(*rt.Task, error) bool, deliver func(*rt.Task, P)) func() error, fill func()) (adopted, hostOK bool)
}

// entryRef is one entry of an actor, as its work's Child calls see it.
type entryRef[S, E, D any] struct {
	a       *Actor[S, E, D]
	epoch   uint64
	done    chan struct{} // the entry's kidsDone
	adopted atomic.Bool
}

// kid is a child an entry keeps: halt stops it and waits for its end, and
// gives the defects it has not reported to the parent yet.
type kid struct {
	halt func() error
}

func (r *entryRef[S, E, D]) adopt(h hostCell, start func(func(*rt.Task, error) bool, func(*rt.Task, E)) func() error, fill func()) (bool, bool) {
	a := r.a
	a.kidsMu.Lock()
	defer a.kidsMu.Unlock()
	if rt.Exploring {
		a.adopting.Store(true)
		defer a.adopting.Store(false)
	}
	if !a.kidsOpen || a.kidsEpoch != r.epoch {
		return false, true
	}
	if h != nil && !h.fillable(a) {
		return false, false
	}
	halt := start(r.report, r.deliver)
	if a.task.Traced() {
		// child-started now, and child-stopped from the halt: a hook in
		// stopKids would grow the runner's frame.
		halt = a.traceChild(halt, r.epoch)
	}
	a.kids = append(a.kids, &kid{halt: halt})
	r.adopted.Store(true)
	if h != nil {
		fill()
		a.filledHostLocked()
	}
	return true, true
}

// deliver pushes the child output p as a signal of the entry, on the
// child's task t (the child's runner): it waits for room as a Send does,
// and the child's stop interrupts the wait.
func (r *entryRef[S, E, D]) deliver(t *rt.Task, p E) {
	// Closed: the parent ended; the child has no one to tell.
	_ = r.a.inbox.Push(t, envelope[S, E, D]{kind: childSignal, sig: p, epoch: r.epoch})
}

// report pushes a child's defect into the parent's inbox on the child's
// task t; false when it could not (the parent's admission closed, or the
// child is being stopped), and then the stop raises it.
func (r *entryRef[S, E, D]) report(t *rt.Task, err error) bool {
	_, pushed := guarded(t, func(t *rt.Task) (struct{}, error) {
		return struct{}{}, r.a.inbox.Push(t, envelope[S, E, D]{kind: workDefect, err: err})
	})
	return pushed == nil
}

// waitKids waits, uninterruptibly, until the entry's children are stopped,
// when the entry has any. The runner stops them before it stops the work,
// so the wait ends.
func (r *entryRef[S, E, D]) waitKids() {
	if r.adopted.Load() {
		if rt.Exploring {
			rt.Await(nil, isClosedTest(r.done))
		}
		<-r.done
	}
}

// openKids makes the entry of a.epoch the one that takes children.
func (a *Actor[S, E, D]) openKids() {
	a.kidsMu.Lock()
	a.kids, a.kidsEpoch, a.kidsOpen = nil, a.epoch, true
	a.kidsDone = nil // made by startWork: only a work starts children (MU18)
	a.kidsMu.Unlock()
}

// stopKids ends the open entry's children (rule 13): it closes the entry
// to new children, waiting for a start in progress, then stops each child
// and waits for its end, last started first. It gives the defects of the
// children that they did not report.
func (a *Actor[S, E, D]) stopKids() error {
	if rt.Exploring {
		rt.Await(nil, func() bool { return !a.adopting.Load() })
	}
	a.kidsMu.Lock()
	ks, done := a.kids, a.kidsDone
	a.kids, a.kidsOpen, a.kidsDone = nil, false, nil
	// The entry ends: its hosts close first (rule 15), so a Get fails
	// Stopped from here on and no fill comes after.
	a.closeHostsLocked()
	a.kidsMu.Unlock()
	var exit error
	for i := len(ks) - 1; i >= 0; i-- {
		if err := ks[i].halt(); err != nil {
			exit = join(exit, err)
		}
	}
	if done != nil {
		close(done)
	}
	return exit
}

// halt stops a child for its owner: Stop's interruption and wait, with
// none of Stop's results: a typed failure ends only the child, and a
// defect the child reported is not given again.
func (a *Actor[S, E, D]) halt() error {
	a.task.Interrupt()
	a.inbox.Close()
	if rt.Exploring {
		rt.Await(nil, isClosedTest(a.done))
	}
	<-a.done
	if a.reported.Load() {
		return nil
	}
	a.mu.Lock()
	exit := a.exit
	a.mu.Unlock()
	return defectsOnly(exit)
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
//
// arm returns once the child's sleep is registered with the Scheduler (its
// first wait, rt.WhenWaiting) or the child has ended, so the sleep is part
// of the commit: an observer that sees the entry and then advances a
// manual clock by the delay always fires the timer (rule 7). The sleep's
// deadline is the commit's instant, not a later one.
func (a *Actor[S, E, D]) arm(at *rt.Task, s S) {
	d := a.delay(s)
	if d == noTimer {
		return
	}
	if d <= 0 {
		panic(MachineTimerNotPositive("", s, d))
	}
	env := envelope[S, E, D]{kind: timerSignal, epoch: a.epoch, at: s}
	holder := rt.NewScope(at)
	armed := make(chan struct{})
	var once sync.Once
	registered := func() { once.Do(func() { close(armed) }) }
	a.timer = rt.Go(&holder, a.span+" (timer)", func(ct *rt.Task) (struct{}, error) {
		cancel := rt.WhenWaiting(ct, registered)
		defer func() {
			cancel()
			registered() // the child ended without a wait
		}()
		return guarded(ct, func(ct *rt.Task) (struct{}, error) {
			a.sleeper.Sleep(ct, d)
			// Closed: the actor ended; the timer has no one to tell.
			_ = a.inbox.Push(ct, env)
			return struct{}{}, nil
		})
	})
	if rt.Exploring {
		rt.Await(at, isClosedTest(armed))
	}
	<-armed
}

// noTimer is the delay of a state with no timer: no timer waits it.
const noTimer = time.Duration(math.MinInt64)

// ActorNoTimer is the delay std actor gives a state whose After is None.
func ActorNoTimer() time.Duration { return noTimer }

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
	a.publishLocked(s)
	a.mu.Unlock()
}

// publishLocked is publish with a.mu held.
func (a *Actor[S, E, D]) publishLocked(s S) {
	a.state = s
	if len(a.subs) == 0 {
		return
	}
	for c := range a.subs {
		c.buf = append(c.buf, s)
		c.signal()
	}
}

// release answers env as an ended actor does: an Ask with ActorStopped, a
// State read with the last state and ActorStopped (State ignores it,
// ActorLiveState returns it); a Send, a timer and a Feed are dropped.
func (a *Actor[S, E, D]) release(env *envelope[S, E, D]) {
	switch env.kind {
	case askSignal:
		env.reply.give(answer[S, D]{err: ActorStopped{}})
	case stateRead:
		a.mu.Lock()
		s := a.state
		a.mu.Unlock()
		env.reply.give(answer[S, D]{s: s, err: ActorStopped{}})
	}
}

// feed is a started source (rule 14): its task, and over once its body
// returned with no exit to report, so the runner may forget it.
type feed struct {
	h    *rt.Handle[struct{}]
	over atomic.Bool
}

// ActorFeed is std actor's feed(a, run) on the caller's task t (rule 14):
// it admits run as a source of a, waiting for room as a Send does, and
// returns once it is queued. The runner then starts run on a task of its
// own, a child of the runner's, with send: send admits a signal as a Send
// does, in run's order, and raises rt.Interrupted once a has ended, so run
// ends there. It fails ActorStopped when a has ended or its Stop was
// requested; run then never runs. A stop of t while it waits raises
// rt.Interrupted.
func ActorFeed[S, E, D any](t *rt.Task, a *Actor[S, E, D], run func(*rt.Task, func(E))) error {
	a.feedMu.Lock()
	a.feedSeq++
	id := a.feedSeq
	if a.feedQ == nil {
		a.feedQ = map[uint64]func(*rt.Task, func(E)){}
	}
	a.feedQ[id] = run
	a.feedMu.Unlock()
	// The envelope names its source by id (in the epoch field, which a
	// feedStart does not use), so a Feed whose Push fails takes back its
	// own source and no other.
	if err := a.inbox.Push(t, envelope[S, E, D]{kind: feedStart, epoch: id}); err != nil {
		a.feedMu.Lock()
		delete(a.feedQ, id)
		a.feedMu.Unlock()
		return ActorStopped{}
	}
	return nil
}

// startFeed starts the source the feedStart envelope names on a child task
// of the runner's task at (rule 14). It first forgets the sources that
// ended.
func (a *Actor[S, E, D]) startFeed(at *rt.Task, id uint64) {
	a.feedMu.Lock()
	run := a.feedQ[id]
	delete(a.feedQ, id)
	a.feedMu.Unlock()
	kept := a.feeds[:0]
	for _, f := range a.feeds {
		if !f.over.Load() {
			kept = append(kept, f)
		}
	}
	clear(a.feeds[len(kept):])
	a.feeds = kept
	f := &feed{}
	holder := rt.NewScope(at)
	f.h = rt.Go(&holder, a.span+" (feed)", func(ft *rt.Task) (struct{}, error) {
		send := func(e E) {
			if a.inbox.Push(ft, envelope[S, E, D]{kind: sendSignal, sig: e}) != nil {
				// Closed: the actor ended, so the source ends here.
				panic(rt.Interrupted)
			}
		}
		_, err := guarded(ft, func(ft *rt.Task) (struct{}, error) {
			run(ft, send)
			return struct{}{}, nil
		})
		if err == nil || err == rt.Interrupted {
			f.over.Store(true)
			return struct{}{}, nil
		}
		// A defect of the source ends the actor (rule 10's rule): pushed,
		// never dropped, or, when admission has closed, the feed's exit,
		// which finish reads.
		_, pushed := guarded(ft, func(ft *rt.Task) (struct{}, error) {
			return struct{}{}, a.inbox.Push(ft, envelope[S, E, D]{kind: workDefect, err: err})
		})
		if pushed == nil {
			f.over.Store(true)
			return struct{}{}, nil
		}
		return struct{}{}, err
	})
	a.feeds = append(a.feeds, f)
}

// ActorLiveState is std actor's live(a) on the caller's task t: a State
// read that fails ActorStopped when a has ended before it was answered
// (std actor's Can reads it, MU10). It raises rt.Interrupted when t is
// stopped while it waits.
func ActorLiveState[S, E, D any](t *rt.Task, a *Actor[S, E, D]) (S, error) {
	reply := a.takeReply(t)
	if err := a.inbox.Push(t, envelope[S, E, D]{kind: stateRead, reply: reply}); err != nil {
		a.giveReply(reply)
		var zero S
		return zero, ActorStopped{}
	}
	r := a.await(t, reply)
	a.giveReply(reply)
	return r.s, r.err
}

// finish ends the actor (rule 8) on the runner's goroutine, after the loop
// returned or raised: admission closes, the timer child and the work are
// stopped and drained (a defect of the work joins the exit), the envelope
// of an interrupted step and every queued one are released (a queued work
// defect joins the exit), and the Changes subscribers end. It waits for nothing a stop can interrupt.
func (a *Actor[S, E, D]) finish(exit error) {
	a.inbox.Close()
	// The hosts a step that raised made (rule 15).
	a.closeNewHosts()
	// The children first, last started first (rule 13).
	if err := a.stopKids(); err != nil {
		exit = join(exit, err)
	}
	for _, h := range []*rt.Handle[struct{}]{a.timer, a.working} {
		if h == nil {
			continue
		}
		if err := h.Stop(); err != nil {
			exit = join(exit, err)
		}
	}
	a.timer, a.working = nil, nil
	// The sources last (rule 14): admission is closed, so each one's next
	// send raises; the stop interrupts one that waits.
	for _, f := range a.feeds {
		if err := f.h.Stop(); err != nil {
			exit = join(exit, err)
		}
	}
	a.feeds = nil
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
	// The waits for the end are unparked now, before done closes: early,
	// which only keeps a clock's domain unsettled until it closes (fua-b).
	for _, w := range a.doneWait {
		rt.Waking(w, a).Done()
	}
	a.mu.Unlock()
	if a.task.Traced() {
		a.traceExit()
	}
	// A child's defect ends its parent (rule 13): pushed now, or raised by
	// the parent's stop of the child when it cannot be pushed.
	if a.report != nil {
		if d := defectsOnly(exit); d != nil && a.report(a.task, d) {
			a.reported.Store(true)
		}
	}
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
	reply := a.takeReply(t)
	var zero D
	if err := a.inbox.Push(t, envelope[S, E, D]{kind: askSignal, sig: e, reply: reply}); err != nil {
		a.giveReply(reply) // never queued: no answer comes
		return zero, ActorStopped{}
	}
	r := a.await(t, reply)
	a.giveReply(reply)
	return r.d, r.err
}

// replySlot is a reply channel an Ask or a State read reuses (MU18). Every
// queued Ask and State read is answered exactly once (rules 1 and 8), so a
// channel whose answer was taken is empty and nobody else holds it; one
// whose wait was interrupted may still get its answer, so it is never
// given back.
// task is the asker's, so the runner can unpark it in a clock's domain
// before it answers (fua-b).
type replySlot[S, D any] struct {
	ch   chan answer[S, D]
	task *rt.Task
}

// give answers r's asker. In a clock's domain the asker is unparked first
// (rt idle.go), so no window shows it parked while it can run; the
// channel has room for the one answer, so the send never waits.
func (r *replySlot[S, D]) give(ans answer[S, D]) {
	k := rt.Waking(r.task, r)
	r.ch <- ans
	k.Done()
}

// takeReply is the actor's spare reply channel, or a new one when another
// asker holds it (or under the reference runner), for the asker t.
func (a *Actor[S, E, D]) takeReply(t *rt.Task) *replySlot[S, D] {
	if r := a.spare.Swap(nil); r != nil {
		r.task = t
		return r
	}
	return &replySlot[S, D]{ch: make(chan answer[S, D], 1), task: t}
}

// giveReply keeps r, empty, as the spare, unless another one is there.
func (a *Actor[S, E, D]) giveReply(r *replySlot[S, D]) {
	if a.inbox.ref == nil {
		a.spare.CompareAndSwap(nil, r)
	}
}

// State is a.State() on t: the state after every signal admitted before
// it, read in inbox order, so a sender's State follows its own Sends. On an
// ended actor it is the last committed state. It never fails; it raises
// rt.Interrupted when t is stopped while it waits.
func (a *Actor[S, E, D]) State(t *rt.Task) S {
	reply := a.takeReply(t)
	if err := a.inbox.Push(t, envelope[S, E, D]{kind: stateRead, reply: reply}); err != nil {
		a.giveReply(reply)
		// Ended or ending: the last state, once the runner has stopped.
		a.waitDone(t)
		a.mu.Lock()
		s := a.state
		a.mu.Unlock()
		return s
	}
	s := a.await(t, reply).s
	a.giveReply(reply)
	return s
}

// await waits for an answer on t: a stop of t raises rt.Interrupted.
// In a clock's domain it waits parked on reply (fua-b).
func (a *Actor[S, E, D]) await(t *rt.Task, reply *replySlot[S, D]) answer[S, D] {
	t.Check()
	var p rt.Parked
	if t.InDomain() {
		p = rt.Park(t, reply, t.Done(), func() bool { return len(reply.ch) > 0 })
	}
	rt.Block(t)
	select {
	case r := <-reply.ch:
		rt.Unblock(t)
		p.Unpark()
		return r
	case <-t.Done():
		rt.Unblock(t)
		p.Unpark()
		panic(rt.Interrupted)
	}
}

// waitDone waits on t until the runner has ended. In a clock's domain it
// waits parked on the actor until finish unparks it (fua-b). A stop of t
// ends the wait with rt.Interrupted.
func (a *Actor[S, E, D]) waitDone(t *rt.Task) { a.waitEnd(t, t.Done()) }

// waitEnd waits on t until the runner has ended or stop closes (nil: an
// uninterruptible wait, Stop's and Drain's). In a clock's domain t waits
// parked on the actor, and on stop when it is not nil, so a manual clock's
// Advance does not wait for t (batch-13 round 2, B1): finish unparks it.
func (a *Actor[S, E, D]) waitEnd(t *rt.Task, stop <-chan struct{}) {
	var p rt.Parked
	if t.InDomain() {
		a.mu.Lock()
		if !a.ended {
			a.doneWait = append(a.doneWait, t)
			p = rt.Park(t, a, stop, nil)
		}
		a.mu.Unlock()
	}
	rt.Block(t)
	select {
	case <-a.done:
	case <-stop:
		rt.Unblock(t)
		a.dropDoneWait(t, p)
		panic(rt.Interrupted)
	}
	rt.Unblock(t)
	a.dropDoneWait(t, p)
}

// dropDoneWait unparks t, a waitDone in a clock's domain, and removes it
// from doneWait.
func (a *Actor[S, E, D]) dropDoneWait(t *rt.Task, p rt.Parked) {
	if !t.InDomain() {
		return
	}
	p.Unpark()
	a.mu.Lock()
	for i, w := range a.doneWait {
		if w == t {
			a.doneWait = append(a.doneWait[:i], a.doneWait[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
}

// Stop is a.Stop(): it interrupts the runner, closes admission and waits,
// uninterruptibly, until it has ended, its timer and its work drained and
// its queued Asks answered ActorStopped (rule 8). It returns the typed
// failure that ended the actor, if one did, and raises its defects, a
// work's included (rule 10). A Stop of an ended
// actor returns the same at once; every Stop is idempotent. t, the
// caller's task, waits parked in a clock's domain (waitEnd).
func (a *Actor[S, E, D]) Stop(t *rt.Task) error {
	// The interruption comes first: once it is set, the runner's next Pop
	// raises at its check, so no envelope is taken after the request, also
	// one queued before it. A Send admitted between the two is queued
	// before the close and released (dropped) by finish.
	rt.Point(t, rt.Op{Kind: rt.OpStop, On: a})
	if a.task.Traced() {
		a.traceStop("stop")
	}
	a.task.Interrupt()
	a.inbox.Close()
	a.waitEnd(t, nil)
	return a.result()
}

// Drain is a.Drain() (rule 12): it closes admission, so later Sends and
// Asks fail ActorStopped, and waits, uninterruptibly, until the runner has
// stepped every signal queued before it, in order (its Asks answered),
// and then ended as Stop ends it. A Done, a failure or a defect among the
// queued signals ends the actor there, as it would without Drain. Its
// result is Stop's: the typed failure that ended the actor, its defect
// raised. A Stop during a Drain overtakes it: the rest is dropped. t, the
// caller's task, waits parked in a clock's domain (waitEnd).
func (a *Actor[S, E, D]) Drain(t *rt.Task) error {
	rt.Point(t, rt.Op{Kind: rt.OpClose, On: a})
	if a.task.Traced() {
		a.traceStop("drain")
	}
	a.inbox.Close()
	a.waitEnd(t, nil)
	return a.result()
}

// result is what Stop and Drain return once the runner has ended: the
// typed failure that ended it, nil, or its defect raised.
func (a *Actor[S, E, D]) result() error {
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

// The kinds of an actor's exit (rule 12), in the order of std actor's
// Exit variants.
const (
	exitFinished = iota // Done ended it
	exitHalted          // Stop, Drain, or a stop of its parent ended it
	exitFailed          // a step's typed failure after a Send or a timer
	exitDefected        // a defect (of a step, a timer or a work)
)

// ActorExitKind is std actor's exitKind(a) on the caller's task t: it
// waits until the runner has ended and gives how (exitFinished …
// exitDefected). Every caller gets the same answer, and so does Stop (one
// exit, rule 12). A stop of t raises rt.Interrupted.
func ActorExitKind[S, E, D any](t *rt.Task, a *Actor[S, E, D]) int {
	a.waitDone(t)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exitKindLocked()
}

// exitKindLocked is how the ended actor ended, with a.mu held.
func (a *Actor[S, E, D]) exitKindLocked() int {
	if a.exit != nil && a.exit != rt.Interrupted {
		// The order of result: Stop returns the failure, else raises the
		// defect, also after a Done.
		if _, ok := rt.Failure(a.exit); ok {
			return exitFailed
		}
		if defectsOnly(a.exit) != nil {
			return exitDefected
		}
	}
	if a.finished {
		return exitFinished
	}
	return exitHalted
}

// ActorFinal is std actor's final(a): the Done decision that ended a, read
// after ActorExitKind gave exitFinished.
func ActorFinal[S, E, D any](a *Actor[S, E, D]) D {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.final
}

// ActorFailure is std actor's failure(a): the typed failure that ended a,
// read after ActorExitKind gave exitFailed.
func ActorFailure[S, E, D any](a *Actor[S, E, D]) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if f, ok := rt.Failure(a.exit); ok {
		return f
	}
	return nil
}

// ActorHasLatest is std actor's hasLatest(a): whether a has stepped a
// signal. Once true it stays true, so ActorLatest after it has a decision.
func ActorHasLatest[S, E, D any](a *Actor[S, E, D]) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hasLatest
}

// ActorLatest is std actor's latest(a): the decision of the last signal a
// stepped (rule 12; a Reject and an Ignore included, a dropped signal
// not).
func ActorLatest[S, E, D any](a *Actor[S, E, D]) D {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.latest
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
		if a.subs == nil {
			a.subs = map[*changes[S]]struct{}{}
		}
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
	// task is the subscriber while it waits in a clock's domain (fua-b).
	task *rt.Task
}

// signal wakes the subscriber's wait; in a clock's domain it is unparked
// first (rt idle.go). The actor's mu is held.
func (c *changes[S]) signal() {
	if c.wake != nil {
		if c.task != nil {
			rt.Waking(c.task, c).Done()
		}
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
		// In a clock's domain the subscriber parks under a.mu, which
		// signal holds too (fua-b).
		var p rt.Parked
		if t.InDomain() {
			c.task = t
			p = rt.Park(t, c, t.Done(), nil)
		}
		a.mu.Unlock()
		rt.Block(t)
		select {
		case <-ch:
		case <-t.Done():
			rt.Unblock(t)
			p.Unpark()
			panic(rt.Interrupted)
		}
		rt.Unblock(t)
		a.mu.Lock()
		p.Unpark()
		c.task = nil
	}
}
