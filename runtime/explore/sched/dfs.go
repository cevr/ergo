package sched

import (
	"errors"
	"fmt"
	"slices"
)

// Options bound a search.
type Options struct {
	// Preempt is the most preemptions a schedule may have: picks of
	// another task while the task at the point could go on. Below zero
	// is no bound.
	Preempt int
	// Schedules is the most schedules a search runs; 0 is no bound.
	Schedules int
	// All goes on after a failing run (the property tests collect every
	// outcome); a search otherwise stops at its first failure.
	All bool
	// Visit, when set, sees each run that was not drained.
	Visit func(Run)
}

// Run is one schedule and what it did.
type Run struct {
	Choices  []Choice // each scheduling point
	Picks    []int    // the index picked at each
	Outcome  Outcome
	Drained  bool // the search had nothing new past a point and ran it out cooperatively
	Diverged string
}

// Schedule is the list of picked tasks, the replay's input.
func (r Run) Schedule() []string {
	out := make([]string, len(r.Picks))
	for i, k := range r.Picks {
		out[i] = r.Choices[i].Enabled[k].Task
	}
	return out
}

// Steps is the picked step at each point.
func (r Run) Steps() []Step {
	out := make([]Step, len(r.Picks))
	for i, k := range r.Picks {
		out[i] = r.Choices[i].Enabled[k]
	}
	return out
}

// Failed reports whether the run failed: its outcome names a failure, or
// it did not replay (a picker stopped it).
func (r Run) Failed() bool {
	return r.Outcome.Failure != "" || r.Outcome.Stopped != "" || r.Diverged != ""
}

// Report is what a search did, in counters: the schedules it ran, the
// scheduling points over all of them, the longest schedule, and the runs
// a sleep set blocked (every enabled task was asleep: its orders are
// covered by other runs). Proved holds when the search ended with no
// failure, inside the bound, before the budget.
type Report struct {
	Schedules, Points, Depth, SleepBlocked int
	Proved                                 bool
	Failure                                *Run
}

// record runs p on x and keeps each choice and pick.
type record struct {
	p   Picker
	run Run
}

func (r *record) Pick(c Choice) (int, error) {
	k, err := r.p.Pick(c)
	if err != nil {
		return k, err
	}
	r.run.Choices = append(r.run.Choices, c)
	r.run.Picks = append(r.run.Picks, k)
	return k, nil
}

// Once runs one schedule of p on x and returns it.
func Once(x Executor, p Picker) Run {
	r := &record{p: p}
	r.run.Outcome = x.Run(r)
	return r.run
}

// frame is one scheduling point on the search's current path.
type frame struct {
	choice    Choice
	pick      int
	tried     map[string]bool // tasks whose orders from here are explored
	backtrack map[string]bool // tasks still to try from here
	sleep     []Step          // tasks asleep here, with their steps
	preempts  int             // the preemptions on the path before here
}

// dfs is the Picker of a depth-first search over schedules: it replays the
// path of frames on its stack, then extends it with new frames, picking at
// each the task at the point when it may and the first other task when it
// may not.
type dfs struct {
	o        Options
	stack    []*frame
	n        int  // the picks of this run so far
	preempts int  // the preemptions of this run so far
	drained  bool // past a point with nothing new to try
	blocked  bool // drained by a sleep set
	diverged string
}

var errDiverged = errors.New("the run left the path it replays")

// reset prepares d for the next schedule.
func (d *dfs) reset() {
	d.n, d.preempts, d.drained, d.blocked, d.diverged = 0, 0, false, false, ""
}

// cost is the preemptions a pick of k at c costs.
func cost(c Choice, k int) int {
	if c.Current >= 0 && k != c.Current {
		return 1
	}
	return 0
}

// allowed reports whether a pick of k at f stays inside the bound.
func (d *dfs) allowed(f *frame, k int) bool {
	return d.o.Preempt < 0 || f.preempts+cost(f.choice, k) <= d.o.Preempt
}

func asleep(f *frame, task string) bool {
	for _, s := range f.sleep {
		if s.Task == task {
			return true
		}
	}
	return false
}

