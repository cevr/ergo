package lower

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// exitOfWaiterSource has a waiter task take a sleeping task out of the
// block of the task that started it (outer), wait for it with
// task.ExitOf, and be stopped. (A task block only borrows what it
// captures, so the waiter gets the sleeper from outer's Wait.) runtime.md
// Tasks 13 says a stop of the waiting task is Wait's: the sleeper is
// stopped and drained, and ExitOf never returns to the waiter. The program
// exits 0 when Stop returns with no sleeper left.
const exitOfWaiterSource = `import task
import time

func main() {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let waiter = task.Go() {
			let outer = task.Go() {
				task.Go() {
					Scheduler.Sleep(1h)
					0
				}
			}
			let sleeper = outer.Wait()
			let _ = task.ExitOf(sleeper)
			Unreachable("ExitOf returned to a stopped waiter")
		}
		for m.Sleepers() == 0 {
		}
		waiter.Stop()
		if m.Sleepers() != 0 {
			Unreachable("the sleeper outlived its waiter's stop")
		}
	}
}
`

// TestExitOfWaiterIsTheCaller holds that the waiter of task.ExitOf is the
// task that calls it, not the task that started the Task: a stop of the
// caller ends the wait (runtime.md Tasks 13). It builds Go because the
// fault is in the runtime's join (rt.Handle parks the starter, PROCESS-RUN
// request), which only a run of the lowered program shows; the
// evaluator, which has no starter, passes law task.ExitOfAnywhere.
//
// It is red on main as of PROCESS-RUN r1 (measured: the program deadlocks,
// Go's "all goroutines are asleep", with main in the waiter's Stop) and skipped
// by name until wf10 NG STOP-NESTED-WAIT makes the waiter the caller and
// gives TaskExit the caller-waiter marking; ERGO_STOP_NESTED_WAIT=1 runs it.
// The test's deadline (testwait) only stops the hung program and names it.
func TestExitOfWaiterIsTheCaller(t *testing.T) {
	needsBuild(t)
	if os.Getenv("ERGO_STOP_NESTED_WAIT") == "" {
		t.Skip("red until wf10 NG STOP-NESTED-WAIT: TaskExit waits as the Task's starter, so a stop of the task that calls ExitOf does not end its wait")
	}
	exes := buildAll(t, []*lowered{mustLower(t, "exitofwaiter", exitOfWaiterSource)}, true)
	ctx, cancel := testwait.Context(t, context.Background()) // the test's own deadline
	defer cancel()
	cmd := exec.CommandContext(ctx, exes["exitofwaiter"])
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := runProgram(t, cmd) // the kit's memory bound
	if ctx.Err() != nil {
		t.Fatalf("hung: the stop of the task in task.ExitOf did not end its wait (the waiter is the Task's starter, not the caller)\n%s", firstLines(out.String(), 20))
	}
	if err != nil {
		t.Fatalf("exit %v\n%s", err, firstLines(out.String(), 20))
	}
}
