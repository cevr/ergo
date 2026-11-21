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

	"ergo.local/prototype/runtime/rt"
)

// The exit actions of a machine's entries (actor.go rule 18; runtime.md,
// Machines and actors 12a; FK9-g1) against a reference fold: generated
// step tables whose states own a work or none, an exit action of a kind
// (none, log, wait, check, defect) or none, and a timer that is not
// positive or none, run as supervised actors (a budget of 0 or 2
// restarts), driven by traces of Sends (a step defect among them) and
// ended by Stop or Drain. One event log, written on the runner's task,
// holds each entry's open (its delay is read), its work's start (workOf)
// and end, and each exit action with the state it got; the reference fold
// gives the log the runner must write, in order. Waits wait for events,
// never for a wall-clock bound.

// xstate is one state of a generated machine: its work, its exit action,
// and whether its timer is not positive.
type xstate struct {
	work    bool
	exit    string // none, log, wait, check, defect
	badTime bool
}

// xmachine is a generated step table and its states.
type xmachine struct {
	m      tmachine
	states []xstate
	limit  int // restarts after a defect (Supervise's budget)
}

// xop is one event of a trace: a send of e (a step defect when e.x is
// stepDefect), or the end (stop, drain).
type xop struct {
	kind string
	e    tsig
}

// stepDefect is the e.x of a signal whose step panics.
const stepDefect = 77

const (
	exitBoomText = "exit boom"
	stepBoomText = "step boom"
)

// xref is the reference fold: the state, whether the entry's work runs,
// the restarts made, the handled count and the event log.
type xref struct {
	xm       xmachine
	s        tstate
	working  bool
	ended    bool
	failed   bool
	defects  []string
	restarts int
	handled  int
	stayed   bool // a Stay wrote the current entry's state
	log      []string
	seen     map[string]bool
}

// open opens an entry in s: its delay is read; a timer that is not
// positive raises there, so its work never starts; it reports the raise.
func (r *xref) open(s tstate) bool {
	r.s = s
	r.stayed = false
	r.log = append(r.log, fmt.Sprintf("open %d.%d", s.v, s.n))
	st := r.xm.states[s.v]
	if st.badTime {
		return true
	}
	if st.work {
		r.log = append(r.log, fmt.Sprintf("work %d.%d start", s.v, s.n))
		r.working = true
	}
	return false
}

// stopWork ends the current entry's work.
func (r *xref) stopWork() {
	if r.working {
		r.log = append(r.log, "work end")
		r.working = false
	}
}

// exit runs the exit action of the entry that ends in s, as cause; it
// reports the action's defect.
func (r *xref) exit(s tstate, cause string) bool {
	k := r.xm.states[s.v].exit
	if k == "none" {
		return false
	}
	r.log = append(r.log, fmt.Sprintf("exit %d.%d", s.v, s.n))
	r.seen["an exit action at "+cause] = true
	if k != "none" && r.xm.states[s.v].work {
		r.seen["an exit action after its entry's work"] = true
	}
	return k == "defect"
}

// fail ends the incarnation with a defect (what) while the entry is owed
// its exit action when owed: a restart within the budget, or the end.
func (r *xref) fail(what string, owed bool) {
	r.stopWork()
	if r.restarts < r.xm.limit {
		// The crash runs the owed action; its defect is the crash's.
		if owed {
			r.exit(r.s, "a crash")
		}
		r.restarts++
		r.seen["a restart after a defect"] = true
		if r.open(r.xm.m.init) {
			r.fail("timer", true)
		}
		return
	}
	r.defects = append(r.defects, what)
	if owed && r.exit(r.s, "the end after a defect") {
		r.defects = append(r.defects, exitBoomText)
	}
	r.ended = true
}

