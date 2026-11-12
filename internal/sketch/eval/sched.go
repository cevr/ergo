package eval

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Tasks at check time (ORACLE O3; runtime.md, Tasks, I1; stdlib.md,
// Testing). The obligation's body is the root task. task.Go, a tasks.Set
// member and the like start a task of their own, and every task runs on
// one evaluator thread: a cooperative scheduler hands a baton from task
// to task, so exactly one runs at any time and every evaluator structure
// is touched by one goroutine at a time. A task runs until it waits (a
// Wait, a queue's Push or Pop, a Sleep, an Advance settling the others)
// or reaches a loop's back-edge while another task can run; then the
// scheduler picks the next task by the obligation's schedule seed, from
// the tasks that can go on. So one seed is one schedule, run again the
// same way; the schedules a program may take stay with ergo test
// --explore. Time is the clocks' own: a time.Manual moves only by its
// Advance, a testing.VirtualClock moves to its next sleeper's deadline
// when no task can go on. When no task can go on and no clock can move,
// the obligation ends as a defect naming what each task waits for.
//
// A stop is cooperative: a stopped task ends at its next interruption
// point (I1: a capability call or a loop's back-edge in an impure frame,
// a recursive or opaque frame's entry, each blocking operation), unless
// it runs masked (a release, I2). A task's exit is its body's value or
// a cause, with the stop winning over a value (Exit causes 5).
//
// An exhausted budget, a form the evaluator does not run and an internal
// error end the whole obligation: every other task unwinds without
// running anything more (ctlAbort), and the obligation's result is that
// first stop.

// taskState is the part of the interpreter that belongs to the running
// task; the scheduler saves and restores it at each switch.
type taskState struct {
	depth int
	// ctxv are the context values in force for the running task,
	// innermost first (contextvalues.go).
	ctxv   *ctxBinding
	ranges []*sliceRange
	cur    *etask
}

// etask is one task of the obligation.
type etask struct {
	id     int
	site   string // what started it, for a deadlock's report
	parent *etask // the task that started it, while both run (rule 1)
	kids   []*etask
	wake   chan struct{}
	state  taskRun
	ready  func() bool // a blocked task goes on when it holds
	why    string      // what a blocked task waits for
	// stopped marks a task asked to stop; mask counts the releases it is
	// in, where a stop waits (I2).
	stopped  bool
	mask     int
	settling bool     // an Advance waiting for the other tasks to wait
	sleep    *sleeper // the sleep a blocked task waits on, if any
	saved    taskState
	// Its exit: the body's value, or the cause (nil for a value). ended
	// says the body returned val, also when a stop won (Gap 47).
	val   Value
	exit  *abrupt
	ended bool
	start func(t *etask) (Value, *abrupt)
	// The actor whose runner the task is, and the entry whose work it
	// runs (runner.go): actor.Child finds its owner entry from the
	// running task and its ancestors, the first that is either.
	runs  *actorV
	entry *entryRef
	// recov collects the hosts a durable recover makes on the task (and
	// the tasks it starts); handoff holds them for the first actor the
	// task starts after (runnerdurable.go).
	recov   *recovery
	handoff *[]*hostV
}

type taskRun int

const (
	runnable taskRun = iota
	running
	blocked
	done
)

// scheduler runs the tasks of one obligation's case.
type scheduler struct {
	seed   uint64
	rng    *rand.Rand
	root   *etask
	tasks  []*etask // the tasks that have not ended, in start order
	nextID int

	// fatal is the stop that ends the obligation, panicked an internal
	// error raised on another task's goroutine.
	fatal    *abrupt
	panicked any
	switches int64 // tasks resumed, for the census
}

// scheduleSeed is the seed of an obligation's case schedule: the run's
// seed, the obligation's name and the case's number, never 0.
func scheduleSeed(seed uint64, name string, k int) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d\x00%s\x00%d", seed, name, k)
	return h.Sum64() | 1
}

// newScheduler makes the scheduler of a case whose body runs as the root
// task on the calling goroutine.
func (in *interp) newScheduler(seed uint64) *scheduler {
	s := &scheduler{seed: seed, rng: rand.New(rand.NewPCG(seed, 0x4f7261636c65))}
	s.root = &etask{id: 1, site: "the obligation", wake: make(chan struct{}), state: running}
	s.nextID = 2
	s.tasks = []*etask{s.root}
	return s
}

// interrupt is the exit of a task stopped at an interruption point.
func (fr *frame) interrupt(at syntax.Node) *abrupt {
	return fr.abrupt(ctlInterrupt, at, nil, "interrupted")
}

// abort is the exit every task takes once the obligation has ended.
func (fr *frame) abort(at syntax.Node) *abrupt {
	return fr.abrupt(ctlAbort, at, nil, "the obligation ended")
}

