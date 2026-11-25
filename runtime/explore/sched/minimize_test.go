package sched

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// failingExec wraps an Executor and fails whenever a run's observable value
// equals badValue, letting TestMinimizedScheduleStillFails synthesize failing
// schedules from generated concurrent programs on vexec.
type failingExec struct {
	inner    Executor
	badValue string
}

func (f failingExec) Run(p Picker) Outcome {
	o := f.inner.Run(p)
	if o.Failure == "" && o.Stopped == "" && o.Value == f.badValue {
		o.Failure = "bad state reached"
	}
	return o
}

// twoPreemptBugExec models a deterministic concurrent program that fails ONLY
// when two specific preemptions occur (task 0 preempted to 0.1 before 0's write,
// and 0.1 preempted to 0.2 before 0.1's write), while allowing extra noisy
// preemptions before and after. Dropping either required preemption causes the
// run to pass, directly killing mc-min-drops-needed-pick.
type twoPreemptBugExec struct{}

func (twoPreemptBugExec) Run(pk Picker) Outcome {
	type task struct {
		path string
		pc   int
		done bool
	}
	tasks := []*task{
		{path: "0"},
		{path: "0.0"},
		{path: "0.1"},
		{path: "0.2"},
	}
	cur := tasks[0]
	var trace []string
	for step := 0; ; step++ {
		c := Choice{Current: -1, Step: step}
		var cands []*task
		for _, t := range tasks {
			if t.done {
				continue
			}
			if t == cur {
				c.Current = len(cands)
			}
			cands = append(cands, t)
			c.Enabled = append(c.Enabled, Step{Task: t.path, Kind: "write", Handle: 1})
		}
		if len(cands) == 0 {
			break
		}
		k, err := pk.Pick(c)
		if err != nil {
			return Outcome{Stopped: err.Error(), Abandoned: true}
		}
		cur = cands[k]
		trace = append(trace, fmt.Sprintf("%s:%d", cur.path, cur.pc))
		cur.pc++
		if cur.pc >= 3 {
			cur.done = true
		}
	}
	// Failure occurs iff 0.1:0 runs between 0:0 and 0:1 AND 0.2:0 runs between 0.1:0 and 0.1:1.
	pos := func(ev string) int {
		for i, s := range trace {
			if s == ev {
				return i
			}
		}
		return -1
	}
	p00, p01 := pos("0:0"), pos("0:1")
	p10, p11 := pos("0.1:0"), pos("0.1:1")
	p20 := pos("0.2:0")
	if p00 < p10 && p10 < p01 && p10 < p20 && p20 < p11 {
		return Outcome{Failure: "two-preemption interleaving bug", Value: "bug"}
	}
	return Outcome{Value: "ok"}
}

// TestMinimizedScheduleStillFails: on generated failing concurrent programs
// (found with non-minimal random/PCT schedules) and on a deterministic
// two-preemption bug with noisy extra preemptions, Minimize returns a schedule
// that replays cleanly (including through Token/ParseToken) to the exact same
// failure with no more preemptions than the original, and strictly reduces
// preemptions on non-minimal schedules.
func TestMinimizedScheduleStillFails(t *testing.T) {
	// Deterministic two-preemption bug with 5 preemptions in the initial schedule.
	t.Run("two-preemption-bug", func(t *testing.T) {
		x := twoPreemptBugExec{}
		noisy := []string{
			"0", "0.0", "0.1", "0.2", "0.0", "0.0",
			"0.2", "0.2", "0.1", "0.1", "0", "0",
		}
		orig := Once(x, Replay{Schedule: noisy})
		if orig.Outcome.Failure != "two-preemption interleaving bug" {
			t.Fatalf("noisy schedule did not hit bug: %+v", orig.Outcome)
		}
		if Preemptions(orig) <= 2 {
			t.Fatalf("expected >2 preemptions in noisy schedule, got %d", Preemptions(orig))
		}
		minRun := Minimize(x, orig)
		if !sameFailure(minRun, orig) {
			t.Fatalf("minimized schedule did not reproduce failure: %+v", minRun.Outcome)
		}
		if Preemptions(minRun) > Preemptions(orig) {
			t.Fatalf("minimized preemptions %d > original %d", Preemptions(minRun), Preemptions(orig))
		}
		if Preemptions(minRun) >= Preemptions(orig) {
			t.Fatalf("expected minimization to reduce preemptions from %d, got %d", Preemptions(orig), Preemptions(minRun))
		}
		tok := Token(minRun.Schedule())
		parsed, err := ParseToken(tok)
		if err != nil {
			t.Fatalf("ParseToken(%q): %v", tok, err)
		}
		replayed := Once(x, Replay{Schedule: parsed})
		if !sameFailure(replayed, orig) {
			t.Fatalf("token replay failed to reproduce %q: %+v", orig.Outcome.Failure, replayed.Outcome)
		}
	})

	// Generated failing programs on vexec.
	checked, reduced := 0, 0
	for seed := uint64(1); seed <= 250 && checked < 60; seed++ {
		r := rand.New(rand.NewPCG(seed, 31))
		p := genProg(r, 4)
		vx := vexec{p}
		coop := Once(vx, Cooperative{})
		var found Run
		var badVal string
		for i := 0; i < 20; i++ {
			cand := Once(vx, NewRandom(seed*100+uint64(i)+1))
			if cand.Outcome.Stopped != "" || cand.Diverged != "" {
				continue
			}
			if cand.Outcome.Value != coop.Outcome.Value && Preemptions(cand) >= 2 {
				badVal = cand.Outcome.Value
				found = cand
				break
			}
		}
		if badVal == "" {
			continue
		}
		fx := failingExec{inner: vx, badValue: badVal}
		orig := Once(fx, Replay{Schedule: found.Schedule()})
		if !orig.Failed() {
			t.Fatalf("seed %d: synthetic failing schedule did not fail on replay", seed)
		}
		minRun := Minimize(fx, orig)
		if !sameFailure(minRun, orig) {
			t.Fatalf("seed %d: minimized schedule lost failure %q: got %+v\nprogram: %s", seed, orig.Outcome.Failure, minRun.Outcome, p)
		}
		if Preemptions(minRun) > Preemptions(orig) {
			t.Fatalf("seed %d: minimized preemptions %d > original %d\nprogram: %s", seed, Preemptions(minRun), Preemptions(orig), p)
		}
		tok := Token(minRun.Schedule())
		parsed, err := ParseToken(tok)
		if err != nil {
			t.Fatalf("seed %d: ParseToken(%q): %v", seed, tok, err)
		}
		replayed := Once(fx, Replay{Schedule: parsed})
		if !sameFailure(replayed, orig) {
			t.Fatalf("seed %d: token %q did not replay to %q: %+v", seed, tok, orig.Outcome.Failure, replayed.Outcome)
		}
		checked++
		if Preemptions(minRun) < Preemptions(orig) {
			reduced++
		}
	}
	if checked < 20 {
		t.Fatalf("only checked %d generated failing schedules, want >= 20", checked)
	}
	if reduced == 0 {
		t.Fatalf("minimization reduced preemptions in 0 of %d non-minimal failing schedules", checked)
	}
	t.Logf("minimized %d generated failing schedules: %d reduced preemptions", checked, reduced)
}
