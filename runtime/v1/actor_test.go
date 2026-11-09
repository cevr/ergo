package v1

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// The machine actor (actor.go; runtime.md, Machines and actors 11) against
// a reference: generated machines driven on a test Scheduler whose sleeps
// the test releases, so virtual time is exact, and a reference timed fold
// with one timer slot that steps the same trace (runner rule 10). Waits
// wait for events (a registered sleep, the runner's handled count), never
// for a wall-clock bound.

// tcell is one cell of a generated step table.
type tcell struct {
	kind   string // enter, stay, emit, ignore, reject, done, fail
	target int
	delta  int
}

type tstate struct{ v, n int }
type tsig struct{ v, x int }

// tdec is a decision of the generated step: kind as actor.go's decide
// constants, the next state and the output.
type tdec struct {
	kind int
	next tstate
	out  int
}

type ttimer struct {
	has          bool
	base, sig, x int
}

type tmachine struct {
	table  [][]tcell
	timers []ttimer
	init   tstate
	// arms are the step's arms in match order (MU15): the first that
	// takes a pair of state and signal variants is the arm that steps it.
	arms []tarm
}

// tarm is one arm of a generated step: the state and signal variant it
// takes, -1 for any.
type tarm struct{ state, sig int }

var errBoom = errors.New("boom")

// gate is the signal whose step waits until the test opens it, then
// ignores: it holds the runner in a step while the test queues behind it.
const gate = -1

var kinds = map[string]int{"enter": decideEnter, "stay": decideStay, "emit": decideEmit, "ignore": decideIgnore, "reject": decideReject, "done": decideDone}

// decide is the pure step of m: the decision, or errBoom.
func (m tmachine) decide(s tstate, e tsig) (tdec, error) {
	c := m.table[s.v][e.v]
	if c.kind == "fail" {
		return tdec{}, errBoom
	}
	d := tdec{kind: kinds[c.kind], out: s.n + e.x}
	if c.kind == "enter" || c.kind == "stay" || c.kind == "emit" {
		d.next = tstate{c.target, s.n + e.x + c.delta}
	}
	return d, nil
}

// in is the delay of s's timer, or ActorNoTimer(). base runs from -1, so
// the delays include negative ones and 0, which are defects (rule 7).
func (m tmachine) in(s tstate) time.Duration {
	tm := m.timers[s.v]
	if !tm.has {
		return ActorNoTimer()
	}
	return time.Duration(tm.base + ((s.n%3)+3)%3)
}

// sleep is one registered Sleep of the test Scheduler.
type sleep struct {
	d       time.Duration
	release chan struct{}
}

// releaser is a Scheduler whose sleeps end when the test releases them.
// Each Sleep announces itself on regs; a stop of the sleeping task ends it
// with an interruption.
type releaser struct{ regs chan sleep }

func (r releaser) Sleep(t *rt.Task, d time.Duration) {
	s := sleep{d: d, release: make(chan struct{})}
	stop := t.Done() // the sleep is registered: the runner's arm returns
	r.regs <- s
	select {
	case <-s.release:
	case <-stop:
		panic(rt.Interrupted)
	}
}

// tout is an output the sink got, with the state an observer that began
// a Changes loop at that moment saw first (rule 17: the state before the
// commit that gave the output).
type tout struct {
	out   int
	shown tstate
}

