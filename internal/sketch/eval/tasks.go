package eval

import (
	"fmt"
	"math"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The task, queue, clock and exit-action externs the evaluator runs
// (ORACLE O3), written from runtime.md (Tasks 1-12, Queues 1-10, Exit
// actions 1-5, G4) and stdlib.md (time.Manual, testing.VirtualClock), on
// the scheduler of sched.go. None calls runtime/v1.

// drainer is a std value whose generated release takes the values it
// still holds (check.Drop.Drain): a queue's values left, or a Task's
// value no Wait took.
type drainer interface{ drain() []Value }

func init() {
	natives["task.Go"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		c, ok := args[0].(*closure)
		if !ok {
			return nil, fr.unsupported(at, "a task whose body is not a literal or a block yet")
		}
		if in.sched == nil {
			return nil, fr.unsupported(at, "a task outside an obligation's body")
		}
		run, _ := in.taskBody(fr, at, c)
		t := in.start(in.siteOf(fr, at, "task.Go"), func(t *etask) (Value, *abrupt) { return run(nil) })
		in.handCaptures(fr, c)
		return &taskV{t: t}, nil
	}
	natives["scope.OnExit"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		c, ok := args[0].(*closure)
		if !ok {
			return nil, fr.unsupported(at, "an exit action that is not a block yet")
		}
		// The innermost function's own list, also inside an owning with
		// (Exit actions 2): it runs when the function returns.
		fr.ownerOf()
		e := &owned{act: c, caps: fr.caps, at: at, live: true}
		fr.frameOwn.list = append(fr.frameOwn.list, e)
		return unit{}, nil
	}
	natives["queue.New"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		n := args[0].(int64)
		if n < 0 {
			return nil, fr.defect(at, "queue.New: negative capacity %d", n)
		}
		if ab := in.build(fr, at, 1, 4, 0); ab != nil {
			return nil, ab
		}
		return &queueV{capacity: n, closed: stdRecord(fn, "Closed", nil).t}, nil
	}
	timeline := func(auto bool) native {
		return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if ab := in.build(fr, at, 1, 4, 0); ab != nil {
				return nil, ab
			}
			return &timelineV{auto: auto, start: time.Unix(args[0].(int64), args[1].(int64)).UTC()}, nil
		}
	}
	// The pure duration helpers of std time, from Go's time package and
	// the saturation rules their doc comments state.
	natives["time.subMono"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		a, b := args[0].(int64), args[1].(int64)
		d := a - b
		switch {
		case d < 0 && a > b:
			d = math.MaxInt64
		case d > 0 && a < b:
			d = math.MinInt64
		}
		return d, nil
	}
	natives["time.subWall"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		a := time.Unix(args[0].(int64), args[1].(int64))
		return int64(a.Sub(time.Unix(args[2].(int64), args[3].(int64)))), nil
	}
	quotient := func(what string) native {
		return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			d, n := args[0].(int64), args[1].(int64)
			switch {
			case n == 0:
				return nil, fr.defect(at, "time.%s: integer divide by zero", what)
			case n == -1 && d == math.MinInt64:
				return int64(math.MaxInt64), nil
			}
			return d / n, nil
		}
	}
	natives["time.Divide"] = quotient("Divide")
	natives["time.Count"] = quotient("Count")
	natives["time.newTimeline"] = timeline(false)
	natives["testing.newLine"] = timeline(true)
	natives["tasks.newSet"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		kind, limit := args[0].(int64), args[1].(int64)
		if kind == 1 {
			return nil, fr.unsupported(at, "a tasks.Set that logs its failures to standard error yet")
		}
		if in.sched == nil {
			return nil, fr.unsupported(at, "a tasks.Set outside an obligation's body")
		}
		return &setV{restart: kind == 2, limit: limit, owner: in.cur, closed: stdRecord(fn, "Closed", nil).t}, nil
	}
	natives["tasks.Go"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		set, ok := args[0].(*setV)
		if !ok {
			return nil, fr.unsupported(at, "a Set the evaluator did not build")
		}
		c, ok := args[1].(*closure)
		if !ok {
			return nil, fr.unsupported(at, "a Set member whose body is not a literal or a block yet")
		}
		if in.cur != set.owner {
			return nil, fr.defect(at, "tasks: a Set's operations run only on the task that made it")
		}
		if set.isClosed {
			// A rejected admission releases only the moved captures, in
			// the call's step (Tasks 6).
			if ab := in.releaseCaptures(fr, c); ab != nil {
				return nil, ab
			}
			return nil, fr.abrupt(ctlFail, at, &record{t: set.closed}, "")
		}
		run, newRoot := in.taskBody(fr, at, c)
		t := in.start(in.siteOf(fr, at, "tasks.Go"), func(t *etask) (Value, *abrupt) {
			root := newRoot()
			ab := set.runMember(in, t, func() *abrupt {
				_, ab := run(root)
				return ab
			})
			return unit{}, in.endFrame(root, ab)
		})
		set.members = append(set.members, t)
		in.handCaptures(fr, c)
		return unit{}, nil
	}
}