func sameChoice(a, b Choice) bool {
	if a.Current != b.Current || len(a.Enabled) != len(b.Enabled) {
		return false
	}
	for i := range a.Enabled {
		if a.Enabled[i] != b.Enabled[i] {
			return false
		}
	}
	return true
}

func cooperative(c Choice) int {
	if c.Current >= 0 {
		return c.Current
	}
	return 0
}

func (d *dfs) Pick(c Choice) (int, error) {
	i := d.n
	d.n++
	if d.drained {
		return cooperative(c), nil
	}
	if i < len(d.stack) {
		f := d.stack[i]
		if !sameChoice(f.choice, c) {
			d.diverged = fmt.Sprintf("at step %d the enabled tasks were %v, not %v as before", i, c.Enabled, f.choice.Enabled)
			return 0, errDiverged
		}
		d.preempts += cost(c, f.pick)
		return f.pick, nil
	}
	f := &frame{choice: c, preempts: d.preempts, tried: map[string]bool{}, backtrack: map[string]bool{}}
	if i > 0 && d.o.Preempt < 0 {
		// A task asleep at the parent, or already tried there, stays asleep
		// here when its step is independent of the step just taken: its
		// orders from here were explored from the parent. Under a bound
		// those orders may lie past the bound, so a bounded search keeps
		// no sleep sets (Coons et al., bounded POR).
		p := d.stack[i-1]
		taken := p.choice.Enabled[p.pick]
		var was []Step
		was = append(was, p.sleep...)
		for _, s := range p.choice.Enabled {
			if p.tried[s.Task] {
				was = append(was, s)
			}
		}
		for _, s := range was {
			if !Dependent(s, taken) && enabled(c, s) {
				f.sleep = append(f.sleep, s)
			}
		}
	}
	k := -1
	for _, j := range order(c) {
		if !asleep(f, c.Enabled[j].Task) && d.allowed(f, j) {
			k = j
			break
		}
	}
	if k < 0 {
		d.drained = true
		d.blocked = len(f.sleep) > 0
		return cooperative(c), nil
	}
	f.pick = k
	f.backtrack[c.Enabled[k].Task] = true
	d.stack = append(d.stack, f)
	d.preempts += cost(c, k)
	return k, nil
}

// enabled reports whether s, with its step, is enabled at c.
func enabled(c Choice, s Step) bool {
	for _, e := range c.Enabled {
		if e == s {
			return true
		}
	}
	return false
}

// order is the order in which a frame tries the enabled tasks: the task at
// the point first, then the others as they were started.
func order(c Choice) []int {
	out := make([]int, 0, len(c.Enabled))
	if c.Current >= 0 {
		out = append(out, c.Current)
	}
	for j := range c.Enabled {
		if j != c.Current {
			out = append(out, j)
		}
	}
	return out
}

// DFS searches the schedules of x depth first, with partial-order
// reduction: after each run, every race in its trace (two dependent steps
// of two tasks that no chain of dependent steps orders) adds the later
// task as a choice at the earlier step's point (Flanagan and Godefroid's
// rule; every task enabled there when it was not), and a sleep set keeps a
// task asleep while its orders are covered. With a bound, a choice is
// added at the start of the earlier task's stretch too (bounded POR), so
// every schedule inside the bound is covered by one that is explored.
func DFS(x Executor, o Options) Report {
	d := &dfs{o: o}
	var rep Report
	for {
		if o.Schedules > 0 && rep.Schedules >= o.Schedules {
			return rep
		}
		d.reset()
		r := Once(x, d)
		r.Drained, r.Diverged = d.drained, d.diverged
		rep.Schedules++
		rep.Points += len(r.Picks)
		rep.Depth = max(rep.Depth, len(r.Picks))
		if d.blocked {
			rep.SleepBlocked++
		}
		if r.Diverged == "" {
			d.races(r.Outcome.Deadlock)
		}
		if !r.Drained && r.Diverged == "" && o.Visit != nil {
			o.Visit(r)
		}
		if r.Failed() && (!o.All || r.Diverged != "") {
			rep.Failure = &r
			return rep
		}
		if !d.next() {
			rep.Proved = rep.Failure == nil
			return rep
		}
	}
}

