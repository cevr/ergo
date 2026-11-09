package lower

import (
	"bytes"
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
	run := exec.CommandContext(ctx, exes[name])
	var combined bytes.Buffer
	run.Stdout, run.Stderr = &combined, &combined
	err := runProgram(t, run)
	out := combined.Bytes()
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

// methodValueProgram uses each queue operation and an operation of the
// program's own operation type as a value; a method value made in main is
// called in a task.Go body, which a stop reaches while it waits; and a
// method value's receiver is evaluated once, where the value is made.
const methodValueProgram = `import queue
import task

type Shape { Area() int }

type Sq { N int }

func Area(s Sq) int { s.N * s.N }

func mk(n int) queue.Queue[int] fails queue.Closed needs Console {
	Console.Println("make \(n)")
	let q = queue.New[int](4)
	try q.Push(n)
	q
}

func call(f func() int fails queue.Closed) int fails queue.Closed {
	try f()
}

func main() fails queue.Closed needs Console {
	let q = queue.New[int](4)
	let push = q.Push
	let pop = q.Pop
	let close = q.Close
	let values = q.Values
	try push(3)
	try push(4)
	let a = try pop()
	close()
	let mut n = 0
	for v in values() {
		n += v
	}
	let sh Shape = Sq{N: 5}
	let area = sh.Area
	Console.Println("\(a) \(n) \(area())")
	let empty = queue.New[int](1)
	let wait = empty.Pop
	let t = task.Go() { call(wait) }
	t.Stop()
	let mut r = try mk(1)
	let first = r.Pop
	r = try mk(2)
	let got = try first()
	Console.Println("stopped \(got)")
}
`

// TestErgoRunMethodValues: a method value of a std operation (Push, Pop,
// Close, Values) and of the program's own operation type lowers to a
// function value of the uniform shape; it evaluates its receiver once and
// takes the task of the environment it is called with, so a stop reaches a
// task that waits through a method value made by main (review M3; before
// the repair each was an internal compiler error).
func TestErgoRunMethodValues(t *testing.T) {
	runWithin(t, "methodvalues", methodValueProgram, "3 4 25\nmake 1\nmake 2\nstopped 1\n", 60*time.Second)
}

// noValueProgram binds a type parameter to no value with a block that has
// none: task.Go's T (a Task whose Wait gives no value, with an empty and
// with a written error row), and a declared generic function's T, through a
// trailing block, a function literal, a declared function and a Task
// argument.
const noValueProgram = `import queue
import task

func run[T](f func() T) T {
	f()
}

func twice[T](f func() T) T {
	f()
	f()
}

func spawn[T, E](f escaping func() T fails E) Task[T, E] {
	task.Go() { try f() }
}

func waitIt[T, E](t Task[T, E]) T fails E {
	try t.Wait()
}

func bump() {
}

func main() fails queue.Closed needs Console {
	let mut x = 0
	let t = task.Go() { x += 100 }
	t.Wait()
	let q = queue.New[int](1)
	let u = task.Go() { try q.Push(1) }
	try u.Wait()
	run() { x += 1 }
	twice() { x += 10 }
	run(func() { x += 1000 })
	run(bump)
	let s = spawn() { bump() }
	waitIt(s)
	let n = run() { 3 }
	let v = spawn(func() int { 7 })
	let m = waitIt(v)
	let got = try q.Pop()
	Console.Println("\(x) \(n) \(m) \(got)")
}
`

// TestErgoRunNoValueTypeArgument: a type argument that a block with no
// value binds lowers to struct{} (types.md, Inference): task.Go() { work() }
// runs and Wait's struct{} is discarded, and a declared generic function's
// func() T parameter takes the block through a wrapper that returns
// struct{}{} (stage D3b round 1 review M2; before the repair task.Go was
// refused with a message that named no owner).
func TestErgoRunNoValueTypeArgument(t *testing.T) {
	o := mustLower(t, "novalue", noValueProgram)
	if goSrc := stripDirectives(o.mainGo()); !strings.Contains(goSrc, "ergo.Go[struct{}]") || !strings.Contains(goSrc, "run[struct{}]") {
		t.Errorf("the no-value type argument is not struct{}:\n%s", goSrc)
	}
	runWithin(t, "novalue", noValueProgram, "1121 3 7 1\n", 60*time.Second)
}

// noValueShapesProgram binds a type parameter to no value where the checked
// type and Go's instance used to differ (C11): a declared generic type's
// field, a slice of function values (read through the same slice after an
// element is replaced, so no copy stands in for it), a function result and
// a parameter's parameter. A function value with no result now has the one
// Go shape of func(…) struct{} (typeCtx.funcType).
const noValueShapesProgram = `type Job[T] { Run func() T }

func all[T](fs []func() T) int { len(fs) }

func runAll[T](fs []func() T) {
	for f in fs {
		f()
	}
}

func later[T](f escaping func() T) func() T { f }

func outer[T](f func(g func() T) int) int { 3 }

func main() needs Console {
	let mut hits = 0
	let j = Job{Run: func() { hits = hits + 1 }}
	j.Run()
	j.Run()
	let mut fs = []func(){func() {}, func() {}}
	fs[1] = func() { hits = hits + 100 }
	runAll(fs)
	let n = all(fs)
	let g = later() { hits = hits + 10 }
	g()
	let k = outer(func(h func()) int {
		h()
		4
	})
	Console.Println("\(hits) \(n) \(k)")
}
`

// TestNoValueTypeArgumentsLower (C11, lower-v1 stage 4): each shape of
// noValueShapesProgram lowers with no adapter and runs: the Job's Run, the
// replaced slice element, the later result and the parameter's parameter.
func TestNoValueTypeArgumentsLower(t *testing.T) {
	o := mustLower(t, "shapes", noValueShapesProgram)
	if goSrc := stripDirectives(o.mainGo()); !strings.Contains(goSrc, "Job[struct{}]") || !strings.Contains(goSrc, "runAll[struct{}](") {
		t.Errorf("the no-value type argument is not struct{}:\n%s", goSrc)
	}
	runWithin(t, "shapes", noValueShapesProgram, "112 2 3\n", 60*time.Second)
}

// TestErgoRunTaskBodyBoundAtGo: a function value passed to task.Go is
// evaluated where task.Go is called; a later assignment to its variable
// does not change what the task runs (the common rule's second half, found
// by the round 2 audit: before the repair the task read the variable when
// it ran and printed "2 2").
func TestErgoRunTaskBodyBoundAtGo(t *testing.T) {
	runWithin(t, "gobound", `import task

func one() int { 1 }

func two() int { 2 }

func main() needs Console {
	let mut f = one
	let t = task.Go(f)
	f = two
	let n = t.Wait()
	Console.Println("\(n) \(f())")
}
`, "1 2\n", 60*time.Second)
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
