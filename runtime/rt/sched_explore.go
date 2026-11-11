//go:build ergoexplore

package rt

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// The explored build's scheduler (runtime.md, Explored schedules). Explore
// runs a body as the root of an auto clock's domain whose tasks share one
// baton: only the task that holds it runs. A task gives it back at each
// scheduling point (sched.go): before an operation on a shared handle, at
// a wait that parks it (Block), at a handshake (Await) and at its end; the
// strategy then picks the next task among the enabled ones.
//
// A task is enabled when it has not ended, is not parked in the clock's
// domain (idle.go) and has no handshake that is still not ready. The
// domain already knows each parked wait and clears the flag in the step
// that makes the wait's event visible, so enabledness needs no state of its
// own: a waker enables the task it wakes, under the clock's lock. The
// clock moves on its own only when every live member is parked, so it
// never moves while a task is enabled. When no task is enabled and some
// have not ended, the run is a deadlock.
//
// A task is named by its spawn path: the root is "0", and the k-th child
// that task p starts is p + "." + k, from 0. The path depends only on the
// schedule, never on goroutine timing, so a schedule (the list of picked
// paths) replays.
//
// Every field of an explorer is guarded by its clock's mu, the lock that
// guards the parked flags it reads.

// Exploring is true: this build has the controlled scheduler.
const Exploring = true

// Strategy picks the next task at each scheduling point.
type Strategy interface {
	// Pick returns the index in c.Enabled of the task that runs next. An
	// error stops the run there (a replay that left its schedule); it is
	// the run's Stopped.
	Pick(c Choice) (int, error)
}

// Pending is an enabled task and the operation it does when it is picked.
// Handle numbers the handle the operation touches, in the order the run
// first meets each handle; 0 is none.
type Pending struct {
	Task   string
	Kind   OpKind
	Handle int
}

// Choice is one pick: the enabled tasks in the order they were started,
// and the index among them of the task that reached the point, or -1 when
// that task blocked or ended (a pick of another task is then no
// preemption).
type Choice struct {
	Enabled []Pending
	Current int
	Step    int // the number of picks before this one
}

// SchedEvent is one entry of a run's trace: a pick, or a move of the clock.
type SchedEvent struct {
	Pending
	Tick bool      // a clock moved; Pending.Handle names it
	Now  time.Time // a tick's time on the clock that moved (Handle)
}

// Result is what an explored run did.
type Result struct {
	Events []SchedEvent
	Picks  int
	// Exit is the root body's exit.
	Exit error
	// Deadlock holds the tasks that had not ended when none could run,
	// with the operation each waits to do; nil when the run ended.
	Deadlock []Pending
	// Stopped is the strategy's error that stopped the run.
	Stopped error
	// Escape is the first event the scheduler did not order: a task that
	// ran without the baton, or a wait that ended with no wake the clock
	// saw. A run with an escape does not replay.
	Escape string
	// Handles names each handle number the run met by what the program
	// calls it: a task by its spawn path ("task 0.1"), a queue, a Ref or
	// an actor by the Ergo call that made it and its source position
	// (NameHandle). A number with no name is a handle the runtime made
	// for itself (a scope, a wake's source).
	Handles map[int]string
}

// Schedule is the list of picked tasks, the replay's input.
func (r Result) Schedule() []string {
	var out []string
	for _, e := range r.Events {
		if !e.Tick {
			out = append(out, e.Task)
		}
	}
	return out
}

// Cooperative runs the task at the point while it can, and otherwise the
// first enabled task: the schedule with no preemption.
type Cooperative struct{}

// Pick is Strategy's.
func (Cooperative) Pick(c Choice) (int, error) {
	if c.Current >= 0 {
		return c.Current, nil
	}
	return 0, nil
}

// ErrExploreBusy is Explore's panic when another explored run is active in
// the process: the hooks of a task with no explorer of its own read the
// one that is.
var ErrExploreBusy = errors.New("rt: another explored run is active")

type explorer struct {
	v        *Virtual
	strategy Strategy
	tasks    map[*Task]*xtask
	order    []*xtask // in the order they were started
	running  *xtask
	handles  map[any]int
	names    map[any]string // NameHandle's names
	sites    map[string]int // how many handles each name was given to
	res      Result
	free     bool // the run is over or stopped: every hook passes
	over     chan struct{}
}

type xtask struct {
	t       *Task
	path    string
	kids    int
	baton   chan struct{}
	next    Op
	done    bool
	blocked bool        // between Block and Unblock
	await   func() bool // a handshake that is not ready yet
}

var (
	active    atomic.Pointer[explorer]
	exploreMu sync.Mutex
)

// Explore runs body on the root task "0" of a new auto clock that reads
// start, under s, and returns when every task has ended, when no task can
// run (a deadlock), or when s stops the run. A run that did not end leaves
// its tasks where they wait, so its values stay as its last step left them;
// their goroutines stay until the process ends. One explored run is active at
// a time in a process; a second panics with ErrExploreBusy.
func Explore(start time.Time, s Strategy, body func(*Task) error) Result {
	return ExploreTraced(start, s, nil, body)
}