// start runs m as an actor whose outputs go to outs.
func (m tmachine) start(t *rt.Task, sl Sleeper, capacity int, gateOpen chan struct{}, entered chan struct{}, mu *sync.Mutex, outs *[]tout) *Actor[tstate, tsig, tdec] {
	// self is set before the first signal is sent, so before the first
	// output.
	var self *Actor[tstate, tsig, tdec]
	step := func(_ *rt.Task, s tstate, e tsig) (tdec, error) {
		if e.v == gate {
			entered <- struct{}{}
			<-gateOpen
			return tdec{kind: decideIgnore}, nil
		}
		return m.decide(s, e)
	}
	fire := func(t *rt.Task, at tstate, s tstate) (tdec, error) {
		tm := m.timers[at.v]
		return step(t, s, tsig{tm.sig, tm.x})
	}
	next := func(s tstate, d tdec) tstate {
		if d.kind == decideEnter || d.kind == decideStay || d.kind == decideEmit {
			return d.next
		}
		return s
	}
	out := func(at *rt.Task, d tdec) {
		var shown tstate
		for s := range self.Changes(at) {
			shown = s
			break
		}
		mu.Lock()
		*outs = append(*outs, tout{d.out, shown})
		mu.Unlock()
	}
	self = MachineStart(Site("actor test"), t, sl, m.init, step, fire, m.in,
		noWork[tstate], hasNoWork, runNoWork[tsig], func(d tdec) int { return d.kind }, next, out, capacity)
	return self
}

// noWork, hasNoWork and runNoWork are the work of a machine whose states
// have none.
func noWork[S any](S) struct{} { return struct{}{} }

func hasNoWork(struct{}) bool { return false }

func runNoWork[E any](*rt.Task, struct{}, func(E)) {}

// ref is the reference: the timed fold of the trace with one timer slot,
// as machines.RunTimed folds, except that a Reject of an outside signal
// keeps the state (the live actor goes on; its asker sees the Reject).
type ref struct {
	m        tmachine
	s        tstate
	outs     []tout
	now      int
	armed    bool
	deadline int
	due      tsig // the armed timer's signal, from the state its entry opened in
	ended    bool
	failed   bool
	defect   bool // an entry armed a timer that is not positive
	handled  int
	dropped  int
	seen     map[string]bool
	// epoch is the open entry's, and events the machine events the
	// runner must raise, in order (MU15).
	epoch  uint64
	events []rt.MachineEvent
}

// open opens an entry in r.s at r.now: it arms the slot, and reports
// whether the runner registers a sleep. A delay that is not positive is
// the defect that ends the actor.
func (r *ref) open() bool {
	r.armed = false
	d := r.m.in(r.s)
	if d != ActorNoTimer() && d <= 0 {
		r.ended, r.defect = true, true
		r.seen["a timer that is not positive is a defect"] = true
		r.exit("defected")
		return false
	}
	if d != ActorNoTimer() {
		tm := r.m.timers[r.s.v]
		r.armed, r.deadline, r.due = true, r.now+int(d), tsig{tm.sig, tm.x}
		r.events = append(r.events, rt.MachineEvent{What: rt.MachineTimerArmed, Epoch: r.epoch, State: stateVariant(r.s),
			Signal: sigVariant(r.due), In: d})
		return true
	}
	return false
}

// step steps e (a timer's signal when timer) and reports the decision,
// whether a new timer is armed, and the step's failure.
func (r *ref) step(e tsig, timer, asked bool) (tdec, bool, error) {
	r.take(e, timer, asked)
	d, err := r.m.decide(r.s, e)
	if err != nil {
		r.events = append(r.events, rt.MachineEvent{What: rt.MachineFail, Epoch: r.epoch, State: stateVariant(r.s)})
		if asked {
			r.seen["a failure to its asker"] = true
			return d, false, err
		}
		r.seen["a failure ends the actor"] = true
		r.ended, r.failed = true, true
		r.exit("failed")
		return d, false, err
	}
	r.decided(e, d)
	armed := false
	switch d.kind {
	case decideEnter, decideEmit:
		// The runner gives the Emit's output first, while the state before
		// is the one shown (rule 17), then commits and arms: a defect of
		// the arm ends the actor after the output and before the answer,
		// so the asker gets Stopped.
		if d.kind == decideEmit {
			r.outs = append(r.outs, tout{d.out, r.s})
			r.seen["an emit"] = true
		}
		r.s = d.next
		r.epoch++
		armed = r.open()
		if r.defect {
			if asked {
				return d, false, ActorStopped{}
			}
			return d, false, nil
		}
	case decideStay:
		r.s = d.next
		if r.armed {
			r.seen["a stay keeps the timer"] = true
		}
	case decideReject:
		if asked {
			r.seen["an asked reject"] = true
		}
	case decideDone:
		r.outs = append(r.outs, tout{d.out, r.s})
		r.ended = true
		r.seen["done"] = true
		r.exit("finished")
	}
	return d, armed, nil
}

