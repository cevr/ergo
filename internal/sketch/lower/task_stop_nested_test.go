package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stopNestedSource is STOP-NESTED-WAIT's property (runtime.md Tasks, rules
// 1, 4 and 11): a task b waits on a Task that another task started and
// handed to it, by a return, in a field or through a queue; the handed
// Task heads a chain of 1 to 4 tasks whose innermost waits on a Task, a
// queue, a manual clock's sleep or a semaphore, and nothing ever ends
// that wait. Stopping b returns, and every exit action the chain
// registered ran. Before the fix b's Wait parked on the starter's word,
// so the stop woke nobody (issue 236).
const stopNestedSource = `import queue
import scope
import sync
import task
import time

// env is what the chain shares: the gate nothing opens, the semaphore
// the test holds, and the counts of exit actions registered and run.
type env { gate queue.Queue[int], sem sync.Semaphore, reg queue.Queue[int], ran queue.Queue[int], ended queue.Queue[int], up queue.Queue[int] }

type held { t Task[int, queue.Closed] }

func newEnv() env {
	env{gate: queue.New[int](0), sem: sync.NewSemaphore(1), reg: queue.New[int](64), ran: queue.New[int](64), ended: queue.New[int](64), up: queue.New[int](1)}
}

// mark counts one in q. Offer is no interruption point, so a stop cannot
// fall between an exit action's registration and its count.
func mark(q queue.Queue[int]) {
	q.Offer(1) catch {
	case _:
		Unreachable("stop: a count queue has room and is never ended")
	}
}

// leaf waits, by kind k, on something nothing ends.
func leaf(k int, e env) int fails queue.Closed needs Scheduler {
	if k == 0 {
		let t = task.Go() {
			scope.OnExit() { mark(e.ran) }
			mark(e.reg)
			try e.gate.Pop()
		}
		return try t.Wait()
	}
	if k == 1 {
		return try e.gate.Pop()
	}
	if k == 2 {
		Scheduler.Sleep(1h)
		return 0
	}
	sync.With(e.sem) { 0 }
}

// chain is a Task heading n tasks, each waiting on the next, the last on
// leaf(k). Each counts its end in ended too.
func chain(n int, k int, e env) Task[int, queue.Closed] needs Scheduler {
	task.Go() {
		scope.OnExit() {
			mark(e.ran)
			mark(e.ended)
		}
		mark(e.reg)
		if n <= 1 {
			mark(e.up)
			try leaf(k, e)
		} else {
			let next = chain(n - 1, k, e)
			try next.Wait()
		}
	}
}

// handed is chain(n, k, e) started on another task and handed to the
// caller: by the task's return (mv 0), in a field of its value (1) or
// through a queue (2).
func handed(mv int, n int, k int, e env) Task[int, queue.Closed] fails queue.Closed needs Scheduler {
	if mv == 0 {
		let outer = task.Go() { chain(n, k, e) }
		return outer.Wait()
	}
	if mv == 1 {
		let outer = task.Go() { held{t: chain(n, k, e)} }
		match outer.Wait() {
		case held{t: t}:
			return t
		}
	}
	let hand = queue.New[Task[int, queue.Closed]](1)
	let outer = task.Go() { try hand.Push(chain(n, k, e)) }
	try outer.Wait()
	try hand.Pop()
}

func span(x int, n int) int { ((x % n) + n) % n }

// holding is what b does with the handed Task, by how: Wait on it (0), Stop
// it and wait on the gate (1), or wait on the gate and leave it to b's
// frame (2); or (3) wait on the gate with an exit action, registered after
// the Task, that waits until the chain counted an end: exit actions run
// masked and before the Task's release, so only rule 1's stop of the tree
// below b ends it.
func holding(how int, inner own Task[int, queue.Closed], e env, ready queue.Queue[int]) int fails queue.Closed {
	if how == 0 {
		try ready.Push(1)
		return try inner.Wait()
	}
	if how == 1 {
		inner.Stop()
		try ready.Push(1)
		return try e.gate.Pop()
	}
	if how == 3 {
		let _ = try e.up.Pop()
		scope.OnExit() {
			let _ = e.ended.Pop() catch {
			case _:
				0
			}
		}
	}
	try ready.Push(1)
	try e.gate.Pop()
}

test StopReachesNestedWaits(depth int, kind int, move int, how int) {
	let e = newEnv()
	let ready = queue.New[int](1)
	with context { Scheduler: time.NewManual(0) } {
		try sync.With(e.sem) {
			let b = task.Go() {
				let inner = try handed(span(move, 3), 1 + span(depth, 4), span(kind, 4), e)
				try holding(span(how, 4), inner, e, ready)
			}
			try ready.Pop()
			b.Stop()
		}
	}
	assert e.ran.Len() == e.reg.Len()
}
`

