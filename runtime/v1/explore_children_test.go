//go:build ergoexplore

package v1

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"ergo.local/prototype/runtime/explore/sched"
	"ergo.local/prototype/runtime/rt"
)

// The router of internal/sketch/lower/testdata/work/children.ergo on the
// real runner, under explored schedules (X7). The router's first entry
// starts two editor children and tells them to save; each save is an
// output to the router. The test re-enters the router three times and
// closes it while the outputs may still be queued, then stops it. The
// entry's work waits until it is stopped, so a stop reaches it with the
// children still live.
const (
	rSaved = iota // a child's output
	rNext         // a new entry
	rClose        // the last entry, with no work
)

type rstate struct {
	entry  int
	closed bool
	got    int
}

type xrdec struct {
	k int
	s rstate
}

const (
	eType = iota
	eSave
)

type edec struct {
	k     int
	edits int
}

// noSleep is the routers' Sleeper: no state has a timer.
type noSleep struct{}

func (noSleep) Sleep(*rt.Task, time.Duration) { panic("no state of the routers has a timer") }

func editorStep(_ *rt.Task, edits int, e int) (edec, error) {
	if e == eSave {
		return edec{k: decideEmit, edits: 0}, nil
	}
	return edec{k: decideStay, edits: edits + 1}, nil
}

func routerStep(_ *rt.Task, s rstate, e int) (xrdec, error) {
	switch {
	case s.closed:
		return xrdec{k: decideIgnore}, nil
	case e == rSaved:
		s.got++
		return xrdec{k: decideStay, s: s}, nil
	case e == rNext:
		return xrdec{k: decideEnter, s: rstate{entry: s.entry + 1}}, nil
	}
	return xrdec{k: decideEnter, s: rstate{entry: s.entry + 1, closed: true}}, nil
}

// routerWork is the first entry's work: two editors, a type and a save
// for the first, a save for the second, then a wait for the stop.
func routerWork(wt *rt.Task, _ rstate, _ func(int)) {
	start := func(name string) *Actor[int, int, edec] {
		return MachineChild(Site(name), wt, noSleep{}, nil, 0, editorStep, nil,
			func(int) time.Duration { return noTimer }, func(s int) int { return s }, func(int) bool { return false },
			func(*rt.Task, int, func(int)) {}, func(d edec) int { return d.k },
			func(s int, d edec) int {
				if d.k == decideIgnore {
					return s
				}
				return d.edits
			},
			func(_ *rt.Task, d edec, deliver func(int)) {
				if d.k == decideEmit {
					deliver(rSaved)
				}
			}, 2)
	}
	a, b := start("editor a"), start("editor b")
	for _, s := range []struct {
		to *Actor[int, int, edec]
		e  int
	}{{a, eType}, {a, eSave}, {b, eSave}} {
		if err := s.to.Send(wt, s.e); err != nil {
			return
		}
	}
	_, _ = New[int](1).Pop(wt)
}

func routerProgram(t *rt.Task) error {
	r := MachineStart(Site("router"), t, noSleep{}, rstate{}, routerStep, nil,
		func(rstate) time.Duration { return noTimer }, func(s rstate) rstate { return s },
		func(s rstate) bool { return !s.closed && s.entry == 0 }, routerWork,
		func(d xrdec) int { return d.k }, func(s rstate, d xrdec) rstate {
			if d.k == decideEnter || d.k == decideStay {
				return d.s
			}
			return s
		}, func(*rt.Task, xrdec) {}, 8)
	for _, e := range []int{rNext, rNext, rNext, rClose} {
		if err := r.Send(t, e); err != nil {
			return err
		}
	}
	return r.Stop(t)
}

// TestChildrenLawsExplored searches the router's schedules with at most
// 0, then 1, then 2 preemptions (`ergo test --explore dfs`'s bound is 2)
// and fails at the first schedule that breaks a children law
// (explore_laws.go), with the schedule as a token, after it replays the
// schedule to the same failure. Each runner mutant of the runtime/v1
// ledger, which changes how an entry keeps, stops or hears its children,
// is found this way. It runs with the ergoexplore tag (the gate's
// explored worker step). It runs the real runner because the laws are the
// runner's: no model of it would show the runner's own orders.
//
// ERGO_EXPLORE_PREEMPT=b searches the one bound b. ERGO_EXPLORE_MODE=pct
// or random runs ERGO_EXPLORE_SCHEDULES seeded schedules (default 100)
// instead, for the request's comparison.
func TestChildrenLawsExplored(t *testing.T) {
	pool := &sched.Pool{Start: func() (sched.Conn, error) { return startPipeWorker(routerProgram) }}
	defer pool.Close()
	var rep sched.Report
	switch mode := os.Getenv("ERGO_EXPLORE_MODE"); mode {
	case "", "dfs":
		// Iterative bounding (CHESS): each bound in turn, with its own
		// budget. A search with bound 2 alone runs its 10,000 schedules
		// deep in the last tasks' orders and misses x7-late-output and
		// x7-epoch-wraps, which bound 1 finds (the request's table).
		bounds := []int{0, 1, 2}
		if p := os.Getenv("ERGO_EXPLORE_PREEMPT"); p != "" {
			b, _ := strconv.Atoi(p)
			bounds = []int{b}
		}
		for _, b := range bounds {
			r := sched.DFS(pool, sched.Options{Preempt: b, Schedules: 5_000})
			rep.Schedules += r.Schedules
			rep.Points += r.Points
			rep.Depth = max(rep.Depth, r.Depth)
			rep.Proved, rep.Failure = r.Proved, r.Failure
			t.Logf("bound %d: %d schedules, proved %v", b, r.Schedules, r.Proved)
			if r.Failure != nil {
				break
			}
		}
	default:
		n, _ := strconv.Atoi(os.Getenv("ERGO_EXPLORE_SCHEDULES"))
		if n == 0 {
			n = 100
		}
		// PCT's n and k from the cooperative run, as `ergo test --explore
		// pct` reads them.
		first := sched.Once(pool, sched.Cooperative{})
		tasks := map[string]bool{}
		for _, c := range first.Choices {
			for _, s := range c.Enabled {
				tasks[s.Task] = true
			}
		}
		rep = sched.Sample(pool, n, func(i int) sched.Picker {
			if mode == "pct" {
				return sched.NewPCT(uint64(i+1), len(tasks), len(first.Picks), 3)
			}
			return sched.NewRandom(uint64(i + 1))
		})
	}
	t.Logf("%d schedules, %d points, depth %d, proved %v", rep.Schedules, rep.Points, rep.Depth, rep.Proved)
	if rep.Failure == nil {
		return
	}
	r := *rep.Failure
	again := sched.Once(pool, sched.Replay{Schedule: r.Schedule()})
	replay := "the schedule replays to the same failure"
	if again.Outcome.Failure != r.Outcome.Failure {
		replay = fmt.Sprintf("the schedule does not replay: %q", again.Outcome.Failure+again.Outcome.Stopped)
	}
	t.Fatalf("schedule %d: %s\nschedule %s\n%s", rep.Schedules, r.Outcome.Failure, sched.Token(r.Schedule()), replay)
}