// op is one outside event of a trace.
type op struct {
	at   int
	kind string // ask, send, tie, stop
	e    tsig
}

// driver drives the actor and the reference over one trace and reports
// the first difference.
type driver struct {
	t        *testing.T
	a        *Actor[tstate, tsig, tdec]
	r        *ref
	task     *rt.Task
	regs     chan sleep
	cur      sleep
	gateOpen chan struct{}
	entered  chan struct{}
	mu       sync.Mutex
	outs     []tout
	fail     string
}

func (dr *driver) failf(format string, args ...any) {
	if dr.fail == "" {
		dr.fail = fmt.Sprintf(format, args...)
	}
}

// expectArm takes the sleep the runner registers for a new entry and
// checks its delay.
func (dr *driver) expectArm() {
	s := <-dr.regs
	if want := time.Duration(dr.r.deadline - dr.r.now); s.d != want {
		dr.failf("timer armed for %v; want %v", s.d, want)
	}
	dr.cur = s
}

// idle waits until the runner has handled every envelope the reference
// counts, or has ended.
func (dr *driver) idle() {
	if dr.r.ended {
		<-dr.a.done
		return
	}
	for dr.a.handled.Load() < int64(dr.r.handled) {
		select {
		case <-dr.a.done:
			return
		default:
		}
		runtime.Gosched()
	}
}

// due reports whether the armed timer fires before at (before or at it
// when inclusive). Each armed delay is positive (rule 7), so each fire
// moves the clock and the fires of a trace are bounded.
func (dr *driver) due(at int, inclusive bool) bool {
	r := dr.r
	return r.armed && !r.ended && dr.fail == "" && r.deadline <= at && (inclusive || r.deadline < at)
}

// fireTimer releases the live timer at its deadline.
func (dr *driver) fireTimer() {
	r := dr.r
	r.now = r.deadline
	r.armed = false
	r.seen["a timer fired"] = true
	close(dr.cur.release)
	_, armed, _ := r.step(r.due, true, false)
	if !r.ended {
		r.handled++
	}
	dr.idle()
	if armed {
		dr.expectArm()
	}
}