// siteOf names a task by the call that started it, for a deadlock's
// report.
func (in *interp) siteOf(fr *frame, at syntax.Node, name string) string {
	s := in.site(fr.pkg, fr.decl, at.Pos())
	return fmt.Sprintf("%s at %s:%d:%d", name, s.File, s.Line, s.Column)
}

// taskBody is the run of c on a task of its own: a copy of the values it
// reads, as they are when the task starts (Tasks 6: plain values copy
// into a task), and the moved resource captures its root frame owns and
// releases when the body ends (R12), before Wait returns. run's argument
// is the root of a Set member, whose captures outlive its restarts (G4
// 4); nil makes the root of one run.
func (in *interp) taskBody(fr *frame, at syntax.Node, c *closure) (run func(root *frame) (Value, *abrupt), newRoot func() *frame) {
	vars := newVars()
	c.vars.each(func(v *check.Var, s *slot) {
		s.lend()
		vars.set(v, &slot{v: s.v})
	})
	cc := *c
	cc.vars = vars
	var moved []*check.Var
	if tc := fr.info.TaskCaptures[c.key]; tc != nil {
		moved = tc.Vars
	}
	newRoot = func() *frame { return in.taskRoot(&cc, moved) }
	run = func(root *frame) (Value, *abrupt) {
		own := root == nil
		if own {
			root = newRoot()
		}
		// The body's frame reads its captures' entries in the root.
		body := cc
		body.outer = root
		v, ab := in.callClosure(root, nil, &body, nil)
		if own {
			ab = in.endFrame(root, ab)
		}
		return v, ab
	}
	return run, newRoot
}

// taskRoot is the root frame of a task's body: it owns the body's moved
// resource captures.
func (in *interp) taskRoot(c *closure, moved []*check.Var) *frame {
	home := c.home
	root := &frame{pkg: home.pkg, info: home.info, decl: home.decl, key: c.key, vars: c.vars, env: c.env, owner: home.owner, caps: c.caps}
	for _, v := range moved {
		if d := home.info.OwnedVars[v]; d != nil {
			if s, _ := c.vars.get(v); s != nil {
				in.register(root, v, s.v, d, c.key)
			}
		}
	}
	return root
}

// handCaptures ends the sender's entries of c's moved captures: the
// started task's root owns them from the step that admits it.
func (in *interp) handCaptures(fr *frame, c *closure) {
	if tc := fr.info.TaskCaptures[c.key]; tc != nil {
		for _, v := range tc.Vars {
			fr.hand(v)
		}
	}
}

