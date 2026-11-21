package lower

import (
	"fmt"
	"strings"
	"testing"
)

// envMemoSource is a durable actor whose every signal commits: each
// signal runs two host callbacks of the runtime, the actor's step on the
// actor's task (launch's step adapter, a task other than the maker's) and
// the save on the step's own task (ActorKeep).
const envMemoSource = `import actor
import time

func step(s int, e int) Decision[int, int] { Stay[int]{Next: s + e} }

func untimed(s int) Option[Timeout[int]] { None }

func unworked(s int) Option[func(send func(e int))] { None }

type Bad { n int }

func Pump(n int) int fails Bad needs Scheduler {
	let a = try actor.StartDurable(Machine[int, int, int]{Initial: 0, Step: step, After: untimed, Work: unworked, Exit: func(s int) Option[func()] { None }}, 1, func(o int) {}, func() Option[int] fails Bad { None }, func(p int, q int, e int) fails Bad {})
	for _ in 0..n {
		a.Send(1) catch {
		case actor.Stopped:
		}
	}
	let s = actor.WaitFor(a, func(s int) bool { s >= n }) catch {
	case actor.Stopped:
		-1
	}
	a.Stop()
	s
}

func Pumps(n int) int {
	with context { Scheduler: time.NewManual(0) } {
		Pump(n) catch {
		case Bad:
			-2
		}
	}
}

func main() needs Console {
	Console.Println("\(Pumps(3))")
}
`

// envMemoDriver measures the allocations of a pump of 10 and of 1010
// signals.
const envMemoDriver = `package main

import (
	"testing"

	rt "ergo.local/prototype/runtime/rt"
)

func TestEnvMemoAllocs(t *testing.T) {
	env := &Env{task: rt.Root("allocs")}
	run := func(n int) float64 {
		return testing.AllocsPerRun(20, func() {
			if s := Pumps(env, n); s != n {
				t.Fatalf("Pumps(%d) = %v", n, s)
			}
		})
	}
	t.Logf("ALLOCS %v %v", run(10), run(1010))
}
`

// envMemoProgram is a Go module file that runs the start envMemoText
// writes (the text itself, not a copy of it) over a stand-in Env, whose
// cap field plays a capability, against runtime/rt's table of kept copies.
func envMemoProgram() string {
	return `package main

import (
	"runtime"
	"testing"
	"weak"

	rt "ergo.local/prototype/runtime/rt"
)

type Env struct {
	task *rt.Task
	from *Env
	cap  int
}

// enter is a host callback's start: the environment a body made from
// outer runs in on the task t.
func enter(outer *Env, t *rt.Task) *Env {
` + envMemoText("\t", "e", "outer", "t", "Env", "c") + `	return e
}

func main() {}

func TestEnvMemo(t *testing.T) {
	root := rt.Root("maker")
	a, b := &Env{task: root, cap: 1}, &Env{task: root, cap: 2}
	// Two environments' callbacks on one task that is not their maker's:
	// each body sees its own capabilities.
	other := rt.Root("host")
	for range 3 {
		for _, outer := range []*Env{a, b, a} {
			if e := enter(outer, other); e.task != other || e.cap != outer.cap {
				t.Fatalf("a callback of the environment with cap %d on another task sees task %p (want %p), cap %d", outer.cap, e.task, other, e.cap)
			}
		}
	}
	// More tasks than the table has slots: each task's callback runs on
	// its own task, never on the task of a copy that shares its slot.
	for i := range 3 * 256 {
		task := rt.Root("many")
		if e := enter(a, task); e.task != task || e.cap != 1 {
			t.Fatalf("task %d: the callback runs on task %p, want its own %p", i, e.task, task)
		}
	}
	// A kept copy holds its task only until another task's copy takes its
	// slot: then the ended task is collected (counted by weak pointers,
	// after one forced collection).
	const n = 64
	gone := make([]weak.Pointer[rt.Task], n)
	for i := range n {
		task := rt.Root("ended")
		enter(a, task)
		gone[i] = weak.Make(task)
	}
	for range 256 {
		enter(a, rt.Root("later"))
	}
	runtime.GC()
	live := 0
	for _, w := range gone {
		if w.Value() != nil {
			live++
		}
	}
	if live != 0 {
		t.Errorf("%d of %d ended tasks are still held after every slot was taken by another task's copy", live, n)
	}
}
`
}

// TestHostCallbackEnvironmentIsItsOwn holds the kept copies of
// envMemoText to its two conditions: a body runs on its own task (the
// table's slot may hold the copy of another task whose ID shares it) and
// with its own environment's capabilities (one task may run callbacks of
// two environments), and a kept copy keeps its ended task only until the
// slot is reused. It builds Go because the subject is the run-time
// behaviour of the emitted text over runtime/rt's table, which go/types
// does not decide.
//
// Mutants it catches that nothing else does (mutants.json):
// env-memo-other-env, env-memo-other-task.
func TestHostCallbackEnvironmentIsItsOwn(t *testing.T) {
	needsBuild(t)
	goTestEmittedVerbose(t, &lowered{name: "envmemoown", files: []File{{Path: "main.go", Data: []byte(envMemoProgram())}}}, `package main

import "testing"

func TestEnvMemoRuns(t *testing.T) { TestEnvMemo(t) }
`)
}

// TestHostCallbackKeepsItsEnvironment: a host callback that runs an Ergo
// body on the host's task (closures.go, childEnv) copies the environment
// only when it runs on a task it has not run on before, so the two
// callbacks of each signal of a durable actor (its step and its save)
// make no copy: per signal, the program allocates at least 1.5 fewer
// times than the reference lowering, which copies the environment at
// every call (envLowering=copy), and the copy, which the callee takes by
// pointer, is on the heap. It counts allocations, not time. It builds Go
// because allocation is a fact of Go's escape analysis of the emitted
// code, which go/types does not decide.
//
// Mutants it catches that nothing else does (mutants.json): env-memo-
// same-task-copies (the save, on the maker's task, copies) and
// env-memo-never-kept (the step copies at every signal).
func TestHostCallbackKeepsItsEnvironment(t *testing.T) {
	needsBuild(t)
	perSignal := func(mode string) float64 {
		saved := envLowering
		envLowering = mode
		prog := mustLower(t, "envmemo"+mode, envMemoSource)
		envLowering = saved
		out := goTestEmittedVerbose(t, prog, envMemoDriver)
		_, rest, ok := strings.Cut(out, "ALLOCS ")
		if !ok {
			t.Fatalf("no ALLOCS line in\n%s", out)
		}
		var short, long float64
		if _, err := fmt.Sscan(rest, &short, &long); err != nil {
			t.Fatalf("the ALLOCS line: %v\n%s", err, out)
		}
		return (long - short) / 1000
	}
	got, ref := perSignal(""), perSignal("copy")
	t.Logf("allocations per signal: %.2f (memo), %.2f (copy at every call)", got, ref)
	if got > ref-1.5 {
		t.Errorf("a signal allocates %.2f times, the copying reference %.2f: a host callback copies the environment", got, ref)
	}
}
