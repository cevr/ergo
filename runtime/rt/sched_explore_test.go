//go:build ergoexplore

package rt

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSchedulerFollowsModel is X5's model-based test of the explored
// build's scheduler (runtime.md, Explored schedules). A generated program
// on the rt API (a root that starts up to three children, then each task
// changes counters, sets and waits on latches, sleeps on the run's clock,
// and the root waits for or stops each child) runs under Explore with a
// seeded random strategy. A reference model in this file holds each
// task's state (new, enabled, parked on a latch, a sleep or a join, ended)
// and steps it by the same picks. At every pick the enabled tasks and
// their operations must equal the model's; the task that runs each
// operation must be the one picked, holding the baton; at the end the
// counters, latches, outcomes, the clock and the deadlock must equal the
// model's. The recorded schedule then replays to an equal trace and equal
// final values. A failing seed prints the program, both results and the
// replay command. It runs only in the explored build:
//
//	go test -tags ergoexplore -run TestSchedulerFollowsModel ./runtime/rt
func TestSchedulerFollowsModel(t *testing.T) {
	seeds := make([]uint64, 0, 300)
	if s := os.Getenv("ERGO_SCHED_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SCHED_SEED: %v", err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(300) {
			seeds = append(seeds, i+1)
		}
	}
	picks, deadlocks, ticks, cut := 0, 0, 0, 0
	for _, seed := range seeds {
		p := genSchedProgram(seed)
		first, why := runSchedCase(p, seed, nil)
		if why != "" {
			t.Fatalf("seed %d: %s\nprogram:\n%s\nreplay: ERGO_SCHED_SEED=%d go test -tags ergoexplore -run TestSchedulerFollowsModel ./runtime/rt", seed, why, p, seed)
		}
		again, why := runSchedCase(p, seed, first.res.Schedule())
		if why != "" {
			t.Fatalf("seed %d, replay: %s\nprogram:\n%s\nreplay: ERGO_SCHED_SEED=%d go test -tags ergoexplore -run TestSchedulerFollowsModel ./runtime/rt", seed, why, p, seed)
		}
		if a, b := first.String(), again.String(); a != b {
			t.Fatalf("seed %d: the replay differs\nprogram:\n%s\nfirst:\n%s\nreplay:\n%s\nreplay: ERGO_SCHED_SEED=%d go test -tags ergoexplore -run TestSchedulerFollowsModel ./runtime/rt", seed, p, a, b, seed)
		}
		picks += first.res.Picks
		if first.res.Deadlock != nil {
			deadlocks++
		}
		for _, e := range first.res.Events {
			if e.Tick {
				ticks++
			}
		}
		for _, c := range first.final.cut {
			if c {
				cut++
			}
		}
	}
	// What the cases reached, for a reader who asks whether the branches
	// ran; no count is asserted.
	t.Logf("%d programs: %d picks, %d deadlocks, %d clock moves, %d tasks ended by a stop", len(seeds), picks, deadlocks, ticks, cut)
}

// The instructions of a generated program.
type insKind int

const (
	insSpawn insKind = iota // root: start child k
	insInc                  // counter k += 1
	insSet                  // set latch k
	insWait                 // wait on latch k (interruptible)
	insSleep                // sleep k units on the run's clock (interruptible)
	insJoin                 // root: Wait for child k
	insStop                 // root: Stop child k
)

type ins struct {
	kind insKind
	k    int
}

func (i ins) String() string {
	return [...]string{"spawn", "inc", "set", "wait", "sleep", "join", "stop"}[i.kind] + " " + strconv.Itoa(i.k)
}

// schedProgram is the root's instructions and each child's.
type schedProgram struct {
	root []ins
	kids [][]ins
}

func (p schedProgram) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  0: %v\n", p.root)
	for i, k := range p.kids {
		fmt.Fprintf(&b, "  0.%d: %v\n", i, k)
	}
	return b.String()
}

const (
	schedCounters = 2
	schedLatches  = 2
)

func genSchedProgram(seed uint64) schedProgram {
	r := rand.New(rand.NewPCG(seed, 0x5c4ed))
	var p schedProgram
	any := func() ins {
		switch r.IntN(4) {
		case 0:
			return ins{insInc, r.IntN(schedCounters)}
		case 1:
			return ins{insSet, r.IntN(schedLatches)}
		case 2:
			return ins{insWait, r.IntN(schedLatches)}
		default:
			return ins{insSleep, 1 + r.IntN(3)}
		}
	}
	n := 1 + r.IntN(3)
	for i := range n {
		p.root = append(p.root, ins{insSpawn, i})
		var kid []ins
		for range r.IntN(5) {
			kid = append(kid, any())
		}
		p.kids = append(p.kids, kid)
	}
	var rest []ins
	for range r.IntN(4) {
		rest = append(rest, any())
	}
	for i := range n {
		c := ins{insJoin, i}
		if r.IntN(3) == 0 {
			c.kind = insStop
		}
		at := r.IntN(len(rest) + 1)
		rest = slices.Insert(rest, at, c)
	}
	p.root = append(p.root, rest...)
	return p
}