// ExploreTraced is Explore with a root whose tree reports its events to tr
// (TracedRoot): the explorer's laws read a run's machine events (X7).
func ExploreTraced(start time.Time, s Strategy, tr Tracer, body func(*Task) error) Result {
	if !exploreMu.TryLock() {
		panic(ErrExploreBusy)
	}
	defer exploreMu.Unlock()
	root := TracedRoot("explore", tr)
	v := NewAuto(root, start)
	e := &explorer{v: v, strategy: s, tasks: map[*Task]*xtask{}, handles: map[any]int{}, over: make(chan struct{})}
	e.running = e.add(root, "0")
	active.Store(e)
	go func() {
		_, err := Run(root, func(t *Task) (struct{}, error) { return struct{}{}, body(t) })
		v.mu.Lock()
		e.res.Exit = err
		v.mu.Unlock()
		Point(root, Op{OpEnd, root})
		schedEnd(root)
	}()
	<-e.over
	active.CompareAndSwap(e, nil)
	v.mu.Lock()
	res := e.res
	res.Handles = e.handleNames()
	v.mu.Unlock()
	return res
}

// handleNames is Result.Handles. v.mu is held.
func (e *explorer) handleNames() map[int]string {
	out := map[int]string{}
	for on, n := range e.handles {
		if name, ok := e.names[on]; ok {
			out[n] = name
		} else if t, ok := on.(*Task); ok && e.tasks[t] != nil {
			out[n] = "task " + e.tasks[t].path
		}
	}
	return out
}

// NameHandle gives on, a handle the program just made, its name in the
// report of the active explored run: name is the Ergo call that made it
// at its source position. The k-th handle (k > 1) of a run given one name,
// as in a loop, is named "name (k)". Outside an explored run it does
// nothing; a handle keeps its first name.
func NameHandle(on any, name string) {
	e := active.Load()
	if e == nil || name == "" {
		return
	}
	e.v.mu.Lock()
	defer e.v.mu.Unlock()
	if e.free {
		return
	}
	if _, ok := e.names[on]; ok {
		return
	}
	if e.names == nil {
		e.names, e.sites = map[any]string{}, map[string]int{}
	}
	e.sites[name]++
	if k := e.sites[name]; k > 1 {
		name += " (" + strconv.Itoa(k) + ")"
	}
	e.names[on] = name
}

// add registers t under path. v.mu is held, or t is the root.
func (e *explorer) add(t *Task, path string) *xtask {
	x := &xtask{t: t, path: path, baton: make(chan struct{}, 1), next: Op{OpStart, t}}
	e.tasks[t] = x
	e.order = append(e.order, x)
	return x
}

// handle numbers on. v.mu is held.
func (e *explorer) handle(on any) int {
	if on == nil {
		return 0
	}
	n, ok := e.handles[on]
	if !ok {
		n = len(e.handles) + 1
		e.handles[on] = n
	}
	return n
}

// enabled reports whether x can run. v.mu is held.
func (e *explorer) enabled(x *xtask) bool {
	if x.done {
		return false
	}
	// A task may wait in a clock domain of its own (a test's virtual or
	// manual clock): its parked flag is written only by the task that
	// holds the baton, so it is read here without that clock's lock.
	if m := x.t.member.Load(); m != nil && m.parked {
		return false
	}
	return x.await == nil || x.await()
}

func (e *explorer) pending(x *xtask) Pending {
	return Pending{Task: x.path, Kind: x.next.Kind, Handle: e.handle(x.next.On)}
}

// handoff picks the next task after from reached a point, blocked or
// ended, and gives it the baton. It returns the picked task, or nil when
// the run is over. v.mu is held.
func (e *explorer) handoff(from *xtask) *xtask {
	var cands []*xtask
	c := Choice{Current: -1, Step: e.res.Picks}
	for _, x := range e.order {
		if e.enabled(x) {
			if x == from {
				c.Current = len(cands)
			}
			cands = append(cands, x)
			c.Enabled = append(c.Enabled, e.pending(x))
		}
	}
	if len(cands) == 0 {
		var left []Pending
		for _, x := range e.order {
			if !x.done {
				left = append(left, e.pending(x))
			}
		}
		e.res.Deadlock = left
		e.release()
		return nil
	}
	i, err := e.strategy.Pick(c)
	if err == nil && (i < 0 || i >= len(cands)) {
		err = fmt.Errorf("the strategy picked %d of %d enabled tasks", i, len(cands))
	}
	if err != nil {
		e.res.Stopped = err
		e.release()
		return nil
	}
	next := cands[i]
	e.res.Events = append(e.res.Events, SchedEvent{Pending: c.Enabled[i]})
	e.res.Picks++
	next.await = nil
	e.running = next
	if next != from {
		next.baton <- struct{}{}
	}
	return next
}

// release ends the run: every hook passes from now on. A task that has
// not ended stays where it waits, so the run's values stay as they were
// at its last step. v.mu is held.
func (e *explorer) release() {
	if e.free {
		return
	}
	e.free = true
	e.running = nil
	close(e.over)
}

