package lower

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// Stage D3b round 2 (check-v1): a hidden task is bound where the blocking
// happens, never where a value is made (spec.md, "Orchestrator answers,
// stage D3b round 1 review"): a Seq from queue.Values waits on the task
// that iterates it (review M1).

// runWithin lowers src, builds it and runs the program until the test's
// deadline (testwait.End), so a hang or a deadlock fails the test with its
// output instead of stopping the test binary.
//
// It builds Go because its callers ask what the built program prints and how
// it exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func runWithin(t *testing.T, name, src, want string) {
	t.Helper()
	o := mustLower(t, name, src)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		return // buildAll reported the Go diagnostics
	}
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	run := exec.CommandContext(ctx, exes[name])
	var combined bytes.Buffer
	run.Stdout, run.Stderr = &combined, &combined
	err := runProgram(t, run)
	out := combined.Bytes()
	if ctx.Err() != nil {
		t.Fatalf("the program did not end by the test's deadline (a hang); output so far:\n%s", out)
	}
	if err != nil || string(out) != want {
		t.Fatalf("run: %v\n%s\nwant:\n%s\nGo:\n%s", err, out, want, stripDirectives(o.mainGo()))
	}
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
	let close = q.CloseSend
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
	needsBuild(t)
	runWithin(t, "methodvalues", methodValueProgram, "3 4 25\nmake 1\nmake 2\nstopped 1\n")
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
	needsBuild(t)
	o := mustLower(t, "shapes", noValueShapesProgram)
	if goSrc := stripDirectives(o.mainGo()); !strings.Contains(goSrc, "Job[struct{}]") || !strings.Contains(goSrc, "runAll[struct{}](") {
		t.Errorf("the no-value type argument is not struct{}:\n%s", goSrc)
	}
	runWithin(t, "shapes", noValueShapesProgram, "112 2 3\n")
}