// signal steps one outside op on both sides.
func (dr *driver) signal(o op) {
	r := dr.r
	switch o.kind {
	case "ask":
		d, err := dr.a.Ask(dr.task, o.e)
		wd, armed, werr := r.step(o.e, false, true)
		switch {
		case !errors.Is(err, werr) && (err != nil || werr != nil):
			dr.failf("ask %v: failure %v; want %v", o.e, err, werr)
		case err == nil && d.kind != wd.kind:
			dr.failf("ask %v: decision %d; want %d", o.e, d.kind, wd.kind)
		}
		if !r.ended {
			r.handled++
		}
		if armed {
			dr.expectArm()
		}
	case "send":
		if err := dr.a.Send(dr.task, o.e); err != nil {
			dr.failf("send %v: %v", o.e, err)
		}
		_, armed, _ := r.step(o.e, false, false)
		if !r.ended {
			r.handled += 2 // the send and the State read that follows it
		}
		got := dr.a.State(dr.task)
		if got != r.s {
			dr.failf("state after send %v: %v; want %v", o.e, got, r.s)
		}
		if armed {
			dr.expectArm()
		}
	case "tie":
		// The signal arrives at the timer's deadline: the runner is held in
		// a gate step while the signal and then the fired timer queue.
		if err := dr.a.Send(dr.task, tsig{v: gate}); err != nil {
			dr.failf("gate: %v", err)
		}
		<-dr.entered
		if err := dr.a.Send(dr.task, o.e); err != nil {
			dr.failf("tie send %v: %v", o.e, err)
		}
		close(dr.cur.release)
		for dr.a.inbox.Len() < 2 {
			runtime.Gosched()
		}
		dr.gateOpen <- struct{}{}
		r.handled++ // the gate
		r.take(tsig{v: gate}, false, false)
		r.decided(tsig{v: gate}, tdec{kind: decideIgnore})
		r.seen["a signal at its timer's deadline"] = true
		epochBefore := r.deadline
		stale, staleSig := r.epoch, r.due
		_, armed, _ := r.step(o.e, false, false)
		if !r.ended {
			r.handled++
		}
		if !r.ended {
			if armed || !r.armed || r.deadline != epochBefore {
				// The signal ended the timer's entry: the queued timer is
				// stale and dropped.
				r.dropped++
				r.handled++
				r.seen["a stale timer dropped"] = true
				r.events = append(r.events, rt.MachineEvent{What: rt.MachineDropped, Epoch: stale, Source: "timer",
					Signal: sigVariant(staleSig)})
				dr.idle()
				if armed {
					dr.expectArm()
				}
			} else {
				r.armed = false
				r.seen["a timer fired after a signal at its deadline"] = true
				_, armed2, _ := r.step(r.due, true, false)
				if !r.ended {
					r.handled++
				}
				dr.idle()
				if armed2 {
					dr.expectArm()
				}
			}
		} else {
			dr.idle()
		}
	}
}

// stopOf is a.Stop()'s failure and the defect it raised.
func stopOf(a *Actor[tstate, tsig, tdec]) (err error, raised any) {
	defer func() { raised = recover() }()
	return a.Stop(), nil
}

// actorCase is one generated machine and trace.
type actorCase struct {
	m        tmachine
	capacity int
	ops      []op
	end      int
}

func genActorCase(r *rand.Rand) actorCase {
	ns, ne := r.Intn(3)+1, r.Intn(3)+1
	m := tmachine{table: make([][]tcell, ns), timers: make([]ttimer, ns)}
	for i := range m.table {
		m.table[i] = make([]tcell, ne)
		for j := range m.table[i] {
			kind := []string{"enter", "stay", "emit", "ignore"}[r.Intn(4)]
			if r.Intn(6) == 0 {
				kind = []string{"reject", "done", "fail"}[r.Intn(3)]
			}
			m.table[i][j] = tcell{kind: kind, target: r.Intn(ns), delta: r.Intn(7) - 3}
		}
		if r.Intn(3) > 0 {
			m.timers[i] = ttimer{has: true, base: r.Intn(5) - 1, sig: r.Intn(ne), x: r.Intn(7) - 3}
		}
	}
	m.init = tstate{r.Intn(ns), r.Intn(9) - 4}
	// The arms: some single pairs, some rows or columns, in any order,
	// and often a last catch-all; a pair that no arm takes has none.
	for range r.Intn(ns*ne) + 1 {
		a := tarm{r.Intn(ns+1) - 1, r.Intn(ne+1) - 1}
		if r.Intn(3) > 0 {
			a = tarm{r.Intn(ns), r.Intn(ne)}
		}
		m.arms = append(m.arms, a)
	}
	if r.Intn(4) > 0 {
		m.arms = append(m.arms, tarm{-1, -1})
	}
	c := actorCase{m: m, capacity: r.Intn(3) + 2}
	at := 0
	for range r.Intn(12) + 1 {
		at += r.Intn(4)
		kind := []string{"ask", "send", "ask", "send", "tie"}[r.Intn(5)]
		if r.Intn(16) == 0 {
			kind = "stop"
		}
		c.ops = append(c.ops, op{at: at, kind: kind, e: tsig{r.Intn(ne), r.Intn(7) - 3}})
	}
	c.end = at + r.Intn(6)
	return c
}