// stopNestedRepro is issue 236's program: b waits on a Task that outer
// started and returned, parked on a queue nothing pushes to; main stops b.
const stopNestedRepro = `import queue
import task

func parked(gate queue.Queue[int]) int fails queue.Closed {
	try gate.Pop()
}

func nest(gate queue.Queue[int]) Task[int, queue.Closed] {
	task.Go() { parked(gate) }
}

func main() fails queue.Closed needs Console | Ambient {
	with context { Scheduler: NewLiveScheduler() } {
		let ready = queue.New[int](0)
		let gate = queue.New[int](0)
		let b = task.Go() {
			let outer = task.Go() { nest(gate) }
			let inner = outer.Wait()
			try ready.Push(1)
			try inner.Wait()
			Console.Println("BAD: Wait completed")
		}
		try ready.Pop()
		b.Stop()
		Console.Println("stopped")
	}
}
`

// TestStopReachesNestedWaits: issue 236's program prints "stopped" and
// exits 0, and the property stopNestedSource holds built (seeded, ergo
// test), under the explorer (ergo test --explore) and in the evaluator.
// Mutants: stop-waiter-is-starter (Wait parks the task that started the
// Task: issue 236), stop-stopper-is-starter, stop-closer-is-starter (Stop
// and the frame's release join on the starter), stop-detach-drops-live-
// child (a task that ends drops the child it handed out from the tree:
// how 3's exit action waits forever), stop-eval-finish-drops-kids (the
// same in the evaluator). Its evaluator leg found EVAL-RETURNED-TEMPS
// (the Task `return outer.Wait()` gives was released at the frame's end),
// which this unit stacks on.
//
// It builds Go because the subject is a stop crossing goroutines parked in
// rt's waits; go/types cannot execute a wait.
func TestStopReachesNestedWaits(t *testing.T) {
	needsBuild(t)
	o := lowerSource("stopnested", stopNestedRepro, true)
	if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
		t.Fatalf("repro lowering: err %v, panic %v, diags %v, refused %v", o.err, o.panicked, o.diags, o.refused)
	}
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("repro: no build")
	}
	out, stderr, code := runExe(t, exes[o.name])
	if code != 0 || out != "stopped\n" {
		t.Errorf("repro: exit %d, out %q, want exit 0 and \"stopped\"\n%s", code, out, firstLines(stderr, 12))
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stop.ergo"), []byte(stopNestedSource), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"test", "--seed", "1", dir},
		{"test", "--run", "StopReachesNestedWaits", "--explore", "random", "--schedules", "40", dir},
	} {
		cmd := exec.Command(ergoCLI(t), args...)
		cmd.Dir = t.TempDir()
		got, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(got), "ok StopReachesNestedWaits") {
			t.Errorf("ergo %s: %v\n%s", strings.Join(args, " "), err, firstLines(string(got), 30))
		}
	}
	file, err := syntax.Parse(stopNestedSource)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v", diags)
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	for _, res := range rep.Results {
		ran = ran || res.Name == "StopReachesNestedWaits"
		if res.Status != eval.Passed {
			t.Errorf("eval %s: %s %v", res.Name, res.Status, res)
		}
	}
	if !ran {
		t.Error("eval ran no StopReachesNestedWaits")
	}
}
