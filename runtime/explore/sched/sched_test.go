package sched

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// naiveLimit is the most schedules the naive reference enumerates: a
// program with more is drawn again (a work counter, never a time).
const naiveLimit = 3000

// TestDPORFindsEveryOutcome is the acceptance property of the search
// (plan X6): on generated programs (2 to 4 tasks, up to 4 operations
// each, on up to 3 handles: queues, inboxes, a Ref; those whose naive
// enumeration ends inside naiveLimit), DFS with partial-order
// reduction reaches the same set of outcomes, deadlocks included, as a
// naive enumeration of every schedule, and runs no more schedules; with a
// preemption bound B, the same set as the naive enumeration of every
// schedule of at most B preemptions. Each run DFS visits replays from its
// token to the same outcome, and a schedule that names a task that is not
// enabled stops the replay. A failing seed prints the program, both sets
// and the command that runs the seed alone.
func TestDPORFindsEveryOutcome(t *testing.T) {
	for seed := uint64(1); seed <= 300; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		p := genProg(r, 4)
		x := vexec{p}
		if all, _ := naive(x, -1, naiveLimit); all == nil {
			continue // too many schedules for the reference
		}
		for _, bound := range []int{-1, 0, 1, 2} {
			want, naiveRuns := naive(x, bound, naiveLimit)
			got := map[string]bool{}
			var runs []Run
			rep := DFS(x, Options{Preempt: bound, All: true, Visit: func(run Run) {
				got[run.Outcome.Value] = true
				runs = append(runs, run)
			}})
			fail := func(format string, args ...any) {
				t.Helper()
				t.Fatalf("seed %d, bound %d: %s\nprogram: %s\nnaive (%d schedules): %q\ndfs (%d schedules): %q\nreplay: go test ./runtime/explore/sched -run TestDPORFindsEveryOutcome (seed %d)",
					seed, bound, fmt.Sprintf(format, args...), p, naiveRuns, keys(want), rep.Schedules, keys(got), seed)
			}
			if rep.Failure != nil && rep.Failure.Diverged != "" {
				fail("the search diverged: %s", rep.Failure.Diverged)
			}
			if !rep.Proved {
				fail("the search did not end")
			}
			if !slices.Equal(keys(got), keys(want)) {
				fail("the outcome sets differ")
			}
			if rep.Schedules > naiveRuns {
				fail("dfs ran %d schedules, more than the naive %d", rep.Schedules, naiveRuns)
			}
			if bound >= 0 {
				continue
			}
			for _, run := range runs {
				sched, err := ParseToken(Token(run.Schedule()))
				if err != nil {
					fail("token %q: %v", Token(run.Schedule()), err)
				}
				again := Once(x, Replay{Schedule: sched})
				if again.Outcome.Stopped != "" || again.Outcome.Value != run.Outcome.Value {
					fail("the schedule %s replays to %q (stopped %q), not %q", Token(run.Schedule()), again.Outcome.Value, again.Outcome.Stopped, run.Outcome.Value)
				}
				if len(sched) == 0 {
					continue
				}
				bad := slices.Clone(sched)
				bad[r.IntN(len(bad))] = "0.9"
				if off := Once(x, Replay{Schedule: bad}); off.Outcome.Stopped == "" {
					fail("the schedule %s, which names a task that never runs, replayed to %q", Token(bad), off.Outcome.Value)
				}
			}
		}
	}
}

// refPCT is PCT written from its definition, as a pure function of the
// seed, n, k, d and the run's choices: the priorities d+1..d+n are a
// random permutation, given to tasks in the order they are first enabled
// (later tasks above them); d-1 distinct steps of 0..k-1 are change
// points, where the task at the point drops to d-i at the i-th; the
// enabled task with the highest priority (the first of equals) runs.
func refPCT(seed uint64, n, k, d int, choices []Choice) []int {
	r := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	perm := r.Perm(n)
	steps := r.Perm(k)
	drop := map[int]int{}
	for i := 1; i < d && i-1 < len(steps); i++ {
		drop[steps[i-1]] = i
	}
	prio := map[string]int{}
	var picks []int
	for _, c := range choices {
		for _, s := range c.Enabled {
			if _, ok := prio[s.Task]; !ok {
				if len(prio) < n {
					prio[s.Task] = d + 1 + perm[len(prio)]
				} else {
					prio[s.Task] = d + 1 + len(prio)
				}
			}
		}
		if i, ok := drop[c.Step]; ok && c.Current >= 0 {
			prio[c.Enabled[c.Current].Task] = d - i
		}
		best := 0
		for j := range c.Enabled {
			if prio[c.Enabled[j].Task] > prio[c.Enabled[best].Task] {
				best = j
			}
		}
		picks = append(picks, best)
	}
	return picks
}

// TestPCTMatchesReference: on generated programs, with n and k from a
// first cooperative run, PCT's picks equal refPCT's for depths 1 to 3.
// No test counts how often a random mode finds a failure.
func TestPCTMatchesReference(t *testing.T) {
	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, 11))
		x := vexec{genProg(r, 3)}
		first := Once(x, Cooperative{})
		tasks := map[string]bool{}
		for _, c := range first.Choices {
			for _, s := range c.Enabled {
				tasks[s.Task] = true
			}
		}
		n, k := len(tasks), len(first.Picks)
		for d := 1; d <= 3; d++ {
			run := Once(x, NewPCT(seed, n, k, d))
			if want := refPCT(seed, n, k, d, run.Choices); !slices.Equal(run.Picks, want) {
				t.Fatalf("seed %d, n %d, k %d, d %d: pct picked %v, the reference %v\nprogram: %s", seed, n, k, d, run.Picks, want, x.p)
			}
		}
	}
}

// TestDependencyTableTotal keeps the dependency table equal to the
// runtime's operations: every rt.OpKind has a row, and every row is an
// rt.OpKind. A missing row would make its steps dependent with all others
// by default, which is sound but hides a table that fell behind.
func TestDependencyTableTotal(t *testing.T) {
	words := map[string]bool{}
	for k := rt.OpKind(1); k.String() != "op?"; k++ {
		words[k.String()] = true
		if kinds[k.String()] == 0 {
			t.Errorf("rt's operation %q has no row in the dependency table", k)
		}
	}
	for w := range kinds {
		if !words[w] {
			t.Errorf("the dependency table has a row %q that is no rt operation", w)
		}
	}
}