// send steps e.
func (r *xref) send(e tsig) {
	if e.x == stepDefect {
		r.fail(stepBoomText, true)
		return
	}
	d, err := r.xm.m.decide(r.s, e)
	if err != nil {
		r.stopWork()
		r.failed = true
		if r.exit(r.s, "a failure") {
			r.defects = append(r.defects, exitBoomText)
		}
		r.ended = true
		return
	}
	switch d.kind {
	case decideEnter, decideEmit:
		r.stopWork()
		cause := map[int]string{decideEnter: "an enter", decideEmit: "an emit"}[d.kind]
		if r.stayed && r.xm.states[r.s.v].exit != "none" {
			r.seen["an exit action after a stay"] = true
		}
		if r.exit(r.s, cause) {
			r.seen["a defect in an exit action"] = true
			r.fail(exitBoomText, false)
			return
		}
		r.handled++
		if r.open(d.next) {
			r.seen["an exit action of an entry whose arm raised"] = r.seen["an exit action of an entry whose arm raised"] || r.xm.states[d.next.v].exit != "none"
			r.handled--
			r.fail("timer", true)
		}
	case decideStay:
		r.s = d.next
		r.stayed = true
		r.handled++
	case decideDone:
		r.stopWork()
		r.exitEnd("a done")
	default:
		r.handled++
	}
}

// exitEnd ends the actor in r.s with its exit action, a defect of which
// joins the end.
func (r *xref) exitEnd(cause string) {
	if r.exit(r.s, cause) {
		r.defects = append(r.defects, exitBoomText)
	}
	r.ended = true
}

// xdriver drives one case: the actor and the log the runner writes.
type xdriver struct {
	a    *Actor[tstate, tsig, tdec]
	task *rt.Task
	mu   sync.Mutex
	log  []string
}

func (dr *xdriver) logf(format string, args ...any) {
	dr.mu.Lock()
	dr.log = append(dr.log, fmt.Sprintf(format, args...))
	dr.mu.Unlock()
}

// settle waits until the runner wrote n events and handled h envelopes,
// or ended.
func (dr *xdriver) settle(n, h int) {
	for {
		dr.mu.Lock()
		got := len(dr.log)
		dr.mu.Unlock()
		if got >= n && dr.a.handled.Load() >= int64(h) {
			return
		}
		select {
		case <-dr.a.done:
			return
		default:
		}
		runtime.Gosched()
	}
}

func genExitCase(r *rand.Rand) (xmachine, []xop) {
	ns, ne := r.Intn(3)+1, r.Intn(3)+1
	m := tmachine{table: make([][]tcell, ns), timers: make([]ttimer, ns)}
	states := make([]xstate, ns)
	for i := range m.table {
		m.table[i] = make([]tcell, ne)
		for j := range m.table[i] {
			kind := []string{"enter", "stay", "stay", "emit", "ignore"}[r.Intn(5)]
			if r.Intn(8) == 0 {
				kind = []string{"reject", "done", "fail"}[r.Intn(3)]
			}
			m.table[i][j] = tcell{kind: kind, target: r.Intn(ns), delta: r.Intn(5) - 2}
		}
		states[i] = xstate{
			work:    r.Intn(2) == 0,
			exit:    []string{"none", "log", "log", "wait", "check", "check", "defect"}[r.Intn(7)],
			badTime: i > 0 && r.Intn(10) == 0,
		}
	}
	m.init = tstate{0, r.Intn(5) - 2}
	xm := xmachine{m: m, states: states, limit: []int{0, 0, 2}[r.Intn(3)]}
	var ops []xop
	for range r.Intn(10) {
		e := tsig{r.Intn(ne), r.Intn(5) - 2}
		if r.Intn(15) == 0 {
			e.x = stepDefect
		}
		ops = append(ops, xop{kind: "send", e: e})
	}
	ops = append(ops, xop{kind: []string{"stop", "drain"}[r.Intn(2)]})
	return xm, ops
}

// xreleaser is a Scheduler whose sleeps end at once, unless a stop of the
// sleeping task ends them first.
type xreleaser struct{}

func (xreleaser) Sleep(t *rt.Task, d time.Duration) {
	select {
	case <-t.Done():
		panic(rt.Interrupted)
	default:
	}
}

