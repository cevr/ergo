package v1

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// memberFailed is the typed failure of a generated member of TestSetModel.
type memberFailed struct{ id int }

func (e memberFailed) Error() string { return fmt.Sprintf("member %d failed", e.id) }

// setMember is a generated member: each run sleeps d seconds; the first
// defects runs panic, and the run after them ends with fail or success.
//
// A run that is stopped runs its cleanup, masked: it sleeps cleanup
// seconds, then panics when stopDefect holds (a defect in an exit action).
type setMember struct {
	d          int64
	defects    int
	fail       bool
	cleanup    int64
	stopDefect bool
}

// setOp is an owner step: go (start member m), sleep (s seconds), len,
// wait or stop.
type setOp struct {
	kind string
	m    int
	s    int64
}

// setCase is a generated run: the policy, the members and the owner's
// steps. The frame's end drains the Set after the last step.
type setCase struct {
	kind, limit int
	members     []setMember
	ops         []setOp
}

// setOutcome is what a run shows: each step's result, the time when the
// frame ended, the members' runs, the lines LogFailures wrote and the
// frame's exit.
type setOutcome struct {
	results []string
	at      int64
	runs    []int
	logged  []string
	exit    string
}

// TestSetModel is the model check of tasks.Set (runtime.md, G4; the red
// acceptance check of batch-13 tasks-set). 400 generated runs of up to 5
// members under each policy, on a VirtualClock (C1), against a
// discrete-event model: the owner starts members, sleeps, reads Len, waits
// and stops; the frame's end drains the Set. Each member's runs sleep a
// distinct power of four, scaled 1 to 3 times, and the owner sleeps whole
// thousands, so no two events share an instant and the model's order is
// the run's. The run must give the model's result at every step (Len,
// Closed, Wait's failure and the defects it raises, Stop's defects), run
// each member's body the model's number of times (no restart past the
// limit, none after the Set ended), log exactly the model's failures, end
// at the model's instant (a stopped member is not waited out), and leave
// no member running when its frame has ended. A failing seed prints the
// case.
func TestSetModel(t *testing.T) {
	before := runtime.NumGoroutine()
	reached := map[string]int{}
	for seed := uint64(0); seed < 400; seed++ {
		c := setDraw(seed)
		want := setModel(c)
		got := setRun(c)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("seed %d: case %+v\nrun   %+v\nmodel %+v", seed, c, got, want)
		}
		text := fmt.Sprint(want)
		for _, s := range []string{"closed", "wait failed", "wait defects", "stop defects", "len 1", "defect m", "failure "} {
			if strings.Contains(text, s) {
				reached[s]++
			}
		}
		if want.exit != "" {
			reached["drain defects"]++
		}
		for _, n := range want.runs {
			if n > 1 {
				reached["restart"]++
			}
		}
	}
	// The generator reaches every rule: Closed, Wait's failure and its
	// defects, Stop's defects, the drain's defects, a running member,
	// both kinds of logged exit and a restart.
	for _, s := range []string{"closed", "wait failed", "wait defects", "stop defects", "len 1", "defect m", "failure ", "restart", "drain defects"} {
		if reached[s] == 0 {
			t.Errorf("no generated case reaches %q", s)
		}
	}
	waitFor(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
}

func setDraw(seed uint64) setCase {
	r := rand.New(rand.NewPCG(seed, 31))
	c := setCase{kind: r.IntN(3), limit: r.IntN(3)}
	n := 1 + r.IntN(5)
	for i, p := range r.Perm(5)[:n] {
		_ = i
		// Cleanups sleep odd powers of two: a stopped member's end, its
		// stop's instant plus its cleanup, meets no other event.
		m := setMember{d: 1 << (2 * p), cleanup: 1 << (2*p + 1), stopDefect: r.IntN(3) == 0}
		switch r.IntN(4) {
		case 0:
			m.defects = 1 + r.IntN(2)
		case 1:
			m.fail = true
		case 2:
			m.d = 1 << 20 // runs past every sleep: a Wait or a stop ends it
		}
		c.members = append(c.members, m)
	}
	started := make([]bool, n)
	for range 3 + r.IntN(8) {
		switch r.IntN(6) {
		case 0, 1:
			m := r.IntN(n)
			if started[m] {
				continue // each member is started at most once
			}
			started[m] = true
			c.ops = append(c.ops, setOp{kind: "go", m: m})
		case 2:
			c.ops = append(c.ops, setOp{kind: "sleep", s: 1000 * int64(1+r.IntN(2))})
		case 3:
			c.ops = append(c.ops, setOp{kind: "len"})
		case 4:
			c.ops = append(c.ops, setOp{kind: "wait"})
		case 5:
			if r.IntN(3) == 0 {
				c.ops = append(c.ops, setOp{kind: "stop"})
			}
		}
	}
	return c
}

