//go:build ergoexplore

package v1

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"ergo.local/prototype/runtime/explore/sched"
	"ergo.local/prototype/runtime/rt"
)

// doneProgram is std actor's DoneIsFinal law on the real runner: a tally
// whose signal 5 is answered Done. Once Ask has the Done, a Send must fail
// ActorStopped (rule 8), and the state stays the one the Done saw.
func doneProgram(t *rt.Task) error {
	step := func(_ *rt.Task, s int, e int) (xrdec, error) {
		if e == 5 {
			return xrdec{k: decideDone, s: rstate{got: s}}, nil
		}
		return xrdec{k: decideStay, s: rstate{got: s + 1}}, nil
	}
	a := MachineStart(Site("tally"), t, noSleep{}, nil, 0, step, nil,
		func(int) time.Duration { return noTimer }, func(s int) int { return s }, func(int) bool { return false },
		func(*rt.Task, int, func(int)) {}, func(d xrdec) int { return d.k },
		func(s int, d xrdec) int {
			if d.k == decideStay {
				return d.s.got
			}
			return s
		}, func(*rt.Task, xrdec) {}, 1)
	if err := a.Send(t, 1); err != nil {
		return err
	}
	d, err := a.Ask(t, 5)
	if err != nil {
		return err
	}
	if d.k != decideDone || d.s.got != 1 {
		return fmt.Errorf("the Ask was answered %+v, want Done of 1", d)
	}
	if err := a.Send(t, 1); !errors.Is(err, ActorStopped{}) {
		return fmt.Errorf("a Send after an Ask answered Done returned %v, want ActorStopped", err)
	}
	if s := a.State(t); s != 1 {
		return fmt.Errorf("the state after Done is %d, want 1", s)
	}
	return nil
}

// TestDoneIsFinalExplored searches doneProgram's schedules with at most
// two preemptions and fails at the first that breaks DoneIsFinal, with the
// schedule as a token. It is the order behind the law's flake under load
// (FK1, TestStdActorLawsRun/actor.DoneIsFinal, case 36 of seed 1): the
// runner answered the Ask before it closed admission, so a Send in
// between was admitted, and the signal was then dropped unstepped. It
// runs with the ergoexplore tag (the gate's explored worker step), on the
// real runner, whose own order is the subject.
func TestDoneIsFinalExplored(t *testing.T) {
	pool := &sched.Pool{Start: func() (sched.Conn, error) { return startPipeWorker(doneProgram) }}
	defer pool.Close()
	for _, b := range []int{0, 1, 2} {
		r := sched.DFS(pool, sched.Options{Preempt: b, Schedules: 2_000})
		t.Logf("bound %d: %d schedules, proved %v", b, r.Schedules, r.Proved)
		if r.Failure != nil {
			t.Fatalf("schedule %d: %s\nschedule %s", r.Schedules, r.Failure.Outcome.Failure, sched.Token(r.Failure.Schedule()))
		}
	}
}