func runExitCase(xm xmachine, ops []xop, seen map[string]bool) (string, []string, []string) {
	task := rt.Root("exit driver")
	dr := &xdriver{task: task}
	ref := &xref{xm: xm, seen: seen}
	step := func(_ *rt.Task, s tstate, e tsig) (tdec, error) {
		if e.x == stepDefect {
			panic(stepBoomText)
		}
		return xm.m.decide(s, e)
	}
	next := func(s tstate, d tdec) tstate {
		if d.kind == decideEnter || d.kind == decideStay || d.kind == decideEmit {
			return d.next
		}
		return s
	}
	delay := func(s tstate) time.Duration {
		dr.logf("open %d.%d", s.v, s.n)
		if xm.states[s.v].badTime {
			return 0
		}
		return ActorNoTimer()
	}
	workOf := func(s tstate) tstate {
		if xm.states[s.v].work {
			dr.logf("work %d.%d start", s.v, s.n)
		}
		return s
	}
	work := func(t *rt.Task, s tstate, _ func(tsig)) {
		<-t.Done()
		dr.logf("work end")
		panic(rt.Interrupted)
	}
	leave := func(t *rt.Task, s tstate) {
		switch xm.states[s.v].exit {
		case "check":
			// Masked: a stop requested before or during the action does
			// not cut it.
			t.Check()
		case "wait":
			xreleaser{}.Sleep(t, 1)
		}
		dr.logf("exit %d.%d", s.v, s.n)
		if xm.states[s.v].exit == "defect" {
			panic(exitBoomText)
		}
	}
	fresh := func(*rt.Task) (tstate, error) { return xm.m.init, nil }
	dr.a = MachineSupervise(Site("exit test"), task, xreleaser{}, nil, xm.m.init, fresh, xm.limit, step,
		func(*rt.Task, tstate, tstate) (tdec, error) { return tdec{kind: decideIgnore}, nil },
		delay, workOf, func(s tstate) bool { return xm.states[s.v].work }, work,
		func(s tstate) bool { return xm.states[s.v].exit != "none" }, leave,
		func(d tdec) int { return d.kind }, next, func(*rt.Task, tdec) {}, 4)
	if ref.open(xm.m.init) {
		ref.fail("timer", true)
	}
	var err error
	var raised any
	for _, o := range ops {
		if ref.ended {
			break
		}
		switch o.kind {
		case "send":
			if e := dr.a.Send(task, o.e); e != nil {
				return fmt.Sprintf("send %v: %v", o.e, e), dr.snapshot(), ref.log
			}
			ref.send(o.e)
			if ref.ended {
				<-dr.a.done
			} else {
				dr.settle(len(ref.log), ref.handled)
			}
		case "stop", "drain":
			ref.stopWork()
			ref.exitEnd("a " + o.kind)
			err, raised = endOf(dr.a, o.kind)
		}
	}
	if raised == nil && err == nil {
		// A trace the actor ended before its end op.
		err, raised = endOf(dr.a, "stop")
	}
	var text strings.Builder
	if x, ok := raised.(*exit); ok {
		report(&text, x.err)
	}
	got := dr.snapshot()
	switch {
	case !slices.Equal(got, ref.log):
		return "the event logs differ", got, ref.log
	case len(ref.defects) > 0 != (raised != nil):
		return fmt.Sprintf("the end raised %v; want the defects %v", raised, ref.defects), got, ref.log
	case !ref.failed && len(ref.defects) == 0 && err != nil:
		return fmt.Sprintf("the end gave %v; want nothing", err), got, ref.log
	case ref.failed && len(ref.defects) == 0 && !errors.Is(err, errBoom):
		return fmt.Sprintf("the end gave %v; want boom", err), got, ref.log
	}
	for _, d := range ref.defects {
		if !strings.Contains(text.String(), d) {
			return fmt.Sprintf("the end's report %q has no %q", text.String(), d), got, ref.log
		}
	}
	if n := strings.Count(text.String(), exitBoomText); n != strings.Count(strings.Join(ref.defects, "\n"), exitBoomText) {
		return fmt.Sprintf("the end's report has %d exit defects; want %v", n, ref.defects), got, ref.log
	}
	if got := ActorRestarts(dr.a); got != ref.restarts {
		return fmt.Sprintf("%d restarts; want %d", got, ref.restarts), dr.snapshot(), ref.log
	}
	return "", got, ref.log
}

