package lower

import "testing"

// Stage E2, C18: a frame need (a call whose result holds a Task,
// scope.OnExit, an owning with, a context with) outside a function body is
// the checker's S108 (check/frames.go). Inside a package let's trailing
// block or function literal it has the frame scope of that block, so the
// checker admits it and it lowers and runs.
func TestErgoRunFrameNeedsInPackageLetBlocks(t *testing.T) {
	wantRun(t, `import task
import scope
import log

type Fault {}
type A { Use() int }
type Conn { V int }

func Use(c Conn) int { c.V }
func Close(c Conn) {}
func get() Conn { Conn{V: 3} }
func start() Task[int, Fault] { task.Go() { 1 } }
func apply(body func() int) int { body() }
func wait(t Task[int, Fault]) int {
	t.Wait() catch {
	case Fault:
		0
	}
}

let Owned = wire { A: get() }

let a = apply() { wait(start()) }
let b = apply() { with Owned { A.Use() } }
let c = apply() { with log.Annotate("k", "v") { 4 } }
let d = apply() {
	scope.OnExit() { }
	5
}
let f = func() int { wait(task.Go() { 6 }) }

func main() needs Console {
	Console.Println("\(a) \(b) \(c) \(d) \(f())")
}
`, "1 3 4 5 6\n")
}

// Stage E2, C16 and C17: every form the escaping rule admits lowers and
// runs: an escaping parameter stored in a field and in a result, a
// non-escaping one called, passed on, bound to a local and reassigned,
// captured by a literal or trailing block passed to a non-escaping
// parameter, and by a literal called at once; a function value whose
// parameter is non-escaping; and task.Go's escaping block.
func TestErgoRunEscapingForms(t *testing.T) {
	wantRun(t, `import task

type Fault {}
type Holder { Run escaping func(int) int }
type Runner { Twice(g func(int) int, x int) int }
type R {}

func Twice(r R, g func(int) int, x int) int { g(g(x)) }

func keep(f escaping func(int) int) Holder { Holder{Run: f} }
func give(f escaping func(int) int) escaping func(int) int { f }
func apply(f func(int) int, x int) int { f(x) }
func now(body func() int) int { body() }
func each(body func(k func(int) int) int) int { body(func(x int) int { x * 10 }) }

func local(f func(int) int, g func(int) int) int {
	let mut h = g
	let a = h(1)
	h = f
	a + h(1)
}

func captured(f func(int) int) int {
	let a = now(func() int { f(1) })
	let b = now() { apply(f, 2) }
	let c = (func() int { f(3) })()
	a + b + c
}

func viaValue(f func(int) int) int {
	let k = apply
	let r Runner = R
	k(f, 4) + r.Twice(f, 1)
}

func started(f escaping func(int) int) Task[int, Fault] {
	task.Go() { f(5) }
}

func main() needs Console {
	let inc = func(x int) int { x + 1 }
	let h = keep(inc)
	let g = give(inc)
	let t = started(inc)
	let w = t.Wait() catch {
	case Fault:
		0
	}
	Console.Println("\(h.Run(1)) \(g(2)) \(local(inc, inc)) \(captured(inc)) \(viaValue(inc)) \(each(func(k) { k(7) })) \(w)")
}
`, "2 3 4 9 8 70 6\n")
}
