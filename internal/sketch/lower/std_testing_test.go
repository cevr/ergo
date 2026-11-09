package lower

import "testing"

// TestVirtualClockRuns (batch 12 C1): std testing.VirtualClock lowers and
// runs. Two tasks sleep 3h and 1h, and a timeout.After of 2h races a body
// that sleeps 5h; the program waits on each with no loop on Sleepers and
// no Advance, and the clock moves to each deadline in turn. It builds the
// program because the runtime/v1 model test calls the Go bodies directly:
// only a build checks that the lowering passes NewVirtualTimeline the
// caller's task (a fresh task would be a member that never waits, and the
// run would hang) and binds the package's Now and Sleep as the providers.
func TestVirtualClockRuns(t *testing.T) {
	const src = `import queue
import task
import testing
import time
import timeout

func nap(d duration, tag string, q queue.Queue[string]) fails queue.Closed needs Scheduler | time.Clock {
	Scheduler.Sleep(d)
	try q.Push("\(tag)@\(time.Clock.Now().Sub(time.Unix(0, 0)))")
}

func main() fails queue.Closed needs Console {
	let c = testing.NewVirtualClock(0)
	with context { Scheduler: c, time.Clock: c } {
		let q = queue.New[string](8)
		let a = task.Go() { try nap(3h, "a", q) }
		let b = task.Go() { try nap(1h, "b", q) }
		try a.Wait()
		try b.Wait()
		let late = timeout.After(2h) {
			Scheduler.Sleep(5h)
			"body"
		} catch {
		case timeout.Expired:
			"expired"
		}
		q.Close()
		for line in q.Values() {
			Console.Println(line)
		}
		Console.Println("\(late) at \(time.Clock.Now().Sub(time.Unix(0, 0)))")
	}
}
`
	runBehaviors(t, []behavior{{name: "virtualclock", src: src, stdout: "b@1h0m0s\na@3h0m0s\nexpired at 5h0m0s\n"}})
}
