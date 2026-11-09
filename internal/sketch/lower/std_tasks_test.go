package lower

import "testing"

// TestTransformRuns (batch 12 C2): std tasks.Transform lowers and runs,
// with a function literal and with a declared function that fails and
// needs Scheduler. The second map sleeps 10, 8 and 6 virtual ns and
// returns at 10; the third fails on 9, whose sleep of 1 ends first, so it
// returns at 11 with the other calls stopped, not waited out. It builds
// the program because the runtime/v1 model test calls the Go body
// directly: only a build checks the lowering's adapter of f, which runs
// the declared function on the child's task (a wrong task would leave the
// call outside the clock's domain, and the run would hang) and returns its
// failure to the body.
func TestTransformRuns(t *testing.T) {
	needsBuild(t)
	const src = `import tasks
import testing
import time

type Odd { N int }

func check(n int) int fails Odd needs Scheduler {
	Scheduler.Sleep(duration(int64(10 - n)))
	if n%2 == 1 && n > 5 {
		return Odd{N: n}
	}
	n * n
}

func show(xs []int) string {
	let mut s = "["
	for x in xs {
		s = s + " \(x)"
	}
	s + " ]"
}

func main() needs Console {
	let c = testing.NewVirtualClock(0)
	with context { Scheduler: c, time.Clock: c } {
		let squares = tasks.Transform([]int{1, 2, 3, 4}, 2, func(n int) int { n * n })
		Console.Println(show(squares))
		let slow = tasks.Transform([]int{0, 2, 4}, 3, check) catch {
		case Odd:
			[]int{}
		}
		Console.Println("\(show(slow)) at \(time.Clock.Now().Sub(time.Unix(0, 0)))")
		let failed = tasks.Transform([]int{2, 7, 9, 4}, 4, check) catch {
		case Odd{N: n}:
			[]int{n}
		}
		Console.Println("\(show(failed)) at \(time.Clock.Now().Sub(time.Unix(0, 0)))")
	}
}
`
	runBehaviors(t, []behavior{{name: "transform", src: src, stdout: "[ 1 4 9 16 ]\n[ 0 4 16 ] at 10ns\n[ 9 ] at 11ns\n"}})
}

// TestSetRuns (batch 13 tasks-set): std tasks.Set lowers and runs on a
// VirtualClock: a Set made in one function and returned to its caller
// (the return moves it), FailFast's failure at its instant with the other
// member stopped, Closed after it, a frame whose end stops its members and
// runs their exit actions, Restart's runs after two defects, and
// LogFailures writing the failure to standard error. It builds the program
// because the runtime/v1 model test calls the Go bodies directly: only a
// build checks what the lowering adds, the hidden frame of NewSet, the
// Move of a returned Set, the adapter that runs a member on its own task
// (a wrong task would leave it outside the clock's domain and the run would
// hang) and the binding of tasks.Closed.
func TestSetRuns(t *testing.T) {
	needsBuild(t)
	const src = `import scope
import sync
import tasks
import testing
import time

type Boom { N int }

// open makes a Set and returns it: the return moves it to the caller.
func open() tasks.Set[Boom] {
	tasks.NewSet[Boom](tasks.FailFast)
}

// sleepers starts three members that sleep an hour; its frame's end
// stops them, their exit actions included.
func sleepers(cleaned sync.Ref[int]) needs Scheduler {
	let set = tasks.NewSet(tasks.LogFailures)
	for _ in 0..3 {
		tasks.Go(set, func() needs Scheduler {
			scope.OnExit() { cleaned.Update(func(v int) int { v + 1 }) }
			Scheduler.Sleep(1h)
		}) catch {
		case tasks.Closed:
		}
	}
	Scheduler.Sleep(1s)
}

func main() needs Console {
	let c = testing.NewVirtualClock(0)
	let cleaned = sync.NewRef[int](0)
	let runs = sync.NewRef[int](0)
	with context { Scheduler: c, time.Clock: c } {
		let set = open()
		tasks.Go(set, func() needs Scheduler { Scheduler.Sleep(1h) }) catch {
		case tasks.Closed:
		}
		tasks.Go(set, func() fails Boom needs Scheduler {
			Scheduler.Sleep(2s)
			return Boom{N: 7}
		}) catch {
		case tasks.Closed:
		}
		Console.Println("running \(set.Len())")
		set.Wait() catch {
		case Boom{N: n}:
			Console.Println("failed \(n) at \(time.Clock.Now().Sub(time.Unix(0, 0))), running \(set.Len())")
		}
		tasks.Go(set, func() {}) catch {
		case tasks.Closed:
			Console.Println("closed")
		}
		sleepers(cleaned)
		Console.Println("cleaned \(cleaned.Get()) at \(time.Clock.Now().Sub(time.Unix(0, 0)))")
		let again = tasks.NewSet(tasks.Restart{Limit: 2})
		tasks.Go(again, func() {
			if sync.Modify(runs, func(v int) (int, int) { (v + 1, v + 1) }) <= 2 {
				Unreachable("restart me")
			}
		}) catch {
		case tasks.Closed:
		}
		again.Wait()
		Console.Println("runs \(runs.Get())")
		let logged = tasks.NewSet[Boom](tasks.LogFailures)
		tasks.Go(logged, func() fails Boom { return Boom{N: 9} }) catch {
		case tasks.Closed:
		}
		logged.Wait() catch {
		case Boom:
		}
		Console.Println("logged, running \(logged.Len())")
	}
}
`
	runBehaviors(t, []behavior{{name: "set", src: src,
		stdout: "running 2\nfailed 7 at 2s, running 0\nclosed\ncleaned 3 at 3s\nruns 3\nlogged, running 0\n",
		stderr: []string{"error: Boom{N:9}"}}})
}
