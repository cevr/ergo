package v1

import (
	"fmt"
	"io"
	"os"
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// Set is tasks.Set[E], an owner of any number of tasks (docs/spec/
// runtime.md, G4). Its rows are erased, so it is one Go type for every E.
//
// Purpose. A program that serves (a listener's connections, a supervisor's
// actors) starts tasks for as long as it runs, and keeps no Task per
// started task: it wants each to end on its own and be forgotten, and wants
// none of them to outlive the code that started them. A frame owns its
// Tasks one by one; a Set owns a group that grows and shrinks, and its
// policy says what a member's failure or defect does to the group.
//
// Rules, each one held by TestSetModel against a reference model:
//
//  1. Owner. A Set is a resource (#197 L8 B3t; resources R1): it has one
//     owner, as any owned value, and its owner's end releases it (Close,
//     which is Stop), last registered first with the owner's other
//     entries; a return or a move into a field or a container moves it.
//     Only the task that made it calls Go, Wait, Stop, Close and Len: Go,
//     Wait, Stop and Close check the caller, and another task is a defect.
//  2. Members. Go starts the body as a child of the owner task, which a
//     stop of the owner stops. A member that has ended is dropped: Len
//     counts only the members still running, and the Set keeps no handle
//     of an ended member past the owner's next call.
//  3. Policy. A member's typed failure or defect:
//     - FailFast: the first one ends the Set: admission closes, the other
//     members are stopped, and Wait gives it.
//     - LogFailures: it is written to the program's standard error in the
//     form main's exit report uses, and dropped; the Set goes on.
//     - Restart(limit): a defect runs the body again on the same task, up
//     to limit times for each Go; a typed failure, and the defect after
//     the last restart, end the Set as under FailFast.
//     An interruption (a member stopped by the Set or by its owner) is no
//     failure under any policy.
//  4. Wait waits until no member runs. It gives the typed failure that
//     ended the Set, again at each later Wait, and raises the defects it
//     has not raised yet, with that failure beside them. It is an
//     interruption point of the owner: a stop raises the interruption, and
//     the frame's end drains the Set.
//  5. Stop closes admission, stops every member and waits until each has
//     ended, its exit actions included. It raises the defects not raised
//     yet and drops typed failures (laws R3, R6). The frame's drain is a
//     Stop that returns those defects to the frame instead.
//  6. Closed. Go on a Set that admits no more tasks (after FailFast ended
//     it, a Stop or the drain) fails with tasks.Closed and starts nothing,
//     as queue.Push fails queue.Closed.
//
// Prior art: Effect's FiberSet (run adds a fiber, a finished fiber leaves
// the set, the scope's close interrupts the rest, join fails with the first
// failure); Go's errgroup.Group (Go, Wait gives the first error, WithContext
// cancels the rest); Erlang/OTP's one_for_one supervisor restarts a child
// from its specification within an intensity limit; Trio's nursery and
// Kotlin's coroutineScope keep every child inside a lexical scope.
type Set struct {
	owner *rt.Task // the task that made it
	kind  int      // setFailFast, setLogFailures or setRestart
	limit int      // restarts per Go under setRestart

	mu      sync.Mutex
	members []*member // running, each at its index i
	ended   []*member // ended, not yet joined by the owner
	wake    chan struct{}
	closed  bool
	// failure is the typed failure that ended the Set (FailFast), which
	// each Wait gives again; defects are the defects no Wait or Stop has
	// raised yet.
	failure error
	defects rt.Cause
	// released reports that Close ran: the Set's owner released it.
	released bool
}

// member is one started body of a Set.
type member struct {
	h    *rt.Handle[struct{}]
	i    int
	runs int // how many times the body ran: 1 + its restarts
}

const (
	setFailFast = iota
	setLogFailures
	setRestart
)

// failureLog is where LogFailures writes; a test replaces it.
var (
	failureLogMu sync.Mutex
	failureLog   io.Writer = os.Stderr
)

// closeGap, when a test sets it, runs in stop between closing admission
// and stopping the members: the window where a member's defect meets a
// closed Set that has not stopped it yet (TestSetNoRestartAfterClose).
// Production leaves it nil; the cost is one nil check per Stop.
var closeGap func(*Set)

// TasksClosed is tasks.Closed, the failure of Go on a Set that admits no
// more tasks.
type TasksClosed struct{}

// Error is the failure's text.
func (TasksClosed) Error() string { return "tasks.Closed" }

// TasksNewSet is the body of tasks.NewSet: s is the hidden frame of the
// code that makes it; the lowering registers the Set as that code's owned
// value in the step that receives it (Owning, R8). kind is the policy (FailFast 0, LogFailures 1,
// Restart 2) and limit Restart's limit; a negative limit is a defect.
func TasksNewSet(s *Scope, kind, limit int) *Set {
	s = s.current()
	if s.ended {
		panic("tasks.NewSet: the frame that would own this set has returned (law R9)")
	}
	if limit < 0 {
		panic(fmt.Sprintf("tasks.Restart: a negative limit %d", limit))
	}
	return &Set{owner: s.s.Task(), kind: kind, limit: limit}
}

// own panics unless t is the owner task (rule 1).
func (set *Set) own(t *rt.Task, op string) {
	if t != set.owner {
		panic("tasks.Set: " + op + " from a task that does not own the set")
	}
}

// TasksGo is the body of tasks.Go: it starts body as a member of set
// (rules 2 and 6). span is the member's site in a defect chain (G2).
func TasksGo(span Site, t *rt.Task, set *Set, body func(*rt.Task) error) error {
	return tasksGo(span, t, set, nil, body)
}

// TasksGoOwned is TasksGo with resources moved into the member's root.
// prepare runs once, even under Restart, and returns the scope that owns
// the captures. Each run of body only lends them (resources R12, R30).
// On failed admission prepare never runs; the sender releases the values.
func TasksGoOwned(span Site, t *rt.Task, set *Set, prepare func(*rt.Task) *Scope, body func(*rt.Task) error) error {
	return tasksGo(span, t, set, prepare, body)
}

func tasksGo(span Site, t *rt.Task, set *Set, prepare func(*rt.Task) *Scope, body func(*rt.Task) error) error {
	set.own(t, "Go")
	set.reap()
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return TasksClosed{}
	}
	m := &member{i: len(set.members)}
	set.members = append(set.members, m)
	holder := rt.NewScope(t)
	// The member's task: the body, again after each defect that the policy
	// restarts, then the member's end. guarded keeps each run's exit, so
	// the task itself ends with none.
	m.h = rt.Go(&holder, string(span), func(ct *rt.Task) (struct{}, error) {
		var exit error
		defer func() { set.end(m, exit) }()
		_, exit = guarded(ct, func(ct *rt.Task) (_ struct{}, err error) {
			if prepare != nil {
				root := prepare(ct)
				defer root.End(&err)
			}
			for {
				m.runs++
				_, err = guarded(ct, func(t *rt.Task) (struct{}, error) { return struct{}{}, body(t) })
				if !set.restarts(ct, m, err) {
					return struct{}{}, err
				}
			}
		})
		return struct{}{}, nil
	})
	return nil
}