// releaseCaptures releases c's moved captures now, last first: a Set
// that admits no member never starts their owner.
func (in *interp) releaseCaptures(fr *frame, c *closure) *abrupt {
	tc := fr.info.TaskCaptures[c.key]
	if tc == nil {
		return nil
	}
	for i := len(tc.Vars) - 1; i >= 0; i-- {
		if e := fr.lookupOwned(tc.Vars[i]); e != nil && e.live {
			if ab := in.release(fr, e); ab != nil {
				return ab
			}
		}
	}
	return nil
}

// runAction runs an exit action's block, masked, as a frame of its own
// (Exit actions 1-5): what it starts or registers ends with it. Its
// defect is a release defect of the function it ends.
func (in *interp) runAction(fr *frame, e *owned) *abrupt {
	_, ab := in.callClosure(fr, nil, e.act, nil)
	if ab == nil || ab.kind == ctlDefect || stops(ab) {
		return ab
	}
	ice("an exit action completed with %d", ab.kind)
	return nil
}

// taskV is a Task: its task, and whether a Wait, Stop or Close consumed
// it (Tasks 4, 11).
type taskV struct {
	t         *etask
	used      bool
	delivered bool // a Wait delivered the value: no release takes it
}

func (v *taskV) describe() string { return fmt.Sprintf("a Task (task #%d)", v.t.id) }

func (v *taskV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	t := v.t
	switch name {
	case "Wait":
		if v.used {
			return nil, fr.defect(at, "task: Wait on a task that was already waited for, stopped or released")
		}
		v.used = true
		if ab := in.block(fr, at, fmt.Sprintf("task #%d to end", t.id), func() bool { return t.state == done }); ab != nil {
			if ab.kind != ctlInterrupt {
				return nil, ab
			}
			// The waiter is stopped: the task is stopped and drained, and
			// the waiter's exit is the interruption with the task's
			// defects (law R4).
			stopTree(t)
			if d := in.waitDone(fr, at, t, fmt.Sprintf("task #%d to end", t.id)); d != nil {
				return nil, d
			}
			if t.exit != nil && t.exit.kind == ctlDefect {
				return nil, t.exit
			}
			return nil, ab
		}
		switch {
		case t.exit == nil:
			v.delivered = true
			return t.val, nil
		case t.exit.kind == ctlFail || t.exit.kind == ctlEscape:
			// A lone typed failure: a catch of the waiter takes it.
			return nil, fr.abrupt(ctlFail, at, t.exit.val, "")
		case t.exit.kind == ctlInterrupt:
			return nil, fr.interrupt(at)
		}
		return nil, t.exit
	case "Stop", "Close":
		// Stop, and the owner's release: stop the task and wait,
		// uninterruptibly, until it has ended; its interruption and a
		// typed failure are dropped, its defects raised (Tasks 10, 12).
		if v.used {
			return unit{}, nil
		}
		v.used = true
		stopTree(t)
		if ab := in.waitDone(fr, at, t, fmt.Sprintf("task #%d to stop", t.id)); ab != nil {
			return nil, ab
		}
		if t.exit != nil && t.exit.kind == ctlDefect {
			return nil, t.exit
		}
		return unit{}, nil
	}
	ice("the Task operation %s", name)
	return nil, nil
}

// drain is the value the task's body returned that no Wait delivered
// (Tasks 12; runtime/v1 Task.Drain): after its Close, for its owner's
// release.
func (v *taskV) drain() []Value {
	if v.delivered || !v.t.ended {
		return nil
	}
	return []Value{v.t.val}
}

// queueV is a queue.Queue (runtime.md, Queues): a FIFO of capacity
// values, the Pushes that wait for room in the order they began to wait,
// and whether End closed it.
type queueV struct {
	capacity int64
	buf      []Value
	pushers  []*pushWait
	ended    bool
	closed   *check.Named // queue.Closed
}

// pushWait is one Push waiting for room: in once its value entered,
// failed once End closed the queue first.
type pushWait struct {
	v          Value
	in, failed bool
}

func (q *queueV) describe() string { return "a queue.Queue" }

