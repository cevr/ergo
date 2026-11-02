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