// restarts reports whether the exit of a member's run starts it again:
// a defect under Restart with restarts left, in an open Set, while the
// member is not stopped.
func (set *Set) restarts(ct *rt.Task, m *member, exit error) bool {
	if set.kind != setRestart || !restartable(ct, exit, m.runs, set.limit) {
		return false
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	return !set.closed
}

// restartable is tasks.Restart's rule, for a Set's member and for a
// supervised actor (actor.go rule 16) alike: a run on ct that ended with
// exit runs again when exit holds a defect, ct's stop was not requested,
// and runs (this one included) is at most limit, so a body runs at most
// 1 + limit times. The caller adds its own "still open" check.
func restartable(ct *rt.Task, exit error, runs, limit int) bool {
	return exit != nil && !ct.StopRequested() && hasDefect(exit) && runs <= limit
}

// end records a member's last exit by the policy (rule 3), moves it to
// ended and wakes a waiting owner.
func (set *Set) end(m *member, exit error) {
	var stop []*member
	logged := false
	set.mu.Lock()
	last := len(set.members) - 1
	set.members[m.i] = set.members[last]
	set.members[m.i].i = m.i
	set.members = set.members[:last]
	set.ended = append(set.ended, m)
	switch {
	case exit == nil || rt.OnlyInterruption(exit):
	case set.kind == setLogFailures && !set.closed:
		logged = true
	case set.closed:
		// After the Set ended, a member's typed failure is the expected
		// result of its stop; its defects are kept (laws R3, R6).
		set.defects.Add(defectsIn(exit))
	default:
		set.closed = true
		if f, ok := rt.Failure(exit); ok {
			set.failure = f
		} else {
			set.defects.Add(exit)
		}
		stop = append(stop, set.members...)
	}
	if set.wake != nil {
		ch := set.wake
		set.wake = nil
		rt.Wake(set.owner, set, func() { close(ch) })
	}
	set.mu.Unlock()
	for _, o := range stop {
		o.h.Task().Interrupt()
	}
	if logged {
		failureLogMu.Lock()
		report(failureLog, exit)
		failureLogMu.Unlock()
	}
}

// reap joins the members that have ended, on the owner's goroutine, and
// drops them (rule 2).
func (set *Set) reap() {
	set.mu.Lock()
	ended := set.ended
	set.ended = nil
	set.mu.Unlock()
	for _, m := range ended {
		set.join(m)
	}
}

// join waits until m's task has finished and keeps a defect of the
// member's own task boundary, which run never sees.
func (set *Set) join(m *member) {
	if err := m.h.Stop(); err != nil {
		set.mu.Lock()
		set.defects.Add(err)
		set.mu.Unlock()
	}
}

// Len is how many members run now (rule 2).
func (set *Set) Len() int {
	set.reap()
	set.mu.Lock()
	defer set.mu.Unlock()
	return len(set.members)
}

// Wait waits until no member runs (rule 4).
func (set *Set) Wait(t *rt.Task) error {
	set.own(t, "Wait")
	if set.released {
		panic("tasks.Set: Wait on a set its owner released")
	}
	for {
		set.reap()
		set.mu.Lock()
		if len(set.ended) > 0 {
			// A member ended after reap took the list: join it first.
			set.mu.Unlock()
			continue
		}
		if len(set.members) == 0 {
			break
		}
		if t.Stopped() {
			set.mu.Unlock()
			t.Check()
		}
		ch := make(chan struct{})
		set.wake = ch
		stop := t.Done()
		p := rt.Park(t, set, stop, nil)
		set.mu.Unlock()
		rt.Block(t)
		select {
		case <-ch:
		case <-stop:
		}
		rt.Unblock(t)
		p.Unpark()
		set.mu.Lock()
		set.wake = nil
		set.mu.Unlock()
		t.Check()
	}
	defer set.mu.Unlock()
	err := set.defects.Err()
	set.defects = rt.Cause{}
	if err == nil {
		return set.failure
	}
	raise(join(set.failure, err))
	return nil
}

// Stop ends the Set (rule 5).
func (set *Set) Stop(t *rt.Task) {
	set.own(t, "Stop")
	if err := set.stop(); err != nil {
		raise(err)
	}
}

// Close is tasks.Set's Close(), the release of a Set its owner holds
// (#197 L8 B3t): masked on t, the owner task, it is Stop: admission closes,
// every member is stopped and joined, typed failures are dropped and the
// defects not raised yet are raised for the releasing scope.
func (set *Set) Close(t *rt.Task) {
	set.own(t, "Close")
	if set.released {
		return
	}
	set.released = true
	Release(t, func() {
		if err := set.stop(); err != nil {
			raise(err)
		}
	})
}

// stop closes admission, stops and joins every member, and returns the
// defects not raised yet.
func (set *Set) stop() error {
	set.mu.Lock()
	set.closed = true
	running := append([]*member(nil), set.members...)
	set.mu.Unlock()
	if closeGap != nil {
		closeGap(set)
	}
	for _, m := range running {
		m.h.Task().Interrupt()
	}
	for _, m := range running {
		set.join(m)
	}
	set.reap()
	set.mu.Lock()
	defer set.mu.Unlock()
	err := set.defects.Err()
	set.defects = rt.Cause{}
	return err
}

// hasDefect reports whether err holds a defect.
func hasDefect(err error) bool {
	c, ok := err.(*rt.Cause)
	if !ok || c == nil {
		return false
	}
	for _, r := range c.Reasons {
		if r.Kind == rt.KindDefect {
			return true
		}
	}
	return false
}

// defectsIn keeps the defects of err.
func defectsIn(err error) error {
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