// latch is the test handle of the model: set once, and waited on parked.
type latch struct {
	mu      sync.Mutex
	set     bool
	ch      chan struct{}
	waiters []*Task
}

func (l *latch) fire(t *Task) {
	l.mu.Lock()
	if !l.set {
		l.set = true
		for _, w := range l.waiters {
			Waking(w, l).Done()
		}
		close(l.ch)
	}
	l.mu.Unlock()
}

func (l *latch) wait(t *Task) error {
	if t.Stopped() {
		return Interrupted
	}
	l.mu.Lock()
	if l.set {
		l.mu.Unlock()
		return nil
	}
	stop := t.Done()
	p := Park(t, l, stop, nil)
	l.waiters = append(l.waiters, t)
	l.mu.Unlock()
	Block(t)
	select {
	case <-l.ch:
	case <-stop:
	}
	Unblock(t)
	p.Unpark()
	l.mu.Lock()
	l.waiters = slices.DeleteFunc(l.waiters, func(w *Task) bool { return w == t })
	set := l.set
	l.mu.Unlock()
	if set {
		return nil
	}
	return Interrupted
}

// schedFinal is what a run ends with, for the model and for rt alike.
type schedFinal struct {
	counters [schedCounters]int
	latches  [schedLatches]bool
	// done counts each task's finished instructions; cut marks a task that
	// an interruption ended.
	done     []int
	cut      []bool
	clock    time.Duration
	deadlock []string
}

func (f schedFinal) String() string {
	return fmt.Sprintf("counters %v latches %v done %v cut %v clock %v deadlock %v", f.counters, f.latches, f.done, f.cut, f.clock, f.deadlock)
}

// schedRun is one explored run: its result and its final values.
type schedRun struct {
	res   Result
	final schedFinal
}

func (r schedRun) String() string {
	var b strings.Builder
	for _, e := range r.res.Events {
		if e.Tick {
			fmt.Fprintf(&b, "  tick %v\n", e.Now.UnixNano())
		} else {
			fmt.Fprintf(&b, "  %s %v #%d\n", e.Task, e.Kind, e.Handle)
		}
	}
	fmt.Fprintf(&b, "  final: %v\n", r.final)
	return b.String()
}

// runSchedCase runs p under Explore: with a seeded random strategy that
// follows the model when replay is nil, else with the replay's picks. It
// returns the run and the first difference from the model, if any.
func runSchedCase(p schedProgram, seed uint64, replay []string) (schedRun, string) {
	m := newSchedModel(p)
	st := &modelStrategy{m: m, rng: rand.New(rand.NewPCG(seed, 0x91c)), replay: replay}
	var counters [schedCounters]int
	latches := [schedLatches]*latch{}
	for i := range latches {
		latches[i] = &latch{ch: make(chan struct{})}
	}
	n := len(p.kids) + 1
	done, cut := make([]int, n), make([]bool, n)
	var clock *Virtual
	var mu sync.Mutex
	// check runs at each instruction: the task that does it holds the
	// baton and is the one the model picked last.
	check := func(t *Task, who int) {
		mu.Lock()
		defer mu.Unlock()
		e := active.Load()
		e.v.mu.Lock()
		running := e.running
		e.v.mu.Unlock()
		if running == nil || running.t != t {
			st.fail("task " + m.tasks[who].path + " ran an instruction without the baton")
		}
		if m.last != who {
			st.fail(fmt.Sprintf("task %s ran an instruction; the model picked %s", m.tasks[who].path, m.tasks[m.last].path))
		}
	}
	var hs []*Handle[struct{}]
	var run func(t *Task, list []ins, who int) error
	run = func(t *Task, list []ins, who int) (err error) {
		for _, in := range list {
			switch in.kind {
			case insSpawn:
				s := scopeOf[t]
				k := in.k
				hs = append(hs, Go(s, "kid", func(ct *Task) (struct{}, error) {
					return struct{}{}, run(ct, p.kids[k], k+1)
				}))
				check(t, who)
			case insInc:
				Point(t, Op{OpWrite, &counters[in.k]})
				check(t, who)
				counters[in.k]++
			case insSet:
				Point(t, Op{OpWrite, latches[in.k]})
				check(t, who)
				latches[in.k].fire(t)
			case insWait:
				Point(t, Op{OpRead, latches[in.k]})
				check(t, who)
				err = latches[in.k].wait(t)
			case insSleep:
				err = Sleep(t, clock, time.Duration(in.k))
				check(t, who)
			case insJoin:
				_, _ = hs[in.k].Wait()
				check(t, who)
			case insStop:
				_ = hs[in.k].Stop()
				check(t, who)
			}
			if err != nil {
				cut[who] = true
				return err
			}
			done[who]++
		}
		return nil
	}
	start := time.Unix(0, 0)
	res := Explore(start, st, func(t *Task) (err error) {
		clock = t.member.Load().v
		s := NewScope(t)
		defer s.End(&err)
		scopeOf[t] = &s
		defer delete(scopeOf, t)
		return run(t, p.root, 0)
	})
	got := schedRun{res: res, final: schedFinal{counters: counters, done: done, cut: cut}}
	for i, l := range latches {
		l.mu.Lock()
		got.final.latches[i] = l.set
		l.mu.Unlock()
	}
	clock.mu.Lock()
	got.final.clock = clock.now.Sub(start)
	clock.mu.Unlock()
	for _, d := range res.Deadlock {
		got.final.deadlock = append(got.final.deadlock, d.Task)
	}
	if st.why != "" {
		return got, st.why
	}
	if res.Escape != "" {
		return got, "escape: " + res.Escape
	}
	if res.Stopped != nil {
		return got, "stopped: " + res.Stopped.Error()
	}
	want := m.final()
	if got.final.String() != want.String() {
		return got, "final values differ\n  rt:    " + got.final.String() + "\n  model: " + want.String() + "\nrun:\n" + got.String()
	}
	// A deadlock's tasks run free once it is reported; wait for them only
	// when the run ended.
	return got, ""
}