// setModel runs c as a discrete-event model of rules 2 to 6 of v1.Set.
func setModel(c setCase) setOutcome {
	type running struct {
		end      int64
		runs     int
		stopping bool // stopped: end is its cleanup's end
	}
	o := setOutcome{runs: make([]int, len(c.members))}
	live := map[int]*running{}
	var now int64
	closed := false
	failure := -1
	var defects []int
	// halt stops every running member at now: each ends after its cleanup.
	halt := func() {
		for id, r := range live {
			if !r.stopping {
				r.stopping, r.end = true, now+c.members[id].cleanup
			}
		}
	}
	// step moves time to the next member end at or before until, and
	// applies it; it reports whether one happened.
	step := func(until int64) bool {
		first := -1
		for id, r := range live {
			if r.end <= until && (first < 0 || r.end < live[first].end) {
				first = id
			}
		}
		if first < 0 {
			return false
		}
		r, m := live[first], c.members[first]
		now = r.end
		if r.stopping {
			// An interruption is no failure; a defect of its cleanup is
			// kept, since the Set has ended.
			delete(live, first)
			if m.stopDefect {
				defects = append(defects, first)
			}
			return true
		}
		defect := r.runs <= m.defects
		if defect && c.kind == setRestart && !closed && r.runs <= c.limit {
			r.runs++
			o.runs[first]++
			r.end = now + m.d
			return true
		}
		delete(live, first)
		switch {
		case !defect && !m.fail:
		case c.kind == setLogFailures && !closed:
			o.logged = append(o.logged, setText(first, defect))
		case closed:
			if defect {
				defects = append(defects, first)
			}
		default:
			closed = true
			if defect {
				defects = append(defects, first)
			} else {
				failure = first
			}
			halt() // the others are stopped
		}
		return true
	}
	stop := func() string {
		closed = true
		halt()
		for step(1 << 62) {
		}
		d := defects
		defects = nil
		return "stop" + setIDs(d)
	}
	for _, op := range c.ops {
		switch op.kind {
		case "go":
			if closed {
				o.results = append(o.results, "closed")
				continue
			}
			live[op.m] = &running{end: now + c.members[op.m].d, runs: 1}
			o.runs[op.m]++
			o.results = append(o.results, "go")
		case "sleep":
			until := now + op.s
			for step(until) {
			}
			now = until
		case "len":
			o.results = append(o.results, fmt.Sprintf("len %d", len(live)))
		case "wait":
			for step(1 << 62) {
			}
			o.results = append(o.results, setWaitText(failure, defects))
			defects = nil
		case "stop":
			o.results = append(o.results, stop())
		}
	}
	o.exit = strings.TrimPrefix(stop(), "stop")
	o.at = now
	slices.Sort(o.logged)
	return o
}

// setText is the line LogFailures writes for member id's exit, as the
// model and the run both name it.
func setText(id int, defect bool) string {
	if defect {
		return fmt.Sprintf("defect m%d", id)
	}
	return fmt.Sprintf("failure %d", id)
}

func setIDs(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	slices.Sort(ids)
	return fmt.Sprintf(" defects %v", ids)
}

func setWaitText(failure int, defects []int) string {
	s := "wait"
	if failure >= 0 {
		s += fmt.Sprintf(" failed %d", failure)
	}
	return s + setIDs(defects)
}

