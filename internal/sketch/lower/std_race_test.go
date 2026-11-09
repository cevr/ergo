package lower

import "testing"

// TestRaceRuns (batch 12 C4): std tasks.Race lowers and runs over a slice
// of function literals on a VirtualClock. The first race's 2 s block beats
// the 5 s one, which is stopped (its exit action runs) rather than waited
// out, so the clock reads 2s; in the second race the failing 1 s block
// ends first, and Race fails with its failure. It builds the program
// because the runtime/v1 model test calls the Go body directly: only a
// build checks Race's Ergo body, which hands the blocks to the Go body as
// one indexed function, and the lowering's adapter of that function.
func TestRaceRuns(t *testing.T) {
	needsBuild(t)
	const src = `import scope
import tasks
import testing
import time

type Late { After int }

func main() needs Console {
	let c = testing.NewVirtualClock(0)
	with context { Scheduler: c, time.Clock: c } {
		let first = tasks.Race([]func() string needs Scheduler | Console{
			func() string needs Scheduler | Console {
				scope.OnExit() { Console.Println("slow stopped") }
				Scheduler.Sleep(5s)
				"slow"
			},
			func() string needs Scheduler | Console {
				Scheduler.Sleep(2s)
				"fast"
			},
		})
		Console.Println("\(first) at \(time.Clock.Now().Sub(time.Unix(0, 0)))")
		let second = tasks.Race([]func() int fails Late needs Scheduler{
			func() int fails Late needs Scheduler {
				Scheduler.Sleep(3s)
				3
			},
			func() int fails Late needs Scheduler {
				Scheduler.Sleep(1s)
				return Late{After: 1}
			},
		}) catch {
		case Late{After: n}:
			-n
		}
		Console.Println("\(second) at \(time.Clock.Now().Sub(time.Unix(0, 0)))")
	}
}
`
	runBehaviors(t, []behavior{{name: "race", src: src, stdout: "slow stopped\nfast at 2s\n-1 at 3s\n"}})
}