// scopeOf is the root's scope, for its spawn instructions.
var scopeOf = map[*Task]*Scope{}

// modelStrategy picks at random (or by the replay) and steps the model,
// after it checks that the enabled tasks are the model's.
type modelStrategy struct {
	m      *schedModel
	rng    *rand.Rand
	replay []string
	mu     sync.Mutex
	why    string
}

func (s *modelStrategy) fail(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.why == "" {
		s.why = why
	}
}

func (s *modelStrategy) Pick(c Choice) (int, error) {
	want := s.m.enabled()
	var got []string
	for _, e := range c.Enabled {
		got = append(got, e.Task+" "+e.Kind.String())
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		s.fail(fmt.Sprintf("pick %d: enabled [%s], model [%s]", c.Step, strings.Join(got, ", "), strings.Join(want, ", ")))
		return 0, fmt.Errorf("the model differs")
	}
	i := s.rng.IntN(len(c.Enabled))
	if s.replay != nil {
		if c.Step >= len(s.replay) {
			return 0, fmt.Errorf("the replay has %d picks", len(s.replay))
		}
		i = slices.IndexFunc(c.Enabled, func(p Pending) bool { return p.Task == s.replay[c.Step] })
		if i < 0 {
			return 0, fmt.Errorf("pick %d: task %s is not enabled", c.Step, s.replay[c.Step])
		}
	}
	s.m.step(s.m.byPath(c.Enabled[i].Task))
	return i, nil
}

// The reference model.

type mstate int

const (
	mNew mstate = iota
	mReady
	mLatch // parked on latch k
	mSleep // parked on a timer
	mJoin  // parked on child k's end
	mDone
)

type mtask struct {
	path    string
	list    []ins
	pc      int
	state   mstate
	k       int  // the latch or child of a park
	woken   bool // a parked task that a wake enabled
	fired   bool // a sleeper whose deadline fired
	stopped bool
	cut     bool // an interruption ended its instructions
}

type mtimer struct {
	when, seq int
	task      int
}

type schedModel struct {
	p        schedProgram
	tasks    []*mtask // root, then children in spawn order
	kidTask  []int    // child k's index in tasks
	counters [schedCounters]int
	latches  [schedLatches]bool
	now, seq int
	timers   []mtimer
	last     int
}

func newSchedModel(p schedProgram) *schedModel {
	m := &schedModel{p: p, kidTask: make([]int, len(p.kids))}
	m.tasks = []*mtask{{path: "0", list: p.root, state: mReady}}
	return m
}

func (m *schedModel) byPath(path string) int {
	for i, x := range m.tasks {
		if x.path == path {
			return i
		}
	}
	panic("no task " + path)
}

func (m *schedModel) parked(x *mtask) bool {
	return (x.state == mLatch || x.state == mSleep || x.state == mJoin) && !x.woken
}

// op is the operation x does when it is picked.
func (m *schedModel) op(x *mtask) OpKind {
	switch {
	case x.state == mNew:
		return OpStart
	case x.state == mLatch || x.state == mSleep || x.state == mJoin:
		return OpResume
	case x.cut || x.pc == len(x.list):
		return OpEnd
	}
	return [...]OpKind{insSpawn: OpSpawn, insInc: OpWrite, insSet: OpWrite, insWait: OpRead, insSleep: OpSleep, insJoin: OpJoin, insStop: OpStop}[x.list[x.pc].kind]
}

