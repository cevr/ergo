//go:build ergoexplore

package v1

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/runtime/explore/sched"
	"ergo.local/prototype/runtime/rt"
)

// exploreMain runs main's body under the controlled scheduler (runtime.md,
// Explored schedules): `ergo test --explore` builds the test binary with
// the ergoexplore tag. Started as a worker (sched.WorkerEnv set, by `ergo test
// --explore MODE`), the binary runs the schedules a search asks for
// (workerMain); started alone, it runs one schedule, the cooperative one.
// A run that cannot end (a deadlock), that a schedule stopped, or that did
// a step outside the scheduler is a defect, and its text names the tasks.
func exploreMain(body func(*rt.Task) error) error {
	if os.Getenv(sched.WorkerEnv) != "" {
		return workerMain(body)
	}
	res := exploreRun(body, sched.Cooperative{})
	if why := exploreDefect(res); why != "" {
		return &rt.Cause{Reasons: []rt.Reason{{Kind: rt.KindDefect, Value: why}}}
	}
	return res.Exit
}

// explored is one explored run: rt's result and the first children law
// it broke (explore_laws.go), "" when none.
type explored struct {
	rt.Result
	law string
}

// exploreRun is one explored run of body under p, traced for the
// children laws.
func exploreRun(body func(*rt.Task) error, p sched.Picker) explored {
	s := &strategy{p: p}
	laws := newChildLaws()
	res := rt.ExploreTraced(time.Unix(0, 0).UTC(), s, laws, func(t *rt.Task) error {
		_, err := guarded(t, func(t *rt.Task) (struct{}, error) { return struct{}{}, body(t) })
		return err
	})
	return explored{res, laws.verdict()}
}

// exploreDefect is the text of a run that did not end as a program does,
// or broke a children law, or "".
func exploreDefect(res explored) string {
	switch {
	case res.law != "":
		return res.law
	case res.Escape != "":
		return "explore: " + res.Escape + " (a step outside the scheduler: the run does not replay)"
	case res.Stopped != nil:
		return "explore: the schedule stopped the run: " + res.Stopped.Error()
	case res.Deadlock != nil:
		var waits []string
		for _, p := range res.Deadlock {
			waits = append(waits, fmt.Sprintf("task %s waits to %v", p.Task, p.Kind))
		}
		return fmt.Sprintf("explore: no task can run after %d steps (a deadlock): %s", res.Picks, strings.Join(waits, "; "))
	}
	return ""
}

// workerMain serves the search on descriptors 3 and 4. It returns after a
// run that left a task: the process then ends, and its tasks with it.
func workerMain(body func(*rt.Task) error) error {
	in, out := os.NewFile(3, "explore-requests"), os.NewFile(4, "explore-replies")
	defer in.Close()
	defer out.Close()
	return workerServe(in, out, body)
}

// workerServe is the worker's loop over in and out.
func workerServe(in io.Reader, out io.Writer, body func(*rt.Task) error) error {
	return sched.Serve(in, out, func(p sched.Picker) sched.Outcome {
		return outcomeOf(exploreRun(body, p))
	})
}

// outcomeOf is a run's outcome for the search. A run that did not end
// with every task ended (a deadlock, a stop, an escape) is abandoned: its
// worker serves no more runs.
func outcomeOf(res explored) sched.Outcome {
	o := sched.Outcome{Failure: exploreDefect(res)}
	if o.Failure == "" && res.Exit != nil {
		o.Failure = res.Exit.Error()
	}
	if res.Stopped != nil {
		o.Stopped = res.Stopped.Error()
	}
	for _, p := range res.Deadlock {
		o.Deadlock = append(o.Deadlock, stepOf(p))
	}
	o.Abandoned = res.Escape != "" || res.Stopped != nil || res.Deadlock != nil
	return o
}

func stepOf(p rt.Pending) sched.Step {
	return sched.Step{Task: p.Task, Kind: p.Kind.String(), Handle: p.Handle}
}

// strategy is rt's Strategy over a sched.Picker. It keeps the paths the
// run has named, and the window of an explore.Replay: while one is open,
// its schedule picks, and the picker sees a choice of the one task.
type strategy struct {
	p      sched.Picker
	seen   map[string]bool
	paths  []string
	replay *replayWindow
}

// replayWindow is an explore.Replay's schedule, relative to the task that
// called it (the base, found at the first point after the call).
type replayWindow struct {
	rel   []string
	abs   []string
	start int // the step of the window's first point
}

func (s *strategy) Pick(c rt.Choice) (int, error) {
	sc := sched.Choice{Current: c.Current, Step: c.Step, Out: outputSize()}
	for _, p := range c.Enabled {
		sc.Enabled = append(sc.Enabled, stepOf(p))
	}
	if s.replay == nil {
		s.replay = takeReplay()
		if s.replay != nil {
			s.replay.arm(sc, s.paths)
		}
	}
	for _, e := range sc.Enabled {
		if s.seen == nil {
			s.seen = map[string]bool{}
		}
		if !s.seen[e.Task] {
			s.seen[e.Task] = true
			s.paths = append(s.paths, e.Task)
		}
	}
	if w := s.replay; w != nil {
		k, err := sched.Replay{Schedule: w.abs}.Pick(sched.Choice{Enabled: sc.Enabled, Current: sc.Current, Step: c.Step - w.start})
		if err != nil {
			return 0, fmt.Errorf("explore.Replay: %w", err)
		}
		if c.Step-w.start == len(w.abs)-1 {
			s.replay = nil
		}
		// The outer picker sees the one task the schedule names.
		one := sc
		one.Enabled, one.Current = []sched.Step{sc.Enabled[k]}, -1
		if sc.Current == k {
			one.Current = 0
		}
		if _, err := s.p.Pick(one); err != nil {
			return 0, err
		}
		return k, nil
	}
	return s.p.Pick(sc)
}

// arm fixes the window's base, the task at the first point after the
// call, and its paths in the run.
func (w *replayWindow) arm(c sched.Choice, seen []string) {
	base := "0"
	if c.Current >= 0 {
		base = c.Enabled[c.Current].Task
	}
	off := sched.Offset(seen, base)
	for _, r := range w.rel {
		w.abs = append(w.abs, sched.Absolute(r, base, off))
	}
	w.start = c.Step
}

var (
	replayMu      sync.Mutex
	replayPending *replayWindow
)

// takeReplay is the window an explore.Replay armed, once.
func takeReplay() *replayWindow {
	replayMu.Lock()
	defer replayMu.Unlock()
	w := replayPending
	replayPending = nil
	if w != nil && len(w.rel) == 0 {
		return nil
	}
	return w
}

// ExploreReplay is explore.Replay (stdlib.md, Testing): the next points of
// the explored run follow schedule, whose paths are relative to the
// calling task; a point whose task is not enabled stops the run with a
// defect that names the step. Outside an explored run it does nothing.
func ExploreReplay(schedule []string) {
	replayMu.Lock()
	defer replayMu.Unlock()
	replayPending = &replayWindow{rel: append([]string(nil), schedule...)}
}

// outputSize is the bytes written to standard output so far, when it is a
// file (a worker's), else 0.
func outputSize() int64 {
	fi, err := os.Stdout.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	return fi.Size()
}