// mask enters (n = 1) or leaves (n = -1) a masked region of the running
// task: a release runs to its end (I2).
func (in *interp) mask(n int) {
	if in.cur != nil {
		in.cur.mask += n
	}
}

// stoppedNow reports a stop the running task observes at an interruption
// point.
func (in *interp) stoppedNow() bool {
	return in.cur != nil && in.cur.stopped && in.cur.mask == 0
}

// polls reports a frame whose interruption points are emitted: its
// footprint is not empty (I1 4). A frame with no recorded footprint
// (a context entry's) has none.
func (fr *frame) polls() bool {
	fp, ok := fr.info.Footprints[fr.key]
	return ok && !fp.Empty()
}

// poll is an interruption point of fr (a capability call, a loop's
// back-edge): the running task stops there when asked to.
func (in *interp) poll(fr *frame, at syntax.Node) *abrupt {
	if in.stoppedNow() && fr.polls() {
		return fr.interrupt(at)
	}
	return nil
}

// entryPoll is the interruption point at a recursive or opaque impure
// frame's entry (Footprint.EntryPoll).
func (in *interp) entryPoll(fr *frame) *abrupt {
	if !in.stoppedNow() {
		return nil
	}
	if fp, ok := fr.info.Footprints[fr.key]; ok && fp.EntryPoll {
		return fr.interrupt(fr.key)
	}
	return nil
}

// backEdge ends a loop's iteration: its interruption point, then, while
// another task can go on, a turn the schedule may give to it.
func (in *interp) backEdge(fr *frame, at syntax.Node) *abrupt {
	if ab := in.poll(fr, at); ab != nil {
		return ab
	}
	s := in.sched
	if s == nil || len(s.tasks) < 2 || s.rng.IntN(2) == 0 {
		return nil
	}
	return in.park(fr, at)
}

// block waits until ready holds: an interruption point, then a wait that
// lets the other tasks run. A stop the task observes ends the wait first,
// so a stopped wait takes nothing (I1 2, 5).
func (in *interp) block(fr *frame, at syntax.Node, why string, ready func() bool) *abrupt {
	s := in.sched
	for {
		if s != nil && s.fatal != nil {
			return fr.abort(at)
		}
		if in.stoppedNow() {
			return fr.interrupt(at)
		}
		if ready() {
			return nil
		}
		if s == nil {
			return fr.unsupported(at, "a wait outside an obligation's body (%s)", why)
		}
		cur := in.cur
		cur.state, cur.ready, cur.why = blocked, ready, why
		ab := in.park(fr, at)
		cur.ready, cur.why = nil, ""
		if ab != nil {
			return ab
		}
	}
}

// settle lets every other task run until each waits: an Advance returns
// once the tasks it woke have run to their next wait (rt.AdvanceSettled).
func (in *interp) settle(fr *frame, at syntax.Node) *abrupt {
	s := in.sched
	if s == nil {
		return nil
	}
	cur := in.cur
	for s.others(cur) {
		cur.settling = true
		ab := in.park(fr, at)
		cur.settling = false
		if ab != nil {
			return ab
		}
	}
	return nil
}

// others reports a task other than cur that can go on.
func (s *scheduler) others(cur *etask) bool {
	for _, t := range s.tasks {
		if t != cur && t.canRun() {
			return true
		}
	}
	return false
}

// canRun reports a task the scheduler may give the baton: runnable, or
// blocked on a wait that can end (its event, or a stop it observes).
func (t *etask) canRun() bool {
	switch t.state {
	case runnable:
		return true
	case blocked:
		return t.ready() || t.stopped && t.mask == 0
	}
	return false
}

// park gives the baton to the task the schedule picks, which may be the
// running one, and returns when the running task has it again. The
// running task stays runnable unless it set itself blocked.
func (in *interp) park(fr *frame, at syntax.Node) *abrupt {
	s := in.sched
	cur := in.cur
	if cur.state == running {
		cur.state = runnable
	}
	next := in.pick(fr, at)
	if next == nil {
		cur.state = running
		return fr.abort(at)
	}
	next.state = running
	if next != cur {
		cur.saved = in.taskState
		next.wake <- struct{}{}
		<-cur.wake
		in.taskState = cur.saved
	}
	if s.fatal != nil {
		return fr.abort(at)
	}
	return nil
}

// pick chooses the next task by the schedule: one of those that can go
// on, a task settling an Advance only when no other can; else it moves
// a virtual clock to its next deadline; else every task waits and the
// obligation ends as a defect. It returns nil once the obligation has
// ended (s.fatal), with the root to resume.
func (in *interp) pick(fr *frame, at syntax.Node) *etask {
	s := in.sched
	for s.fatal == nil {
		var can, settling []*etask
		for _, t := range s.tasks {
			if !t.canRun() {
				continue
			}
			if t.settling {
				settling = append(settling, t)
			} else {
				can = append(can, t)
			}
		}
		if len(can) == 0 {
			can = settling
		}
		if len(can) > 0 {
			s.switches++
			if len(can) == 1 {
				return can[0]
			}
			return can[s.rng.IntN(len(can))]
		}
		if s.idle() {
			continue
		}
		s.fatal = fr.defect(at, "%s", s.deadlock())
	}
	return nil
}