func (m *schedModel) enabled() []string {
	var out []string
	for _, x := range m.tasks {
		if x.state != mDone && !m.parked(x) {
			out = append(out, x.path+" "+m.op(x).String())
		}
	}
	return out
}

// interrupt wakes a child parked in an interruptible wait.
func (m *schedModel) interrupt(x *mtask) {
	x.stopped = true
	if (x.state == mLatch || x.state == mSleep) && !x.woken {
		x.woken = true
	}
}

// join parks x on child k's end unless it ended.
func (m *schedModel) join(x *mtask, k int) {
	if m.tasks[m.kidTask[k]].state == mDone {
		x.pc++
		return
	}
	x.state, x.k = mJoin, k
}

func (m *schedModel) step(i int) {
	m.last = i
	x := m.tasks[i]
	switch {
	case x.state == mNew:
		x.state = mReady
	case x.state == mLatch:
		x.state, x.woken = mReady, false
		if m.latches[x.k] {
			x.pc++
		} else {
			x.cut = true
		}
	case x.state == mSleep:
		x.state, x.woken = mReady, false
		if x.fired {
			x.pc++
		} else {
			x.cut = true
			m.timers = slices.DeleteFunc(m.timers, func(t mtimer) bool { return t.task == i })
		}
		x.fired = false
	case x.state == mJoin:
		x.state, x.woken = mReady, false
		x.pc++
	case x.cut || x.pc == len(x.list):
		x.state = mDone
		for _, w := range m.tasks {
			if w.state == mJoin && !w.woken && m.kidTask[w.k] == i {
				w.woken = true
			}
		}
	default:
		in := x.list[x.pc]
		switch in.kind {
		case insSpawn:
			m.kidTask[in.k] = len(m.tasks)
			m.tasks = append(m.tasks, &mtask{path: "0." + strconv.Itoa(in.k), list: m.p.kids[in.k], state: mNew})
			x.pc++
		case insInc:
			m.counters[in.k]++
			x.pc++
		case insSet:
			if !m.latches[in.k] {
				m.latches[in.k] = true
				for _, w := range m.tasks {
					if w.state == mLatch && w.k == in.k {
						w.woken = true
					}
				}
			}
			x.pc++
		case insWait:
			switch {
			case x.stopped:
				x.cut = true
			case m.latches[in.k]:
				x.pc++
			default:
				x.state, x.k = mLatch, in.k
			}
		case insSleep:
			if x.stopped {
				x.cut = true
				break
			}
			m.seq++
			m.timers = append(m.timers, mtimer{when: m.now + in.k, seq: m.seq, task: i})
			x.state = mSleep
			// The sleeper parks last: when its own deadline comes first,
			// the move wakes it in the same step and it goes on.
			m.idle()
			if x.woken {
				x.state, x.woken, x.fired = mReady, false, false
				x.pc++
			}
		case insJoin:
			m.join(x, in.k)
		case insStop:
			m.interrupt(m.tasks[m.kidTask[in.k]])
			m.join(x, in.k)
		}
	}
	m.idle()
}

// idle moves the clock when every task that has not ended is parked: to
// the earliest deadline, waking its sleepers.
func (m *schedModel) idle() {
	if len(m.timers) == 0 {
		return
	}
	for _, x := range m.tasks {
		if x.state != mDone && !m.parked(x) {
			return
		}
	}
	slices.SortFunc(m.timers, func(a, b mtimer) int {
		if a.when != b.when {
			return a.when - b.when
		}
		return a.seq - b.seq
	})
	when := m.timers[0].when
	if when > m.now {
		m.now = when
	}
	for len(m.timers) > 0 && m.timers[0].when == when {
		x := m.tasks[m.timers[0].task]
		x.woken, x.fired = true, true
		m.timers = m.timers[1:]
	}
}

func (m *schedModel) final() schedFinal {
	f := schedFinal{counters: m.counters, latches: m.latches, clock: time.Duration(m.now)}
	all := true
	for _, x := range m.tasks {
		if x.state != mDone {
			all = false
		}
	}
	// The final slices are indexed root first, then child k at k+1.
	n := len(m.p.kids) + 1
	f.done, f.cut = make([]int, n), make([]bool, n)
	for i, x := range m.tasks {
		at := 0
		if i > 0 {
			at = slices.Index(m.kidTask, i) + 1
		}
		f.done[at], f.cut[at] = x.pc, x.cut
	}
	if !all {
		for _, x := range m.tasks {
			if x.state != mDone {
				f.deadlock = append(f.deadlock, x.path)
			}
		}
	}
	return f
}