// admit moves the oldest waiting Pushes' values in while there is room.
func (q *queueV) admit() {
	for len(q.pushers) > 0 && int64(len(q.buf)) < q.capacity {
		w := q.pushers[0]
		q.pushers = q.pushers[1:]
		q.buf = append(q.buf, w.v)
		w.in = true
	}
}

// take takes the front value, or the oldest waiting Push's value on a
// queue with no room (capacity 0), when there is one.
func (q *queueV) take() (Value, bool) {
	if len(q.buf) > 0 {
		v := q.buf[0]
		q.buf = q.buf[1:]
		q.admit()
		return v, true
	}
	if len(q.pushers) > 0 {
		w := q.pushers[0]
		q.pushers = q.pushers[1:]
		w.in = true
		return w.v, true
	}
	return nil, false
}

// end ends admission: each waiting Push fails, its value never entering.
func (q *queueV) end() {
	q.ended = true
	for _, w := range q.pushers {
		w.failed = true
	}
	q.pushers = nil
}

func (q *queueV) drain() []Value {
	q.end()
	left := q.buf
	q.buf = nil
	return left
}

func (q *queueV) closedFailure(fr *frame, at syntax.Node) *abrupt {
	return fr.abrupt(ctlFail, at, &record{t: q.closed}, "")
}

func (q *queueV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	switch name {
	case "Push":
		if ab := in.queuePush(fr, at, q, args[0]); ab != nil {
			return nil, ab
		}
		return unit{}, nil
	case "Pop":
		v, ok, ab := in.queuePop(fr, at, q)
		switch {
		case ab != nil:
			return nil, ab
		case !ok:
			return nil, q.closedFailure(fr, at)
		}
		return v, nil
	case "End":
		q.end()
		return unit{}, nil
	case "Poll":
		v, ok := q.take()
		call, isCall := at.(*syntax.CallExpr)
		if !isCall {
			ice("a Poll with no call")
		}
		return in.option(fr, call, v, ok)
	case "Len":
		return int64(len(q.buf)), nil
	case "Values":
		return &queueValues{q: q}, nil
	}
	ice("the queue operation %s", name)
	return nil, nil
}

// queuePush is q.Push(v): it fails with Closed on a closed queue, enters
// at once when there is room and no earlier Push waits, else waits; a
// stop while it waits leaves the value out, unless it entered already
// (Queues 2, 5, 8).
func (in *interp) queuePush(fr *frame, at syntax.Node, q *queueV, v Value) *abrupt {
	if s := in.sched; s != nil && s.fatal != nil {
		return fr.abort(at)
	}
	if in.stoppedNow() {
		return fr.interrupt(at)
	}
	if q.ended {
		return q.closedFailure(fr, at)
	}
	if len(q.pushers) == 0 && int64(len(q.buf)) < q.capacity {
		q.buf = append(q.buf, v)
		return nil
	}
	w := &pushWait{v: v}
	q.pushers = append(q.pushers, w)
	ab := in.block(fr, at, "room in a queue", func() bool { return w.in || w.failed })
	if ab != nil {
		if w.in && ab.kind == ctlInterrupt {
			return nil // it entered: the next point sees the stop
		}
		for i, x := range q.pushers {
			if x == w {
				q.pushers = append(q.pushers[:i:i], q.pushers[i+1:]...)
				break
			}
		}
		return ab
	}
	if w.failed {
		return q.closedFailure(fr, at)
	}
	return nil
}

// queuePop is q.Pop(): the front value, waiting while the queue is open
// and empty; ok is false on a closed, empty queue (Queues 3, 5). A
// stopped Pop takes nothing.
func (in *interp) queuePop(fr *frame, at syntax.Node, q *queueV) (Value, bool, *abrupt) {
	ab := in.block(fr, at, "a value in a queue", func() bool { return len(q.buf) > 0 || len(q.pushers) > 0 || q.ended })
	if ab != nil {
		return nil, false, ab
	}
	v, ok := q.take()
	return v, ok, nil
}