// deadlock is the report of a schedule in which every task waits.
func (s *scheduler) deadlock() string {
	var b strings.Builder
	b.WriteString("every task waits and no clock can move:")
	for _, t := range s.tasks {
		fmt.Fprintf(&b, " task #%d (%s) waits for %s;", t.id, t.site, t.why)
	}
	return strings.TrimSuffix(b.String(), ";")
}

// start makes a task below the running one that runs body once the
// schedule picks it, and returns it at once (rule 7: the block runs
// later). Its goroutine waits for the baton before it touches anything.
func (in *interp) start(site string, body func(t *etask) (Value, *abrupt)) *etask {
	s := in.sched
	parent := in.cur
	t := &etask{id: s.nextID, site: site, parent: parent, wake: make(chan struct{}), state: runnable, start: body}
	s.nextID++
	t.saved = taskState{ctxv: in.ctxv, cur: t}
	parent.kids = append(parent.kids, t)
	if parent.stopped {
		t.stopped = true // a stop of the parent reaches every task below it
	}
	s.tasks = append(s.tasks, t)
	go in.runTask(t)
	return t
}

// runTask is a task's goroutine: it waits for the baton, runs the body,
// records the exit and hands the baton on.
func (in *interp) runTask(t *etask) {
	s := in.sched
	<-t.wake
	in.taskState = t.saved
	defer func() {
		if r := recover(); r != nil {
			// An internal error on this goroutine: the root raises it.
			if s.panicked == nil {
				s.panicked = r
			}
			if s.fatal == nil {
				s.fatal = &abrupt{kind: ctlAbort, msg: "an internal error"}
			}
			in.finish(t)
		}
	}()
	if s.fatal == nil {
		t.val, t.exit = t.start(t)
		if t.exit == nil {
			t.ended = true
			if t.stopped {
				// A stop wins over the value (Exit causes 5); the value
				// stays for the owner's release (Tasks 12).
				t.exit = &abrupt{kind: ctlInterrupt, msg: "interrupted"}
			}
		} else if t.stopped && (t.exit.kind == ctlFail || t.exit.kind == ctlEscape) {
			// A failure beside the stop is no lone typed failure.
			t.exit = &abrupt{kind: ctlInterrupt, msg: "interrupted"}
		}
		if stops(t.exit) && s.fatal == nil {
			s.fatal = t.exit
		}
	}
	in.finish(t)
}

// finish ends t, detaches it from its parent, and hands the baton to the
// next task: once the obligation has ended, back to the root.
func (in *interp) finish(t *etask) {
	s := in.sched
	t.state = done
	for i, x := range s.tasks {
		if x == t {
			s.tasks = append(s.tasks[:i:i], s.tasks[i+1:]...)
			break
		}
	}
	if p := t.parent; p != nil {
		for i, k := range p.kids {
			if k == t {
				p.kids = append(p.kids[:i:i], p.kids[i+1:]...)
				break
			}
		}
		t.parent = nil
	}
	var next *etask
	if s.fatal == nil {
		fr := &frame{info: in.root.Info}
		next = in.pick(fr, nil)
	}
	if next == nil {
		next = s.root
	}
	next.state = running
	next.wake <- struct{}{}
}

// stopTree asks t and every task below it to stop (rule 1).
func stopTree(t *etask) {
	t.stopped = true
	for _, k := range t.kids {
		stopTree(k)
	}
}

// waitDone waits, uninterruptibly, until t has ended (a Stop or a
// release drains it, law R5).
func (in *interp) waitDone(fr *frame, at syntax.Node, t *etask, why string) *abrupt {
	in.mask(1)
	defer in.mask(-1)
	return in.block(fr, at, why, func() bool { return t.state == done })
}

// shutdown ends the case's scheduler after the root's body: every task
// still running unwinds (ctlAbort), one at a time, each handing the
// baton back to the root.
func (in *interp) shutdown() {
	s := in.sched
	if s == nil {
		return
	}
	defer func() { in.sched = nil; in.taskState = taskState{} }()
	if len(s.tasks) > 1 && s.fatal == nil {
		s.fatal = &abrupt{kind: ctlAbort, msg: "the obligation ended"}
	}
	for len(s.tasks) > 1 {
		var t *etask
		for _, x := range s.tasks {
			if x != s.root {
				t = x
				break
			}
		}
		s.root.saved = in.taskState
		t.state = running
		t.wake <- struct{}{}
		<-s.root.wake
		in.taskState = s.root.saved
	}
	if s.panicked != nil {
		panic(s.panicked)
	}
}
