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