// queueValues is q.Values(), a Seq a for loop runs as one Pop a step
// (stmt.go).
type queueValues struct{ q *queueV }

// seqSource is a Seq a native made, which a for loop runs step by step:
// next gives the next value, or more false at its end.
type seqSource interface {
	next(in *interp, fr *frame, at syntax.Node) (v Value, more bool, ab *abrupt)
}

func (s *queueValues) next(in *interp, fr *frame, at syntax.Node) (Value, bool, *abrupt) {
	return in.queuePop(fr, at, s.q)
}

// timelineV is a time.Timeline: time.Manual's (moved only by Advance) or
// testing.VirtualClock's (auto: it also moves to its next sleeper's
// deadline when no task can go on). now is the time since start.
type timelineV struct {
	auto      bool
	start     time.Time
	now       time.Duration
	sleepers  []*sleeper
	advancing bool
}

// sleeper is one Wait on a timeline: it wakes when now reaches at.
type sleeper struct {
	tl   *timelineV
	at   time.Duration
	woke bool
}

func (l *timelineV) describe() string { return "a time.Timeline" }

// wake wakes each sleeper whose deadline now has reached.
func (l *timelineV) wake() {
	keep := l.sleepers[:0]
	for _, s := range l.sleepers {
		if s.at <= l.now {
			s.woke = true
		} else {
			keep = append(keep, s)
		}
	}
	l.sleepers = keep
}

// deadline is now + d, saturating.
func (l *timelineV) deadline(d time.Duration) time.Duration {
	if d > math.MaxInt64-l.now {
		return math.MaxInt64
	}
	return l.now + d
}

func (l *timelineV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	switch name {
	case "Read":
		x, ok := at.(syntax.Expr)
		if !ok {
			ice("a Read with no call")
		}
		n, ok := fr.typeOf(x).(*check.Named)
		if !ok || structOf(n) == nil {
			ice("a Read of %s", fr.typeOf(x))
		}
		t := l.start.Add(l.now)
		r := &record{t: n, fields: make([]Value, 3)}
		r.fields[fieldIndex(n, "Sec")] = t.Unix()
		r.fields[fieldIndex(n, "Nsec")] = int64(t.Nanosecond())
		r.fields[fieldIndex(n, "Mono")] = int64(t.Sub(l.start))
		return r, nil
	case "Wait":
		d := time.Duration(args[0].(int64))
		if d <= 0 {
			return unit{}, nil
		}
		s := &sleeper{tl: l, at: l.deadline(d)}
		l.sleepers = append(l.sleepers, s)
		cur := in.cur
		if cur != nil {
			cur.sleep = s
		}
		ab := in.block(fr, at, "a sleep on a clock", func() bool { return s.woke })
		if cur != nil {
			cur.sleep = nil
		}
		if ab != nil {
			if !s.woke {
				for i, x := range l.sleepers {
					if x == s {
						l.sleepers = append(l.sleepers[:i:i], l.sleepers[i+1:]...)
						break
					}
				}
			}
			return nil, ab
		}
		return unit{}, nil
	case "Advance":
		d := time.Duration(args[0].(int64))
		if d < 0 {
			return nil, fr.defect(at, "time.Manual: Advance by a negative duration")
		}
		if l.advancing {
			return nil, fr.defect(at, "time.Manual: Advance while another Advance of the clock runs; one task advances a clock")
		}
		l.advancing = true
		defer func() { l.advancing = false }()
		target := l.deadline(d)
		// One deadline at a time, in deadline order, each woken task
		// running to its next wait before the next (rt.AdvanceSettled).
		for {
			next, found := time.Duration(0), false
			for _, s := range l.sleepers {
				if s.at <= target && (!found || s.at < next) {
					next, found = s.at, true
				}
			}
			if !found {
				break
			}
			l.now = next
			l.wake()
			if ab := in.settle(fr, at); ab != nil {
				return nil, ab
			}
		}
		l.now = target
		if ab := in.settle(fr, at); ab != nil {
			return nil, ab
		}
		return unit{}, nil
	case "Sleepers":
		return int64(len(l.sleepers)), nil
	}
	ice("the Timeline operation %s", name)
	return nil, nil
}

