package lower

import "testing"

// Stage 4 (S4c item 7): C29. Each checked Close of an owning with's
// provider releases (resources.md, Context ownership; Q-S4-1): an
// operation of the provider's operation type, a generic Close, and a
// Close that takes a pointer to a provider that is a value. The releases
// run when the with's block ends, last acquired first.

// TestCloseFormsRelease runs the three forms, nested, and the F close leg
// of the acceptance (a generic Close that needs Console).
func TestCloseFormsRelease(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import queue

type A { Use() string }

type Releaser { ..A, ..Closer }
type R { Q queue.Queue[string] } implements Releaser, Closer
func Use(_ R) string { "use R" }
func Close(r R) {
	r.Q.Push("close R") catch {
	case queue.Closed:
	}
}
func make(q queue.Queue[string]) Releaser { R{Q: q} }

type G[T] { Value T } implements A, Closer
func Use[T](_ G[T]) string { "use G" }
func Close[T](g G[T]) needs Console { Console.Println("close G") }

type P {} implements A, Closer
func Use(_ P) string { "use P" }
func Close(_ *P) needs Console { Console.Println("close P") }

func main() needs Console {
	let q = queue.New[string](4)
	with context { A: make(q) } { Console.Println(A.Use()) }
	let closed = q.Pop() catch {
	case queue.Closed:
		"none"
	}
	Console.Println(closed)
	with context { A: G[int]{Value: 1} } { Console.Println(A.Use()) }
	with context { A: P } {
		Console.Println(A.Use())
		with context { A: G[string]{Value: "s"} } { Console.Println(A.Use()) }
	}
	Console.Println("after")
}
`, "use R\nclose R\nuse G\nclose G\nuse P\nuse G\nclose G\nclose P\nafter\n")
}

// TestCloseGenericFunctionRelease: a generic Close over a type that is not
// a record is a Go function, not a method, so its release passes the type
// arguments its parameter binds against the provider: Close[int].
func TestCloseGenericFunctionRelease(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type A { Use() string }

type List[T] []T implements A, Closer
func Use[T](l List[T]) string { "use List" }
func Close[T](l List[T]) needs Console { Console.Println("close List \(len(l))") }

func main() needs Console {
	with context { A: List[int]([]int{1, 2}) } { Console.Println(A.Use()) }
	Console.Println("after")
}
`, "use List\nclose List 2\nafter\n")
}

// TestCloseOverriddenNotRun: a provider that a replace overrides is never
// built, so its Close never runs; the replacing provider's Close does.
func TestCloseOverriddenNotRun(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type A { Use() string }

type G[T] { Value T } implements A, Closer
func Use[T](_ G[T]) string { "use G" }
func Close[T](g G[T]) needs Console { Console.Println("close G") }

type P {} implements A, Closer
func Use(_ P) string { "use P" }
func Close(_ *P) needs Console { Console.Println("close P") }

let Base = context { A: P }
let Swapped = context {
	Base,
	replace A: G[int]{Value: 1},
}

func main() needs Console {
	with Swapped { Console.Println(A.Use()) }
	Console.Println("after")
}
`, "use G\nclose G\nafter\n")
}

// TestConvertedReleaseRuns is the smallest repro of two bugs batch 16a's
// r6 verifier found: through a value converted to an operation type
// (R23), Held.Close released its Res field, and Held.Keep the Holder it
// took, in an environment of the task alone, which the release then
// dereferenced. The adapter runs its functions in the environment of the
// frame that converted the value, on the calling task. Since REL-needs
// (resources R5, Release needs; G7) a converted value's release needs no
// capability, so the releases log to a sync.Ref, as the ownership grammar
// does. raced calls a converted value's operation on a Race block's task,
// which the race stops while the operation waits in Pop: the adapter's
// copy of the converting frame's environment carries the calling task
// (mutant owgen-adapter-task-kept: kept on main's task, Pop never sees the
// stop and the program deadlocks). It builds Go because the failure is a
// nil dereference in the emitted adapter's release, and a wait on the
// wrong task, which go/types cannot see.
func TestConvertedReleaseRuns(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import queue
import sync
import tasks

type Res { id int, log sync.Ref[string] }

type Bad {}

func Close(r Res) {
	r.log.Update(func(s string) string { s + "close \(r.id)\n" })
}

type Holder { r Res }

type KeeperC { Keep(h Holder) takes h, Close() }
type TagC { Mark() int, Close() }

type Held { r Res }
func Mark(h Held) int { 1 }
func Keep(h Held, x Holder) takes x { let _ = x }
func Close(h Held) {}

func run(log sync.Ref[string]) {
	let t TagC = Held{r: Res{id: 1, log: log}}
	log.Update(func(s string) string { s + "mark \(t.Mark())\n" })
	let k KeeperC = Held{r: Res{id: 3, log: log}}
	k.Keep(Holder{r: Res{id: 5, log: log}})
	log.Update(func(s string) string { s + "kept\n" })
	// On another task: the adapter runs with that task.
	let o KeeperC = Held{r: Res{id: 6, log: log}}
	let set = tasks.NewSet[Bad](tasks.LogFailures)
	tasks.Go(set, func() {
		o.Keep(Holder{r: Res{id: 7, log: log}})
		log.Update(func(s string) string { s + "kept in a task\n" })
	}) catch {
	case tasks.Closed:
	}
	set.Wait() catch {
	case Bad:
	}
}

type Box { q queue.Queue[int] }
type WaitC { Wait() int fails queue.Closed }
func Wait(b Box) int fails queue.Closed { b.q.Pop() }

// The first block is in Wait, or about to enter it, when the second wins.
func raced() int {
	let started = queue.New[int](1)
	let w WaitC = Box{q: queue.New[int](1)}
	tasks.Race([]func() int fails queue.Closed{
		func() int fails queue.Closed {
			try started.Push(1)
			w.Wait()
		},
		func() int fails queue.Closed {
			try started.Pop()
			7
		},
	}) catch {
	case queue.Closed:
		0
	}
}

func main() needs Console {
	let log = sync.NewRef[string]("")
	run(log)
	Console.Println(log.Get())
	Console.Println("won \(raced())")
}
`, "mark 1\nclose 5\nkept\nclose 7\nkept in a task\nclose 6\nclose 3\nclose 1\n\nwon 7\n")
}
