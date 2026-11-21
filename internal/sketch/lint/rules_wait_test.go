package lint_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// The parts of the test-waits-on-time generator: how a law lets time pass
// in its own frame, and what else it runs that reads or waits on time.
var (
	clockKinds = []string{"time.NewManual(0)", "testing.NewVirtualClock(0)"}
	// a wait in the law's own frame, and whether it lets time pass
	ownWaits = []struct {
		text   string
		passes bool
	}{
		{"", false},
		{"m.Advance(10ms)", true},
		{"m.Advance(0s)", false},
		{"Scheduler.Sleep(10ms)", true},
		{"m.Sleep(10ms)", true},
	}
	// other code the law runs, and whether it reads or waits on time
	timedUses = []struct {
		text  string
		timed bool
	}{
		{"", false},
		{"let _ = m.Now()", true},
		{"tick()", true},
		{"let _ = q.Len()", false},
		{"let _ = held(m)", true},
		{"stamp()", true},
		{"let _ = m.Sleepers()", true},
	}
)

// TestTestWaitsOnTimeAgreesWithReference writes laws that start a task,
// let time pass in their own frame or not, and run other time-driven code
// or not, on std time's Manual or testing's VirtualClock, and checks that
// test-waits-on-time fires on exactly the waits that pass time (an
// advance by more than zero, a sleep in the law's own frame) in laws that
// run nothing else that reads or waits on time: a clock read, a function
// whose row needs the Scheduler or std time's Clock (an operation type
// the clock implements), a clock method that gives a result, a sleep in
// the task, the clock passed to other code. Mutants:
// lhw-zero-advance-waits, lhw-task-sleep-waits, lhw-row-unread,
// lhw-implemented-caps-unread, lhw-result-method-advances,
// lhw-clock-sleep-unread, lhw-shared-clock-ignored.
func TestTestWaitsOnTimeAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xf1))
		p := newPlanted()
		p.write("import queue\nimport task\nimport testing\nimport time\n\nfunc work(q queue.Queue[int]) {\n\tq.Push(1) catch {\n\tcase queue.Closed:\n\t}\n}\n\nfunc tick() needs Scheduler {\n\tScheduler.Sleep(1ms)\n}\n\nfunc stamp() needs time.Clock {\n\tlet _ = time.Clock.Now()\n}\n\nfunc held[C](c C) int { 0 }\n\nfunc virtual() testing.VirtualClock { testing.NewVirtualClock(0) }", false, 0)
		for i := range 1 + r.IntN(4) {
			clock := r.IntN(len(clockKinds))
			wait := ownWaits[r.IntN(len(ownWaits))]
			for clock == 1 && wait.text != "" && wait.text[2] == 'A' {
				// a VirtualClock has no Advance: its time moves when every task waits
				wait = ownWaits[r.IntN(len(ownWaits))]
			}
			use := timedUses[r.IntN(len(timedUses))]
			for clock == 1 && use.text == "let _ = m.Sleepers()" {
				// a VirtualClock counts no sleepers
				use = timedUses[r.IntN(len(timedUses))]
			}
			taskSleeps := r.IntN(3) == 0
			body := "work(q)"
			if taskSleeps {
				body = "Scheduler.Sleep(5ms)\n\t\t\twork(q)"
			}
			p.write(fmt.Sprintf("\nlaw Settles%d() {\n\tlet m = %s\n\tlet q = queue.New[int](4)\n\twith context { Scheduler: m, time.Clock: m } {\n\t\tlet t = task.Go() {\n\t\t\t%s\n\t\t}", i, clockKinds[clock], body), false, 0)
			if wait.text != "" {
				p.write("\t\t"+wait.text, wait.passes && !use.timed && !taskSleeps, 0)
			}
			if use.text != "" {
				p.write("\t\t"+use.text, false, 0)
			}
			p.write("\t\tt.Wait()\n\t\tq.Len() == 1\n\t}\n}", false, 0)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "test-waits-on-time", "TestTestWaitsOnTimeAgreesWithReference", seed)
	}
}