// idle moves the virtual clock whose next sleeper is nearest to that
// sleeper's deadline, when no task can go on (testing.VirtualClock); it
// reports whether one moved.
func (s *scheduler) idle() bool {
	var best *sleeper
	var gap time.Duration
	for _, t := range s.tasks {
		if t.state != blocked || t.sleep == nil || !t.sleep.tl.auto || t.sleep.woke {
			continue
		}
		g := t.sleep.at - t.sleep.tl.now
		if best == nil || g < gap {
			best, gap = t.sleep, g
		}
	}
	if best == nil {
		return false
	}
	best.tl.now = best.at
	best.tl.wake()
	return true
}

// setV is a tasks.Set (runtime.md, G4): its members, its policy, and
// what ended it.
type setV struct {
	restart bool
	limit   int64
	owner   *etask
	members []*etask
	// isClosed ends admission; failure is the typed failure that ended
	// the Set, defects those no Wait or Stop raised yet.
	isClosed bool
	failure  Value
	failed   bool
	defects  []*abrupt
	closed   *check.Named // tasks.Closed
}

func (s *setV) describe() string { return "a tasks.Set" }

// runMember runs a member's body under the Set's policy: a defect runs it
// again under Restart, up to the limit; a typed failure or a defect past
// it closes the Set and stops the other members (FailFast). A member's
// root owns its moved captures across its runs, each run lending them
// (G4 3, 4).
func (s *setV) runMember(in *interp, t *etask, run func() *abrupt) *abrupt {
	var ab *abrupt
	for runs := int64(0); ; runs++ {
		ab = run()
		if ab != nil && ab.kind == ctlDefect && s.restart && runs < s.limit && !t.stopped {
			continue
		}
		break
	}
	switch {
	case ab == nil, stops(ab), ab.kind == ctlInterrupt:
		return ab
	case ab.kind == ctlFail || ab.kind == ctlEscape:
		if !s.failed {
			s.failure, s.failed = ab.val, true
		}
	case ab.kind == ctlDefect:
		s.defects = append(s.defects, ab)
	}
	s.stopAll(t)
	return nil
}

// stopAll closes admission and stops every member but except.
func (s *setV) stopAll(except *etask) {
	s.isClosed = true
	for _, m := range s.members {
		if m != except && m.state != done {
			stopTree(m)
		}
	}
}

// running is how many members have not ended.
func (s *setV) running() int {
	n := 0
	for _, m := range s.members {
		if m.state != done {
			n++
		}
	}
	return n
}

// raise is the first defect no Wait or Stop raised yet; it marks every
// one raised.
func (s *setV) raise() *abrupt {
	if len(s.defects) == 0 {
		return nil
	}
	d := s.defects[0]
	s.defects = nil
	return d
}

func (s *setV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	if in.cur != s.owner {
		return nil, fr.defect(at, "tasks: a Set's operations run only on the task that made it")
	}
	switch name {
	case "Wait":
		if ab := in.block(fr, at, "a Set's members to end", func() bool { return s.running() == 0 }); ab != nil {
			return nil, ab
		}
		if d := s.raise(); d != nil {
			return nil, d
		}
		if s.failed {
			return nil, fr.abrupt(ctlFail, at, s.failure, "")
		}
		return unit{}, nil
	case "Stop", "Close":
		s.stopAll(nil)
		for _, m := range s.members {
			if ab := in.waitDone(fr, at, m, "a Set's members to stop"); ab != nil {
				return nil, ab
			}
		}
		if d := s.raise(); d != nil {
			return nil, d
		}
		return unit{}, nil
	case "Len":
		return int64(s.running()), nil
	}
	ice("the Set operation %s", name)
	return nil, nil
}
