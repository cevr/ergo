package sched

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// Cooperative runs the task at the point while it can, and otherwise the
// first enabled task: the schedule with no preemption.
type Cooperative struct{}

// Pick is Picker's.
func (Cooperative) Pick(c Choice) (int, error) { return cooperative(c), nil }

// PCT is one schedule of probabilistic concurrency testing (Burckhardt et
// al., ASPLOS 2010) for a bug of depth d in a run of at most n tasks and
// k steps: each task gets a random priority from d+1 to d+n when it is
// first enabled (a task past the n-th gets the next priority above), the
// enabled task with the highest priority runs, and at d-1 random steps the
// task at the point drops to priority d-i, the i-th drop. Every choice is
// a function of the seed and the run's choices, so a seed replays.
type PCT struct {
	d       int
	prio    map[string]int
	perm    []int
	changes map[int]int // step -> its drop, from 1
}

// NewPCT is the PCT schedule of seed for depth d, n tasks and k steps.
func NewPCT(seed uint64, n, k, d int) *PCT {
	r := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	p := &PCT{d: d, prio: map[string]int{}, perm: r.Perm(max(n, 0)), changes: map[int]int{}}
	steps := r.Perm(max(k, 0))
	for i := 0; i < d-1 && i < len(steps); i++ {
		p.changes[steps[i]] = i + 1
	}
	return p
}

// Pick is Picker's.
func (p *PCT) Pick(c Choice) (int, error) {
	for _, s := range c.Enabled {
		if _, ok := p.prio[s.Task]; !ok {
			seen := len(p.prio)
			if seen < len(p.perm) {
				p.prio[s.Task] = p.d + 1 + p.perm[seen]
			} else {
				p.prio[s.Task] = p.d + 1 + seen
			}
		}
	}
	if i, ok := p.changes[c.Step]; ok && c.Current >= 0 {
		p.prio[c.Enabled[c.Current].Task] = p.d - i
	}
	best := 0
	for j, s := range c.Enabled {
		if p.prio[s.Task] > p.prio[c.Enabled[best].Task] {
			best = j
		}
	}
	return best, nil
}

// Random picks an enabled task uniformly at each point, from its seed.
type Random struct{ r *rand.Rand }

// NewRandom is the random walk of seed.
func NewRandom(seed uint64) *Random {
	return &Random{r: rand.New(rand.NewPCG(seed, 0x2545f4914f6cdd1d))}
}

// Pick is Picker's.
func (w *Random) Pick(c Choice) (int, error) { return w.r.IntN(len(c.Enabled)), nil }

// Replay runs one recorded schedule. A point whose recorded task is not
// enabled, or a run that goes on past the schedule's end, stops the run:
// the test is not deterministic under the scheduler, and the error names
// the first step that differs.
type Replay struct{ Schedule []string }

// Pick is Picker's.
func (r Replay) Pick(c Choice) (int, error) {
	if c.Step >= len(r.Schedule) {
		return 0, fmt.Errorf("the run goes on past the schedule's end at step %d; enabled: %s", c.Step, names(c))
	}
	want := r.Schedule[c.Step]
	for j, s := range c.Enabled {
		if s.Task == want {
			return j, nil
		}
	}
	return 0, fmt.Errorf("the run left the schedule at step %d: task %s is not enabled; enabled: %s", c.Step, want, names(c))
}

func names(c Choice) string {
	var out []string
	for _, s := range c.Enabled {
		out = append(out, s.Task)
	}
	return strings.Join(out, ", ")
}

// Sample runs count schedules of the picker that pick(i) makes, i from 0,
// and stops at the first failure: the PCT and random modes.
func Sample(x Executor, count int, pick func(i int) Picker) Report {
	var rep Report
	for i := 0; i < count; i++ {
		r := Once(x, pick(i))
		rep.Schedules++
		rep.Points += len(r.Picks)
		rep.Depth = max(rep.Depth, len(r.Picks))
		if r.Failed() {
			rep.Failure = &r
			return rep
		}
	}
	return rep
}