// escape records the first event outside the scheduler. v.mu is held.
func (e *explorer) escape(what string) {
	if e.res.Escape == "" {
		e.res.Escape = what
	}
}

// lookup is the explorer and t's task record when t runs in the active
// explored run; it returns with the clock's lock held, and with nil and no
// lock otherwise.
func lookup(t *Task) (*explorer, *xtask) {
	e := active.Load()
	if e == nil {
		return nil, nil
	}
	e.v.mu.Lock()
	x := e.tasks[t]
	if x == nil || e.free {
		e.v.mu.Unlock()
		return nil, nil
	}
	return e, x
}

// Point is the scheduling point of t before op (sched.go): t gives the
// baton back and does op when it is picked.
func Point(t *Task, op Op) {
	e, x := lookup(t)
	if e == nil {
		return
	}
	e.point(x, op)
}

// PointRunning is Point for the task that holds the baton.
func PointRunning(op Op) {
	e := active.Load()
	if e == nil {
		return
	}
	e.v.mu.Lock()
	x := e.running
	if x == nil || e.free {
		e.v.mu.Unlock()
		return
	}
	e.point(x, op)
}

// point is Point with v.mu held; it unlocks.
func (e *explorer) point(x *xtask, op Op) {
	if x != e.running {
		e.escape("task " + x.path + " did " + op.Kind.String() + " without the baton")
		e.v.mu.Unlock()
		return
	}
	x.next = op
	next := e.handoff(x)
	e.v.mu.Unlock()
	if next != x {
		<-x.baton
	}
}

// Block gives the baton away when t is parked: the wait that follows
// blocks until a waker enables t, and Unblock waits until t is picked. A
// wait that ends at once (the event came, or a stop) did not park, and t
// keeps the baton.
func Block(t *Task) {
	e, x := lookup(t)
	if e == nil {
		return
	}
	defer e.v.mu.Unlock()
	m := t.member.Load()
	if m == nil || !m.parked {
		return
	}
	if x != e.running {
		e.escape("task " + x.path + " waited without the baton")
		return
	}
	x.blocked = true
	x.next = Op{OpResume, m.on[0]}
	e.handoff(x)
}

// Unblock takes the baton back after the wait that Block began.
func Unblock(t *Task) {
	e, x := lookup(t)
	if e == nil {
		return
	}
	if !x.blocked {
		e.v.mu.Unlock()
		return
	}
	x.blocked = false
	m := t.member.Load()
	if m != nil && m.parked {
		// The wait ended with no wake the clock saw.
		e.escape("task " + x.path + " woke with no wake the scheduler saw")
	} else {
		m = nil
	}
	e.v.mu.Unlock()
	m.unpark()
	<-x.baton
}

// Await gives the baton away until ready reports true: a wait the clock
// does not see, for an event another task of the run makes (a runtime
// handshake). ready is called under the clock's lock and must take no
// lock. When the task is picked again ready holds, so the wait that
// follows ends at once.
func Await(t *Task, ready func() bool) {
	var e *explorer
	var x *xtask
	if t != nil {
		e, x = lookup(t)
	} else if a := active.Load(); a != nil {
		a.v.mu.Lock()
		if a.running == nil || a.free {
			a.v.mu.Unlock()
		} else {
			e, x = a, a.running
		}
	}
	if e == nil {
		return
	}
	if ready() {
		e.v.mu.Unlock()
		return
	}
	if x != e.running {
		e.escape("task " + x.path + " waited without the baton")
		e.v.mu.Unlock()
		return
	}
	x.await = ready
	x.next = Op{Kind: OpAwait}
	next := e.handoff(x)
	e.v.mu.Unlock()
	if next != x {
		<-x.baton
	}
}

// schedSpawn registers child, just started by parent, as enabled.
func schedSpawn(parent, child *Task) {
	e, px := lookup(parent)
	if e == nil {
		return
	}
	e.add(child, px.path+"."+strconv.Itoa(px.kids))
	px.kids++
	e.v.mu.Unlock()
}

// schedStart waits until the new task t is picked.
func schedStart(t *Task) {
	e, x := lookup(t)
	if e == nil {
		return
	}
	e.v.mu.Unlock()
	<-x.baton
}

// schedEnd gives the baton away for good: t has ended.
func schedEnd(t *Task) {
	e, x := lookup(t)
	if e == nil {
		return
	}
	defer e.v.mu.Unlock()
	if x != e.running {
		e.escape("task " + x.path + " ended without the baton")
	}
	x.done = true
	e.handoff(x)
}

// schedTick records a move of v, the clock of the task that runs. v.mu is
// held; the lock order is a clock's, then the run's.
func schedTick(v *Virtual) {
	e := active.Load()
	if e == nil {
		return
	}
	if e.v != v {
		e.v.mu.Lock()
		defer e.v.mu.Unlock()
	}
	if e.free || e.running == nil {
		return
	}
	if m := e.running.t.member.Load(); m == nil || m.v != v {
		return
	}
	e.res.Events = append(e.res.Events, SchedEvent{Tick: true, Now: v.now, Pending: Pending{Handle: e.handle(v)}})
}