// runActorCase runs c on both sides and returns the first difference.
func runActorCase(c actorCase, seen map[string]bool) string {
	regs := make(chan sleep, 4)
	// The run is traced (MU15): its machine events must be the
	// reference's, each decision with the arm the reference finds.
	log := &machineLog{}
	task := rt.TracedRoot("actor driver", log)
	registerTestGraph(c.m)
	dr := &driver{task: task, regs: regs, gateOpen: make(chan struct{}), entered: make(chan struct{}, 1)}
	dr.r = &ref{m: c.m, s: c.m.init, seen: seen}
	dr.a = c.m.start(task, releaser{regs}, c.capacity, dr.gateOpen, dr.entered, &dr.mu, &dr.outs)
	r := dr.r
	r.events = append(r.events, rt.MachineEvent{What: rt.MachineStart, Incarnation: 1, State: stateVariant(r.s)})
	if r.open() {
		dr.expectArm()
	}
	stopped := false
	for _, o := range c.ops {
		if r.ended || stopped {
			// An ended actor admits nothing.
			if _, err := dr.a.Ask(task, o.e); !errors.Is(err, ActorStopped{}) {
				dr.failf("ask after the end: %v; want actor.Stopped", err)
			}
			if err := dr.a.Send(task, o.e); !errors.Is(err, ActorStopped{}) {
				dr.failf("send after the end: %v; want actor.Stopped", err)
			}
			seen["refused after the end"] = true
			continue
		}
		for dr.due(o.at, false) {
			dr.fireTimer()
		}
		if r.ended {
			continue
		}
		r.now = max(r.now, o.at)
		switch {
		case o.kind == "stop":
			if err := dr.a.Stop(); err != nil {
				dr.failf("stop: %v", err)
			}
			stopped = true
			seen["a stop"] = true
			r.events = append(r.events, rt.MachineEvent{What: rt.MachineStop, Source: "stop"})
			r.exit("halted")
		case o.kind == "tie" && !(r.armed && r.deadline == o.at):
			dr.signal(op{at: o.at, kind: "send", e: o.e})
		default:
			dr.signal(o)
		}
		if dr.fail != "" {
			break
		}
	}
	for !stopped && dr.due(c.end, true) {
		dr.fireTimer()
	}
	if got := dr.a.State(task); got != r.s && dr.fail == "" {
		dr.failf("final state %v; want %v", got, r.s)
	}
	err, raised := stopOf(dr.a)
	r.events = append(r.events, rt.MachineEvent{What: rt.MachineStop, Source: "stop"})
	if !r.ended && !stopped {
		r.exit("halted")
	}
	if diff := log.diff(r.events); diff != "" {
		dr.failf("machine events: %s", diff)
	}
	if r.failed != errors.Is(err, errBoom) {
		dr.failf("Stop: %v; want boom %v", err, r.failed)
	}
	// The defect as the entry's report writes it.
	var text strings.Builder
	if x, ok := raised.(*exit); ok {
		report(&text, x.err)
	}
	if want := "panic: a Machine built from parts: the timer of state tstate{v:"; r.defect != (raised != nil) || r.defect && !strings.Contains(text.String(), want) {
		dr.failf("Stop raised %v (%q); want the timer defect %v", raised, text.String(), r.defect)
	}
	dr.mu.Lock()
	if !slices.Equal(dr.outs, r.outs) {
		dr.failf("outputs %v; want %v", dr.outs, r.outs)
	}
	dr.mu.Unlock()
	if got := dr.a.Dropped(); got != r.dropped {
		dr.failf("dropped %d; want %d", got, r.dropped)
	}
	select {
	case s := <-regs:
		dr.failf("a timer of %v armed that the reference never arms", s.d)
	default:
	}
	return dr.fail
}

