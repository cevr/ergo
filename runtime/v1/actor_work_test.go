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

// The work of a machine's entries (actor.go rule 10; runtime.md, Machines
// and actors 12) against a reference fold: generated step tables whose
// states own no work, a one-shot work or a listener, driven by traces of
// Sends, Asks, work outcomes, work outcomes queued behind a re-entering
// signal, and Stops. Each work is a script the test commands; it logs its
// start and its end, and when it is interrupted it tries one more send,
// which the runner must refuse. Waits wait for events, never for a
// wall-clock bound.

// wwork is the Work value of a state: its kind (none, once, listen).
type wwork struct {
	s    tstate
	kind string
}

// wcmd is one command to a running work script.
type wcmd struct {
	kind string // value (send and end), emit (send, go on), end, defect
	e    tsig
}

// winst is one started work.
type winst struct {
	id   int
	s    tstate
	kind string
	cmd  chan wcmd
	ack  chan struct{}
}

// workDefect is the panic value of a work script's defect.
const workBoomText = "work boom"

// wdriver drives one case: the actor, the reference and the scripts' log.
type wdriver struct {
	a        *Actor[tstate, tsig, tdec]
	task     *rt.Task
	starts   chan *winst
	gateOpen chan struct{}
	entered  chan struct{}

	mu     sync.Mutex
	log    []string // "start k", "end k", in order
	nextID int
	late   []bool // per interrupted work: its late send was refused
	outs   []int

	r    *wref
	live *winst // the reference's running work, nil when none runs
	fail string
}

func (dr *wdriver) failf(format string, args ...any) {
	if dr.fail == "" {
		dr.fail = fmt.Sprintf(format, args...)
	}
}

func (dr *wdriver) logf(format string, args ...any) {
	dr.mu.Lock()
	dr.log = append(dr.log, fmt.Sprintf(format, args...))
	dr.mu.Unlock()
}

// run is the program's work: the script of one started work.
func (dr *wdriver) run(t *rt.Task, w wwork, send func(tsig)) {
	dr.mu.Lock()
	inst := &winst{id: dr.nextID, s: w.s, kind: w.kind, cmd: make(chan wcmd), ack: make(chan struct{}, 1)}
	dr.nextID++
	dr.mu.Unlock()
	dr.logf("start %d", inst.id)
	defer dr.logf("end %d", inst.id)
	dr.starts <- inst
	for {
		select {
		case c := <-inst.cmd:
			switch c.kind {
			case "value":
				send(c.e)
				inst.ack <- struct{}{}
				return
			case "emit":
				send(c.e)
				inst.ack <- struct{}{}
			case "end":
				inst.ack <- struct{}{}
				return
			case "defect":
				inst.ack <- struct{}{}
				panic(workBoomText)
			}
		case <-t.Done():
			// Interrupted: a careless work sends once more; the runner
			// must refuse the send at its check (rule 10).
			refused := interruptedBy(func() { send(tsig{v: 0, x: 99}) })
			dr.mu.Lock()
			dr.late = append(dr.late, refused)
			dr.mu.Unlock()
			panic(rt.Interrupted)
		}
	}
}

// wmachine is a step table with a work kind per state.
type wmachine struct {
	m     tmachine
	works []string
}

// wref is the reference fold: the state, the running work, and the counts
// the actor must match.
type wref struct {
	wm          wmachine
	s           tstate
	running     bool // the current entry's work runs
	kind        string
	ended       bool
	failed      bool
	defect      bool
	handled     int
	dropped     int
	interrupted int // works the end of their entry interrupted
	outs        []int
	seen        map[string]bool
}

// open opens an entry in r.s and reports whether its work starts.
func (r *wref) open() bool {
	r.kind = r.wm.works[r.s.v]
	r.running = r.kind != "none"
	return r.running
}

// leave ends the current entry: a running work is interrupted.
func (r *wref) leave() {
	if r.running {
		r.interrupted++
		r.seen["an interrupted work sends nothing"] = true
	}
	r.running = false
}

// step steps e in the reference; it reports the decision, whether a new
// work starts, and the step's failure.
func (r *wref) step(e tsig, asked bool) (tdec, bool, error) {
	d, err := r.wm.m.decide(r.s, e)
	if err != nil {
		if asked {
			return d, false, err
		}
		r.ended, r.failed = true, true
		r.leave()
		return d, false, err
	}
	started := false
	switch d.kind {
	case decideEnter, decideEmit:
		r.leave()
		r.s = d.next
		started = r.open()
		if d.kind == decideEmit {
			r.outs = append(r.outs, d.out)
		}
	case decideStay:
		r.s = d.next
		if r.running {
			r.seen["a stay keeps the work"] = true
		}
	case decideDone:
		r.outs = append(r.outs, d.out)
		r.ended = true
		r.leave()
	}
	return d, started, nil
}