// setRun runs c through v1.Set on a root that made a VirtualClock's
// timeline.
func setRun(c setCase) setOutcome {
	root := rt.Root("model")
	line := NewVirtualTimeline(root, 0, 0)
	o := setOutcome{runs: make([]int, len(c.members))}
	var mu sync.Mutex
	var buf bytes.Buffer
	failureLogMu.Lock()
	old := failureLog
	failureLog = &buf
	failureLogMu.Unlock()
	defer func() {
		failureLogMu.Lock()
		failureLog = old
		failureLogMu.Unlock()
	}()
	// A task outside the Set sleeps past every event: a member's end that
	// left the owner marked as parked would let the clock jump to it.
	holder := rt.NewScope(root)
	bg := rt.Go(&holder, "background", func(ct *rt.Task) (struct{}, error) {
		return struct{}{}, rt.Sleep(ct, line.v, time.Duration(1<<21)*time.Second)
	})
	defer bg.Stop()
	exit := setCatch(root, func() {
		fs := NewScope(root)
		defer fs.End(nil)
		set := ownedSet(&fs, c.kind, c.limit)
		for _, op := range c.ops {
			switch op.kind {
			case "go":
				id, m := op.m, c.members[op.m]
				err := TasksGo(Site("model"), root, set, func(ct *rt.Task) error {
					mu.Lock()
					o.runs[id]++
					run := o.runs[id]
					mu.Unlock()
					defer func() {
						if ct.StopRequested() {
							Release(ct, func() {
								line.Wait(ct, time.Duration(m.cleanup)*time.Second)
								if m.stopDefect {
									panic(fmt.Sprintf("m%d", id))
								}
							})
						}
					}()
					line.Wait(ct, time.Duration(m.d)*time.Second)
					switch {
					case run <= m.defects:
						panic(fmt.Sprintf("m%d", id))
					case m.fail:
						return memberFailed{id}
					}
					return nil
				})
				if err != nil {
					o.results = append(o.results, "closed")
				} else {
					o.results = append(o.results, "go")
				}
			case "sleep":
				line.Wait(root, time.Duration(op.s)*time.Second)
			case "len":
				o.results = append(o.results, fmt.Sprintf("len %d", set.Len()))
			case "wait":
				var err error
				raised := setCatch(root, func() { err = set.Wait(root) })
				failure := -1
				if f, ok := err.(memberFailed); ok {
					failure = f.id
				}
				if raised != nil {
					if f, ok := rt.Failure(raisedFailure(raised)); ok {
						failure = f.(memberFailed).id
					}
				}
				o.results = append(o.results, setWaitText(failure, setDefectIDs(raised)))
			case "stop":
				raised := setCatch(root, func() { set.Stop(root) })
				o.results = append(o.results, "stop"+setIDs(setDefectIDs(raised)))
			}
		}
	})
	o.exit = setIDs(setDefectIDs(exit))
	o.at = line.Read().Mono / int64(time.Second)
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var id int
		switch {
		case l == "":
		case strings.HasPrefix(l, "panic: "):
			o.logged = append(o.logged, "defect "+strings.TrimPrefix(l, "panic: "))
		case strings.HasPrefix(l, "error: member "):
			fmt.Sscanf(l, "error: member %d failed", &id)
			o.logged = append(o.logged, setText(id, false))
		}
	}
	slices.Sort(o.logged)
	return o
}

// setCatch runs f and returns the exit it raised, nil when it returned.
func setCatch(t *rt.Task, f func()) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = Capture(t, p)
		}
	}()
	f()
	return nil
}

// raisedFailure is the typed failure among err's reasons, or nil.
func raisedFailure(err error) error {
	c, ok := err.(*rt.Cause)
	if !ok {
		return nil
	}
	for _, r := range c.Reasons {
		if r.Kind == rt.KindFailure {
			return r.Err
		}
	}
	return nil
}

// setDefectIDs are the members whose defects err holds, sorted.
func setDefectIDs(err error) []int {
	c, ok := err.(*rt.Cause)
	if !ok {
		return nil
	}
	var ids []int
	for _, r := range c.Reasons {
		if r.Kind != rt.KindDefect {
			continue
		}
		var id int
		if p, ok := r.Value.(*Panic); ok {
			fmt.Sscanf(fmt.Sprint(p.Value), "m%d", &id)
		} else {
			fmt.Sscanf(fmt.Sprint(r.Value), "m%d", &id)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// TestSetNoRestartAfterClose holds rule 3's "in an open Set": a member's
// defect after Stop closed admission, before Stop stopped the member, does
// not run the body again under Restart. The closeGap hook opens that
// window deterministically: it makes the member's first run panic and
// waits until the member either ended or started a second run. 64 rounds.
func TestSetNoRestartAfterClose(t *testing.T) {
	t.Cleanup(func() { closeGap = nil })
	for round := range 64 {
		root := rt.Root("gap")
		fs := NewScope(root)
		set := ownedSet(&fs, setRestart, 3)
		var mu sync.Mutex
		runs := 0
		panicNow := make(chan struct{})
		again := make(chan struct{})
		err := TasksGo(Site("gap"), root, set, func(ct *rt.Task) error {
			mu.Lock()
			runs++
			run := runs
			mu.Unlock()
			if run == 1 {
				<-panicNow
				panic("m0")
			}
			close(again)
			<-ct.Done()
			return nil
		})
		if err != nil {
			t.Fatalf("round %d: Go: %v", round, err)
		}
		hooked, restarted := false, false
		closeGap = func(s *Set) {
			hooked = true
			close(panicNow)
			for {
				s.mu.Lock()
				n := len(s.ended)
				s.mu.Unlock()
				if n > 0 {
					return
				}
				select {
				case <-again:
					restarted = true
					return
				default:
					runtime.Gosched()
				}
			}
		}
		stopErr := set.stop()
		closeGap = nil
		fs.End(nil)
		if !hooked {
			t.Fatalf("round %d: closeGap did not run", round)
		}
		if restarted || runs != 1 {
			t.Fatalf("round %d: a defect after Stop closed the Set ran the body again (runs %d)", round, runs)
		}
		if ids := setDefectIDs(stopErr); !slices.Equal(ids, []int{0}) {
			t.Fatalf("round %d: Stop gave %v, want the member's defect", round, stopErr)
		}
	}
}