// zeroDelayLoop is case 112 of seed 1, the first case of every seed: the
// Emit opens an entry of n 3 at time 0, whose timer (delay n%3 = 0) would
// re-enter with the same n and fire forever at time 0. The driver hung in
// it; a delay that is not positive is now the defect that ends the actor
// at the Emit, and the asker gets Stopped.
var zeroDelayLoop = actorCase{
	m: tmachine{table: [][]tcell{{{kind: "enter", delta: 1}, {kind: "emit"}}},
		timers: []ttimer{{has: true, x: -1}}, init: tstate{0, 2}},
	capacity: 2,
	ops:      []op{{at: 0, kind: "ask", e: tsig{1, 1}}, {at: 0, kind: "tie", e: tsig{0, 2}}},
	end:      5,
}

// TestActorModelAgreesWithTimedFold is the acceptance property of the
// runner (rule 10): over generated step tables, timers whose delay reads
// the state, and traces of Asks, Sends, signals at a timer's deadline and
// Stops on an exact virtual clock, the actor's decisions, states, outputs,
// armed delays and dropped count are the reference fold's. Cases are
// generated until every case below is reached. ERGO_ACTOR_SEED replays a
// seed; a failure prints the machine and the trace.
func TestActorModelAgreesWithTimedFold(t *testing.T) {
	seed := int64(1)
	if s := os.Getenv("ERGO_ACTOR_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	seen := map[string]bool{}
	want := []string{"a timer fired", "a stay keeps the timer", "a stale timer dropped", "a timer fired after a signal at its deadline",
		"a signal at its timer's deadline", "an emit", "done", "a failure to its asker", "a failure ends the actor", "an asked reject",
		"a stop", "refused after the end", "a timer that is not positive is a defect"}
	missing := func() string {
		for _, w := range want {
			if !seen[w] {
				return w
			}
		}
		return ""
	}
	n := 0
	for ; n < 200 || missing() != "" && n < 5000; n++ {
		c := zeroDelayLoop
		if n > 0 {
			c = genActorCase(r)
		}
		if diff := runActorCase(c, seen); diff != "" {
			t.Fatalf("seed %d, case %d: %s\nmachine %+v\ncapacity %d, trace %+v, end %d", seed, n, diff, c.m, c.capacity, c.ops, c.end)
		}
	}
	if m := missing(); m != "" {
		t.Fatalf("seed %d: %d cases reach no %q", seed, n, m)
	}
	t.Logf("seed %d: %d cases", seed, n)
}

// rdec is a decision of the recorder: its kind and the signal.
type rdec struct{ kind, e int }

// signalLog is a machine whose state is the list of the signals it
// stepped, in order. A negative signal holds the runner in its step: it
// tells entered (when not nil) and waits until hold closes.
func signalLog(t *rt.Task, capacity int, entered chan struct{}, hold chan struct{}) *Actor[[]int, int, rdec] {
	step := func(_ *rt.Task, s []int, e int) (rdec, error) {
		if e < 0 {
			if entered != nil {
				entered <- struct{}{}
			}
			<-hold
			return rdec{decideIgnore, e}, nil
		}
		return rdec{decideStay, e}, nil
	}
	return MachineStart(Site("signal log"), t, releaser{make(chan sleep)}, nil, step,
		func(*rt.Task, []int, []int) (rdec, error) { return rdec{kind: decideIgnore}, nil },
		func([]int) time.Duration { return ActorNoTimer() }, noWork[[]int], hasNoWork, runNoWork[int],
		func(d rdec) int { return d.kind },
		func(s []int, d rdec) []int {
			if d.kind == decideStay {
				return append(slices.Clone(s), d.e)
			}
			return s
		}, func(*rt.Task, rdec) {}, capacity)
}

// TestActorSendersKeepTheirOrder: three senders Send and Ask at once; the
// actor steps each sender's signals in the order it sent them (rule 2),
// each Ask is answered once (a decision or Stopped), and a Stop in the
// middle refuses every later Send.
func TestActorSendersKeepTheirOrder(t *testing.T) {
	for round := range 30 {
		root := rt.Root("senders")
		a := signalLog(root, round%3, nil, nil)
		var wg sync.WaitGroup
		answers := make([]int, 300)
		stopAt := 50 + round*7
		for s := range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				task := rt.Root(fmt.Sprintf("sender %d", s))
				for i := range 100 {
					e := s*1000 + i
					if i%3 == 0 {
						if _, err := a.Ask(task, e); err == nil || errors.Is(err, ActorStopped{}) {
							answers[s*100+i]++
						}
					} else {
						_ = a.Send(task, e)
					}
				}
			}()
		}
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			for len(a.State(rt.Root("stopper"))) < stopAt {
				select {
				case <-a.done:
					return
				default:
				}
				runtime.Gosched()
			}
			_ = a.Stop()
		}()
		wg.Wait()
		_ = a.Stop()
		<-stopped
		got := a.State(root)
		last := map[int]int{0: -1, 1: -1, 2: -1}
		for _, e := range got {
			s, i := e/1000, e%1000
			if i <= last[s] {
				t.Fatalf("round %d: sender %d's signal %d stepped after %d: %v", round, s, i, last[s], got)
			}
			last[s] = i
		}
		for k, n := range answers {
			if k%100%3 == 0 && n != 1 {
				t.Fatalf("round %d: ask %d answered %d times", round, k, n)
			}
		}
		if err := a.Send(root, 1); !errors.Is(err, ActorStopped{}) {
			t.Fatalf("round %d: send after Stop: %v", round, err)
		}
	}
}