// expectStart takes the work the runner starts for a new entry.
func (dr *wdriver) expectStart() {
	inst := <-dr.starts
	if inst.s != dr.r.s || inst.kind != dr.r.kind {
		dr.failf("work of %v (%s) started; want %v (%s)", inst.s, inst.kind, dr.r.s, dr.r.kind)
	}
	dr.live = inst
}

// idle waits until the runner has handled every envelope the reference
// counts, or has ended.
func (dr *wdriver) idle() {
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

// wop is one event of a trace.
type wop struct {
	kind string // send, ask, finish, race, stop
	e    tsig
	c    wcmd
}

// checkState reads the actor's state after the reference's.
func (dr *wdriver) checkState(what string) {
	if dr.r.ended {
		return
	}
	dr.r.handled++
	if got := dr.a.State(dr.task); got != dr.r.s {
		dr.failf("state after %s: %v; want %v", what, got, dr.r.s)
	}
}

// command gives the live work c and waits until it acted: its signal is
// queued, or it ended. A listener's emit keeps it running.
func (dr *wdriver) command(c wcmd) {
	dr.live.cmd <- c
	<-dr.live.ack
	if c.kind != "emit" {
		dr.r.running = false
		dr.live = nil
	}
}

func (dr *wdriver) do(o wop) {
	r := dr.r
	switch o.kind {
	case "send":
		if err := dr.a.Send(dr.task, o.e); err != nil {
			dr.failf("send %v: %v", o.e, err)
		}
		_, started, _ := r.step(o.e, false)
		if !r.ended {
			r.handled++
		}
		dr.idle()
		if started {
			dr.expectStart()
		}
		dr.checkState(fmt.Sprintf("send %v", o.e))
	case "ask":
		d, err := dr.a.Ask(dr.task, o.e)
		wd, started, werr := r.step(o.e, true)
		switch {
		case !errors.Is(err, werr) && (err != nil || werr != nil):
			dr.failf("ask %v: failure %v; want %v", o.e, err, werr)
		case err == nil && d.kind != wd.kind:
			dr.failf("ask %v: decision %d; want %d", o.e, d.kind, wd.kind)
		}
		if !r.ended {
			r.handled++
		}
		if started {
			dr.expectStart()
		}
	case "finish":
		dr.command(o.c)
		switch o.c.kind {
		case "value", "emit":
			r.seen["a work's "+o.c.kind+" is stepped"] = true
			_, started, _ := r.step(o.c.e, false)
			if !r.ended {
				r.handled++
			}
			dr.idle()
			if started {
				dr.expectStart()
			}
			dr.checkState(fmt.Sprintf("work %s %v", o.c.kind, o.c.e))
		case "end":
			r.seen["a work ends with no signal"] = true
		case "defect":
			r.seen["a work's defect ends the actor"] = true
			r.ended, r.defect = true, true
			r.running = false
			dr.idle()
		}
	case "race":
		// The work acts while a signal waits in the inbox: the runner is
		// held in a gate step, the signal queues, then the work's signal
		// (or defect) queues behind it.
		if err := dr.a.Send(dr.task, tsig{v: gate}); err != nil {
			dr.failf("gate: %v", err)
		}
		<-dr.entered
		if err := dr.a.Send(dr.task, o.e); err != nil {
			dr.failf("race send %v: %v", o.e, err)
		}
		dr.command(o.c)
		for dr.a.inbox.Len() < 2 {
			runtime.Gosched()
		}
		dr.gateOpen <- struct{}{}
		r.handled++ // the gate
		d, started, err := r.step(o.e, false)
		reentered := err == nil && (d.kind == decideEnter || d.kind == decideEmit)
		switch {
		case o.c.kind == "defect":
			// A defect is never dropped: it ends the actor after e (and
			// interrupts the work e's entry started), or joins the exit
			// when e ended it.
			r.seen["a defect queued behind a signal"] = true
			if started {
				dr.expectStart()
			}
			if !r.ended {
				r.leave()
			}
			r.ended, r.defect = true, true
			dr.idle()
		case r.ended:
			// e ended the actor: the queued work signal is released.
			dr.idle()
		case reentered:
			r.handled += 2 // e's step and the dropped work signal
			r.dropped++
			r.seen["a stale work signal is dropped"] = true
			dr.idle()
			if started {
				dr.expectStart()
			}
			dr.checkState("race")
		default:
			r.handled++
			r.seen["a work signal after a signal of its entry"] = true
			_, started2, _ := r.step(o.c.e, false)
			if !r.ended {
				r.handled++
			}
			dr.idle()
			if started2 {
				dr.expectStart()
			}
			dr.checkState("race")
		}
	}
}

// wcase is one generated machine and trace.
type wcase struct {
	wm       wmachine
	capacity int
	ops      []wop
}

func genWorkCase(r *rand.Rand) wcase {
	ns, ne := r.Intn(3)+1, r.Intn(3)+1
	m := tmachine{table: make([][]tcell, ns), timers: make([]ttimer, ns)}
	works := make([]string, ns)
	for i := range m.table {
		m.table[i] = make([]tcell, ne)
		for j := range m.table[i] {
			kind := []string{"enter", "stay", "emit", "ignore"}[r.Intn(4)]
			if r.Intn(8) == 0 {
				kind = []string{"reject", "done", "fail"}[r.Intn(3)]
			}
			m.table[i][j] = tcell{kind: kind, target: r.Intn(ns), delta: r.Intn(5) - 2}
		}
		works[i] = []string{"none", "once", "listen", "once", "listen"}[r.Intn(5)]
	}
	m.init = tstate{r.Intn(ns), r.Intn(5) - 2}
	c := wcase{wm: wmachine{m: m, works: works}, capacity: r.Intn(3) + 2}
	for range r.Intn(14) + 1 {
		e := tsig{r.Intn(ne), r.Intn(5) - 2}
		kind := []string{"send", "ask", "finish", "finish", "race", "race"}[r.Intn(6)]
		if r.Intn(20) == 0 {
			kind = "stop"
		}
		cmd := []string{"value", "emit", "end", "value", "emit"}[r.Intn(5)]
		if r.Intn(12) == 0 {
			cmd = "defect"
		}
		c.ops = append(c.ops, wop{kind: kind, e: e, c: wcmd{kind: cmd, e: tsig{r.Intn(ne), r.Intn(5) - 2}}})
	}
	return c
}

// fit makes o fit the reference's live work: a once work gets value or
// defect, a listener emit, end or defect; with no running work, a finish
// or race is a send.
func fit(o wop, r *wref) wop {
	if o.kind != "finish" && o.kind != "race" {
		return o
	}
	if !r.running {
		o.kind = "send"
		return o
	}
	switch {
	case r.kind == "once" && (o.c.kind == "emit" || o.c.kind == "end"):
		o.c.kind = "value"
	case r.kind == "listen" && o.c.kind == "value":
		o.c.kind = "emit"
	}
	if o.kind == "race" && o.c.kind == "end" {
		o.kind = "send"
	}
	return o
}

func runWorkCase(c wcase, seen map[string]bool) string {
	task := rt.Root("work driver")
	dr := &wdriver{task: task, starts: make(chan *winst, 8), gateOpen: make(chan struct{}), entered: make(chan struct{}, 1)}
	dr.r = &wref{wm: c.wm, s: c.wm.m.init, seen: seen}
	step := func(_ *rt.Task, s tstate, e tsig) (tdec, error) {
		if e.v == gate {
			dr.entered <- struct{}{}
			<-dr.gateOpen
			return tdec{kind: decideIgnore}, nil
		}
		return c.wm.m.decide(s, e)
	}
	next := func(s tstate, d tdec) tstate {
		if d.kind == decideEnter || d.kind == decideStay || d.kind == decideEmit {
			return d.next
		}
		return s
	}
	dr.a = MachineStart(Site("work test"), task, releaser{make(chan sleep)}, c.wm.m.init, step,
		func(*rt.Task, tstate, tstate) (tdec, error) { return tdec{kind: decideIgnore}, nil },
		func(tstate) time.Duration { return ActorNoTimer() },
		func(s tstate) wwork { return wwork{s: s, kind: c.wm.works[s.v]} },
		func(w wwork) bool { return w.kind != "none" }, dr.run,
		func(d tdec) int { return d.kind }, next,
		func(_ *rt.Task, d tdec) {
			dr.mu.Lock()
			dr.outs = append(dr.outs, d.out)
			dr.mu.Unlock()
		}, c.capacity)
	r := dr.r
	if r.open() {
		dr.expectStart()
	}
	stopped := false
	for _, o := range c.ops {
		if dr.fail != "" {
			break
		}
		if r.ended || stopped {
			if err := dr.a.Send(task, o.e); !errors.Is(err, ActorStopped{}) {
				dr.failf("send after the end: %v; want actor.Stopped", err)
			}
			continue
		}
		o = fit(o, r)
		if o.kind == "stop" {
			if err := dr.a.Stop(rt.Root("stop")); err != nil {
				dr.failf("stop: %v", err)
			}
			r.leave()
			stopped = true
			seen["a stop interrupts the work"] = true
			continue
		}
		dr.do(o)
	}
	if !r.ended && !stopped && dr.fail == "" {
		if got := dr.a.State(task); got != r.s {
			dr.failf("final state %v; want %v", got, r.s)
		}
	}
	if !r.ended && !stopped {
		r.leave()
	}
	err, raised := stopOfWork(dr.a)
	// A defect outranks a failure: Stop raises it and returns nothing.
	if !r.defect && r.failed != errors.Is(err, errBoom) {
		dr.failf("Stop: %v; want boom %v", err, r.failed)
	}
	var text strings.Builder
	if x, ok := raised.(*exit); ok {
		report(&text, x.err)
	}
	if r.defect != (raised != nil) || r.defect && !strings.Contains(text.String(), workBoomText) {
		dr.failf("Stop raised %v (%q); want the work defect %v", raised, text.String(), r.defect)
	}
	dr.mu.Lock()
	defer dr.mu.Unlock()
	if !slices.Equal(dr.outs, r.outs) {
		dr.failf("outputs %v; want %v", dr.outs, r.outs)
	}
	if got := dr.a.Dropped(); got != r.dropped {
		dr.failf("dropped %d; want %d", got, r.dropped)
	}
	for i, refused := range dr.late {
		if !refused {
			dr.failf("interrupted work %d: its late send was taken", i)
		}
	}
	if len(dr.late) != r.interrupted {
		dr.failf("%d works were interrupted; want %d", len(dr.late), r.interrupted)
	}
	// Exit before entry: each work ended before the next one started, and
	// every work ended by the time Stop returned.
	open := -1
	for _, ev := range dr.log {
		var k int
		if n, _ := fmt.Sscanf(ev, "start %d", &k); n == 1 {
			if open >= 0 {
				dr.failf("work %d started while work %d ran: %v", k, open, dr.log)
				break
			}
			open = k
		} else if n, _ := fmt.Sscanf(ev, "end %d", &k); n == 1 && k == open {
			open = -1
		}
	}
	if open >= 0 {
		dr.failf("work %d still ran after Stop: %v", open, dr.log)
	}
	select {
	case inst := <-dr.starts:
		dr.failf("a work of %v started that the reference never starts", inst.s)
	default:
	}
	return dr.fail
}

// stopOfWork is a.Stop()'s failure and the defect it raised.
func stopOfWork(a *Actor[tstate, tsig, tdec]) (err error, raised any) {
	defer func() { raised = recover() }()
	return a.Stop(rt.Root("stop")), nil
}

// TestActorWorkAgreesWithFold is the acceptance property of the entries'
// work (rule 10; MU4): over generated step tables whose states own no
// work, a one-shot work or a listener, and traces of Sends, Asks, work
// outcomes (a signal, a listener's signal, an end with no signal, a
// defect), outcomes queued behind a re-entering signal, and Stops, the
// actor's states, decisions, outputs, dropped count, started works, and
// Stop's failure and defect are the reference fold's; every interrupted
// work's late send is refused; each work ends before the next starts.
// ERGO_ACTOR_SEED replays a seed; a failure prints the machine and the
// trace.
func TestActorWorkAgreesWithFold(t *testing.T) {
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
	want := []string{"a work's value is stepped", "a work's emit is stepped", "a work ends with no signal",
		"a work's defect ends the actor", "a defect queued behind a signal", "a stale work signal is dropped",
		"a work signal after a signal of its entry", "an interrupted work sends nothing", "a stay keeps the work",
		"a stop interrupts the work"}
	missing := func() string {
		for _, w := range want {
			if !seen[w] {
				return w
			}
		}
		return ""
	}
	n := 0
	for ; n < 300 || missing() != "" && n < 5000; n++ {
		c := genWorkCase(r)
		if diff := runWorkCase(c, seen); diff != "" {
			t.Fatalf("seed %d, case %d: %s\nmachine %+v works %v\ncapacity %d, trace %+v", seed, n, diff, c.wm.m, c.wm.works, c.capacity, c.ops)
		}
	}
	if m := missing(); m != "" {
		t.Fatalf("seed %d: %d cases reach no %q", seed, n, m)
	}
	t.Logf("seed %d: %d cases", seed, n)
}
