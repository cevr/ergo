package sched

// Preemptions returns the number of preemptive picks in r (steps where another
// task was picked while the task that reached the point remained enabled).
func Preemptions(r Run) int {
	n := 0
	for i, k := range r.Picks {
		if i < len(r.Choices) {
			n += cost(r.Choices[i], k)
		}
	}
	return n
}

// sameFailure reports whether cand reproduces the exact failure of target
// cleanly under replay (without diverging or stopping early).
func sameFailure(cand, target Run) bool {
	if !cand.Failed() || cand.Diverged != "" || cand.Outcome.Stopped != "" {
		return false
	}
	if cand.Outcome.Failure != target.Outcome.Failure {
		return false
	}
	if (len(cand.Outcome.Deadlock) > 0) != (len(target.Outcome.Deadlock) > 0) {
		return false
	}
	return true
}

func betterSchedule(cand, best Run) bool {
	cp, bp := Preemptions(cand), Preemptions(best)
	if cp != bp {
		return cp < bp
	}
	return len(cand.Picks) < len(best.Picks)
}

type preemptTrigger struct {
	step    int
	curTask string
	curTurn int
	target  string
}

func extractTriggers(r Run) []preemptTrigger {
	turns := map[string]int{}
	var out []preemptTrigger
	for i, k := range r.Picks {
		if i >= len(r.Choices) {
			break
		}
		c := r.Choices[i]
		if c.Current < 0 || c.Current >= len(c.Enabled) {
			continue
		}
		cur := c.Enabled[c.Current].Task
		turn := turns[cur]
		turns[cur] = turn + 1
		if k != c.Current && k >= 0 && k < len(c.Enabled) {
			out = append(out, preemptTrigger{
				step:    i,
				curTask: cur,
				curTurn: turn,
				target:  c.Enabled[k].Task,
			})
		}
	}
	return out
}

type triggerKey struct {
	curTask string
	curTurn int
}

// guidedPicker replays a subset of preemptions (keyed by task-turn or step)
// and chooses cooperatively everywhere else so dropped preemptions never
// introduce spurious extra preemptions.
type guidedPicker struct {
	byTurn map[triggerKey]string
	byStep map[int]string
	turns  map[string]int
}

func newTurnPicker(ts []preemptTrigger) *guidedPicker {
	m := make(map[triggerKey]string, len(ts))
	for _, t := range ts {
		m[triggerKey{curTask: t.curTask, curTurn: t.curTurn}] = t.target
	}
	return &guidedPicker{byTurn: m, turns: map[string]int{}}
}

func newStepPicker(ts []preemptTrigger) *guidedPicker {
	m := make(map[int]string, len(ts))
	for _, t := range ts {
		m[t.step] = t.target
	}
	return &guidedPicker{byStep: m}
}

func (g *guidedPicker) Pick(c Choice) (int, error) {
	want := ""
	if g.byTurn != nil && c.Current >= 0 && c.Current < len(c.Enabled) {
		cur := c.Enabled[c.Current].Task
		turn := g.turns[cur]
		g.turns[cur] = turn + 1
		want = g.byTurn[triggerKey{curTask: cur, curTurn: turn}]
	} else if g.byStep != nil {
		want = g.byStep[c.Step]
	}
	if want != "" {
		for j, s := range c.Enabled {
			if s.Task == want {
				return j, nil
			}
		}
	}
	return cooperative(c), nil
}

const maxMinimizeRuns = 256

// Minimize shrinks a failing schedule by delta-debugging its preemptive picks
// under replay on x, returning a schedule that replays cleanly to the exact
// same failure with at most Preemptions(failing) preemptions (and fewer
// preemptions or picks whenever unnecessary picks can be dropped).
func Minimize(x Executor, failing Run) Run {
	if !failing.Failed() || failing.Diverged != "" || failing.Outcome.Stopped != "" {
		return failing
	}
	best := failing
	runs := 0
	tryCand := func(p Picker) bool {
		if runs >= maxMinimizeRuns {
			return false
		}
		runs++
		cand := Once(x, p)
		if !sameFailure(cand, failing) {
			return false
		}
		if !betterSchedule(cand, best) {
			return false
		}
		best = cand
		return true
	}

	if Preemptions(best) > 0 {
		if tryCand(Cooperative{}) {
			return verifyMinimized(x, failing, best)
		}
	}

	for runs < maxMinimizeRuns {
		triggers := extractTriggers(best)
		if len(triggers) == 0 {
			break
		}
		improved := false
		for n := 2; n <= len(triggers) && !improved && runs < maxMinimizeRuns; {
			chunk := (len(triggers) + n - 1) / n
			// First try keeping only one chunk (fast drop of noisy prefixes/suffixes).
			for lo := 0; lo < len(triggers) && !improved && runs < maxMinimizeRuns; lo += chunk {
				hi := min(lo+chunk, len(triggers))
				sub := triggers[lo:hi]
				if tryCand(newTurnPicker(sub)) || tryCand(newStepPicker(sub)) {
					improved = true
				}
			}
			// Then try dropping each chunk (classic ddmin complement).
			for lo := 0; lo < len(triggers) && !improved && runs < maxMinimizeRuns; lo += chunk {
				hi := min(lo+chunk, len(triggers))
				comp := make([]preemptTrigger, 0, len(triggers)-(hi-lo))
				comp = append(comp, triggers[:lo]...)
				comp = append(comp, triggers[hi:]...)
				if len(comp) == 0 {
					continue
				}
				if tryCand(newTurnPicker(comp)) || tryCand(newStepPicker(comp)) {
					improved = true
				}
			}
			if n == len(triggers) {
				break
			}
			n = min(n*2, len(triggers))
		}
		if !improved {
			break
		}
	}
	return verifyMinimized(x, failing, best)
}

func verifyMinimized(x Executor, failing, best Run) Run {
	replayed := Once(x, Replay{Schedule: best.Schedule()})
	if sameFailure(replayed, failing) {
		return replayed
	}
	return failing
}