// TestActorWaitsAreInterruptible: a Send waiting for room, an Ask waiting
// for its answer, a State read waiting for its answer and a Changes loop
// waiting for a change each end with an interruption when their task is
// stopped; the stopped Send's signal never enters the inbox.
func TestActorWaitsAreInterruptible(t *testing.T) {
	for _, capacity := range []int{0, 4} {
		root := rt.Root("waits")
		entered, hold := make(chan struct{}, 1), make(chan struct{})
		a := signalLog(root, capacity, entered, hold)
		go func() { _ = a.Send(rt.Root("holder"), -1) }()
		<-entered // the runner waits in the hold step
		for inboxParked(a) != 0 {
			runtime.Gosched() // the holder's Send has left the inbox's line
		}
		waits := []struct {
			name string
			op   func(*rt.Task)
		}{
			{"Send", func(w *rt.Task) { _ = a.Send(w, 5) }},
			{"Ask", func(w *rt.Task) { _, _ = a.Ask(w, 6) }},
			{"State", func(w *rt.Task) { a.State(w) }},
			{"Changes", func(w *rt.Task) {
				for range a.Changes(w) {
				}
			}},
		}
		for _, wt := range waits {
			if capacity == 4 && wt.name == "Send" {
				continue // with room, a Send does not wait
			}
			w := rt.Root(wt.name)
			done := make(chan bool, 1)
			for inboxParked(a) != 0 {
				runtime.Gosched() // the last stopped wait has left the line
			}
			parkedBefore, queuedBefore := 0, a.inbox.Len()
			go func() { done <- interruptedBy(func() { wt.op(w) }) }()
			if capacity == 0 && wt.name != "Changes" {
				// The operation is proven waiting for room before the stop.
				for inboxParked(a) == parkedBefore {
					runtime.Gosched()
				}
			}
			if capacity == 4 && (wt.name == "Ask" || wt.name == "State") {
				// Admitted: it waits for its answer.
				for a.inbox.Len() == queuedBefore {
					runtime.Gosched()
				}
			}
			w.Interrupt()
			select {
			case raised := <-done:
				if !raised {
					t.Fatalf("capacity %d, %s: the stopped wait returned instead of raising the interruption", capacity, wt.name)
				}
			case <-testwait.After(t):
				t.Fatalf("capacity %d, %s: the stopped wait did not end before the test's deadline", capacity, wt.name)
			}
		}
		close(hold)
		// The read is bounded: a runner that answers into a reused reply
		// channel blocks on it, and the test says so instead of hanging.
		answered := make(chan []int, 1)
		go func() { answered <- a.State(root) }()
		var got []int
		select {
		case got = <-answered:
		case <-testwait.After(t):
			t.Fatalf("capacity %d: State after the stopped waits got no answer before the test's deadline", capacity)
		}
		if capacity == 0 && len(got) != 0 {
			t.Fatalf("a stopped Send's or Ask's signal was stepped: %v", got)
		}
		// With room, the stopped Ask's signal was queued, so it is stepped;
		// its answer goes to a channel nobody reads, and this State gets
		// its own (MU18: a stopped Ask's reply channel is never reused).
		if capacity == 4 && !slices.Equal(got, []int{6}) {
			t.Fatalf("capacity 4: State after the stopped waits gave %v, want [6]", got)
		}
		_ = a.Stop()
	}
}

