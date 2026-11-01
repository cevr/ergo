package lower

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Stage D3b round 2 (check-v1): a hidden task is bound where the blocking
// happens, never where a value is made (spec.md, "Orchestrator answers,
// stage D3b round 1 review"): a Seq from queue.Values waits on the task
// that iterates it (review M1).

// runWithin lowers src, builds it and runs the program with a time limit,
// so a hang or a deadlock fails the test with its output instead of
// stopping the test binary.
func runWithin(t *testing.T, name, src, want string, limit time.Duration) {
	t.Helper()
	o := mustLower(t, name, src)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		return // buildAll reported the Go diagnostics
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	out, err := exec.CommandContext(ctx, exes[name]).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the program did not end within %v (a hang); output so far:\n%s", limit, out)
	}
	if err != nil || string(out) != want {
		t.Fatalf("run: %v\n%s\nwant:\n%s\nGo:\n%s", err, out, want, stripDirectives(o.mainGo()))
	}
}

// capturedSeqProgram makes a Seq in main and iterates it in a task.Go
// body, which a stop then reaches while it waits on the empty queue.
const capturedSeqProgram = `import queue
import task

func drain(s Seq[int]) int {
	let mut n = 0
	for v in s {
		n += v
	}
	n
}

func main() needs Console {
	let q = queue.New[int](1)
	let s = q.Values()
	let t = task.Go() { drain(s) }
	t.Stop()
	let u = task.Go() {
		let mut n = 0
		for v in s {
			n += v
		}
		n
	}
	u.Stop()
	Console.Println("stopped")
}
`

// TestErgoRunCapturedSeqStops: a Seq made by queue.Values in one task and
// iterated in another waits on the iterating task, so a stop of that task
// reaches the wait and the program ends (review M1; before the repair the
// wait took main's task and the program deadlocked). The Seq calls Values
// inside itself with the environment it is iterated on.
func TestErgoRunCapturedSeqStops(t *testing.T) {
	o := mustLower(t, "seq", capturedSeqProgram)
	if goSrc := stripDirectives(o.mainGo()); !strings.Contains(goSrc, ".Values(e.task)") {
		t.Errorf("the Seq does not call Values with the iterating environment's task:\n%s", goSrc)
	}
	runWithin(t, "seqstop", capturedSeqProgram, "stopped\n", 60*time.Second)
}

// TestErgoRunSeqReceiverEvaluatedOnce: the receiver of Values is evaluated
// once, where the Seq is made: a call that makes it runs once, and a later
// assignment to the variable it came from does not change the queue the
// Seq reads.
func TestErgoRunSeqReceiverEvaluatedOnce(t *testing.T) {
	runWithin(t, "seqonce", `import queue

func mk(n int) queue.Queue[int] fails queue.Closed needs Console {
	Console.Println("make \(n)")
	let q = queue.New[int](4)
	try q.Push(n)
	q.Close()
	q
}

func main() fails queue.Closed needs Console {
	let mut q = try mk(1)
	let s = q.Values()
	let r = try mk(2).Values()
	q = try mk(3)
	for v in s {
		Console.Println("s \(v)")
	}
	for v in r {
		Console.Println("r \(v)")
	}
	for v in q.Values() {
		Console.Println("q \(v)")
	}
}
`, "make 1\nmake 2\nmake 3\ns 1\nr 2\nq 3\n", 60*time.Second)
}