// next moves the stack to the next path: the deepest frame with a task
// still to try gets it as its pick, and the frames below it go. It reports
// false when no frame has one.
func (d *dfs) next() bool {
	for len(d.stack) > 0 {
		f := d.stack[len(d.stack)-1]
		f.tried[f.choice.Enabled[f.pick].Task] = true
		for _, j := range order(f.choice) {
			t := f.choice.Enabled[j].Task
			if f.backtrack[t] && !f.tried[t] && !asleep(f, t) && d.allowed(f, j) {
				f.pick = j
				return true
			}
		}
		d.stack = d.stack[:len(d.stack)-1]
	}
	return false
}

// races adds the choices that the races of the current path need. At
// each point of the path, the step each enabled task would do next is
// checked against the steps taken before it (the picked one among them),
// and at the end so is the step each deadlocked task waits to do: the
// latest earlier step of another task that is dependent with it and does
// not happen before that task's last step is a race (Flanagan and
// Godefroid's rule). A clock maps each task to the number of its steps
// that happen before a step (a vector clock over dependent steps).
func (d *dfs) races(waiting []Step) {
	steps := make([]Step, 0, len(d.stack))
	for _, f := range d.stack {
		steps = append(steps, f.choice.Enabled[f.pick])
	}
	clocks := make([]map[string]int, len(steps))
	last := map[string]int{}
	check := func(n int, s Step) {
		var known map[string]int
		if l, ok := last[s.Task]; ok {
			known = clocks[l]
		}
		for j := n - 1; j >= 0; j-- {
			e := steps[j]
			if e.Task == s.Task || !Dependent(e, s) {
				continue
			}
			if known[e.Task] > j {
				continue // e happens before s's task's last step: no race
			}
			if d.add(j, s.Task) {
				return
			}
			// s's task was not enabled at j, so the two may never be
			// enabled together there: every task there is a choice, and
			// the search goes on to an earlier race.
		}
	}
	ended := map[string]bool{}
	var tasks []string
	for n, s := range steps {
		on := map[string]bool{}
		for _, q := range d.stack[n].choice.Enabled {
			on[q.Task] = true
			if !slices.Contains(tasks, q.Task) {
				tasks = append(tasks, q.Task)
			}
			check(n, q)
		}
		// A task that waits has the resume after its wake next; its
		// handle is not known, so it is dependent with every step.
		for _, t := range tasks {
			if !on[t] && !ended[t] {
				check(n, Step{Task: t, Kind: "resume"})
			}
		}
		if s.Kind == "end" {
			ended[s.Task] = true
		}
		c := map[string]int{}
		if l, ok := last[s.Task]; ok {
			for t, v := range clocks[l] {
				c[t] = v
			}
		}
		for j := 0; j < n; j++ {
			if steps[j].Task != s.Task && Dependent(steps[j], s) {
				for t, v := range clocks[j] {
					c[t] = max(c[t], v)
				}
			}
		}
		c[s.Task] = n + 1
		clocks[n] = c
		last[s.Task] = n
	}
	for _, w := range waiting {
		check(len(steps), w)
	}
}

// add makes task a choice at frame j: the task itself when it is enabled
// there, else every enabled task; it reports whether the task was. With a
// bound, the same choice goes to the frame where the task picked at j
// began its stretch of picks.
func (d *dfs) add(j int, task string) bool {
	put := func(f *frame) bool {
		for _, s := range f.choice.Enabled {
			if s.Task == task {
				f.backtrack[task] = true
				return true
			}
		}
		for _, s := range f.choice.Enabled {
			f.backtrack[s.Task] = true
		}
		return false
	}
	found := put(d.stack[j])
	if d.o.Preempt < 0 {
		return found
	}
	owner := d.stack[j].choice.Enabled[d.stack[j].pick].Task
	k := j
	for k > 0 {
		p := d.stack[k-1]
		if p.choice.Enabled[p.pick].Task != owner {
			break
		}
		k--
	}
	if k != j {
		put(d.stack[k])
	}
	return found
}