func (dr *xdriver) snapshot() []string {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	return slices.Clone(dr.log)
}

// endOf ends a by Stop or Drain: its failure and the defect it raised.
func endOf(a *Actor[tstate, tsig, tdec], kind string) (err error, raised any) {
	defer func() { raised = recover() }()
	if kind == "drain" {
		return a.Drain(rt.Root("drain")), nil
	}
	return a.Stop(rt.Root("stop")), nil
}

// TestActorExitAgreesWithFold is the acceptance property of exit actions
// (rule 18; FK9-g1): over generated step tables whose states own a work
// or none, an exit action that logs, waits on the Scheduler, checks for a
// stop (masked, it never raises), raises a defect, or none, and a timer
// that is not positive, run supervised with a budget of 0 or 2 restarts
// over traces of Sends (step defects among them) ended by Stop or Drain,
// the runner's event log (each entry's open, its work's start and end,
// each exit action with its state) is the reference fold's, in order; and
// the end's failure, defects and restarts are the fold's. So each entry
// that opened ends once with the state it holds then, after its work ended
// and before the next entry opens, on every path: an Enter, an Emit, a
// Done, a failure, a defect, a crash, an arm that raised, Stop and Drain.
// The generator must reach each path. Its first case is FK9-g1's smallest
// repro: two Stays and a Stop give the action the stayed state.
// ERGO_ACTOR_SEED replays a seed; a failure prints the machine, the
// trace, both logs and the replay command.
func TestActorExitAgreesWithFold(t *testing.T) {
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
	want := []string{"an exit action at an enter", "an exit action at an emit", "an exit action at a done",
		"an exit action at a failure", "an exit action at a stop", "an exit action at a drain",
		"an exit action at the end after a defect", "an exit action at a crash", "a defect in an exit action",
		"an exit action after a stay", "an exit action of an entry whose arm raised",
		"an exit action after its entry's work", "a restart after a defect"}
	missing := func() string {
		for _, w := range want {
			if !seen[w] {
				return w
			}
		}
		return ""
	}
	// FK9-g1's smallest repro: a state with an exit action, two Stays,
	// then Stop: the action gets the state after both Stays.
	repro := xmachine{m: tmachine{table: [][]tcell{{{kind: "stay", target: 0, delta: 1}}}, timers: make([]ttimer, 1), init: tstate{0, 0}},
		states: []xstate{{exit: "log"}}}
	cases := []func() (xmachine, []xop){func() (xmachine, []xop) {
		return repro, []xop{{kind: "send", e: tsig{0, 0}}, {kind: "send", e: tsig{0, 0}}, {kind: "stop"}}
	}}
	n := 0
	for ; n < 400 || missing() != "" && n < 5000; n++ {
		xm, ops := genExitCase(r)
		if n < len(cases) {
			xm, ops = cases[n]()
		}
		if diff, got, ref := runExitCase(xm, ops, seen); diff != "" {
			t.Fatalf("seed %d, case %d: %s\nmachine %+v states %+v limit %d\ntrace %+v\nrunner    %v\nreference %v\nreplay: ERGO_ACTOR_SEED=%d go test -run TestActorExitAgreesWithFold ./runtime/v1",
				seed, n, diff, xm.m, xm.states, xm.limit, ops, got, ref, seed)
		}
	}
	if m := missing(); m != "" {
		t.Fatalf("seed %d: %d cases reach no %q", seed, n, m)
	}
	t.Logf("seed %d: %d cases", seed, n)
}