// inboxParked is how many operations wait in a's inbox.
func inboxParked[S, E, D any](a *Actor[S, E, D]) int {
	return a.inbox.waiters()
}

// TestActorStopCascades: a stop of the task that started the actor stops
// it: later Sends fail Stopped, and Stop returns no failure.
func TestActorStopCascades(t *testing.T) {
	starter := rt.Root("starter")
	a := signalLog(starter, 1, nil, nil)
	starter.Interrupt()
	select {
	case <-a.done:
	case <-testwait.After(t):
		t.Fatal("the actor did not end before the test's deadline after its starter's stop")
	}
	if err := a.Send(rt.Root("late"), 1); !errors.Is(err, ActorStopped{}) {
		t.Fatalf("send after the starter's stop: %v", err)
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestActorStopExitIsTheReferences: a Stop of an idle actor ends its
// runner with the exit the reference runner's raised interruption gives,
// though the mailbox runner ends without a panic (MU18).
func TestActorStopExitIsTheReferences(t *testing.T) {
	exits := map[bool]error{}
	for _, ref := range []bool{false, true} {
		old := actorReference
		actorReference = ref
		a := signalLog(rt.Root("stop"), 1, nil, nil)
		actorReference = old
		if err := a.Stop(); err != nil {
			t.Fatalf("reference %v: Stop: %v", ref, err)
		}
		a.mu.Lock()
		exits[ref] = a.exit
		a.mu.Unlock()
	}
	if exits[false] != rt.Interrupted || exits[true] != rt.Interrupted {
		t.Fatalf("the exit after Stop: mailbox runner %v, reference %v; want the interruption for both", exits[false], exits[true])
	}
}

// BenchmarkActorAsk is one Ask of a counter machine: admission, one step
// on the runner's goroutine, the commit and the answer.
func BenchmarkActorAsk(b *testing.B) {
	root := rt.Root("bench")
	a := MachineStart(Site("bench"), root, releaser{make(chan sleep)}, 0,
		func(_ *rt.Task, s int, e int) (int, error) { return decideStay, nil },
		func(*rt.Task, int, int) (int, error) { return decideIgnore, nil },
		func(int) time.Duration { return ActorNoTimer() }, noWork[int], hasNoWork, runNoWork[int],
		func(d int) int { return d },
		func(s int, d int) int { return s + 1 }, func(*rt.Task, int) {}, 1)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = a.Ask(root, 1)
	}
	_ = a.Stop()
}

// BenchmarkActorAskHandGo is the Go control of BenchmarkActorAsk: a
// goroutine that owns the state, a request channel with a reply channel
// per request, and a switch on the decision.
func BenchmarkActorAskHandGo(b *testing.B) {
	type req struct {
		e     int
		reply chan int
	}
	in := make(chan req, 1)
	go func() {
		s := 0
		for r := range in {
			switch r.e {
			case 0:
			default:
				s++
			}
			r.reply <- s
		}
	}()
	b.ReportAllocs()
	for b.Loop() {
		r := req{e: 1, reply: make(chan int, 1)}
		in <- r
		<-r.reply
	}
	close(in)
}
