package lower

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

const stepBudgetCompiledSource = `import os
import process
import queue
import scope
import task
import time

func spin(n int, q queue.Queue[int]) int {
	let mut acc = 0
	for i in 0..n {
		let _ = q.Len()
		acc = acc + i
	}
	acc
}

func burnRec(n int, q queue.Queue[int]) {
	let _ = q.Len()
	if n <= 1 {
		return
	}
	burnRec(n - 1, q)
}

func fail(msg string) os.Exit { os.Exit{Code: 1, Message: msg} }

func main() fails os.Exit {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let q = queue.New[int](8)

		for limit in 1..9 {
			for work in 1..10 {
				let marks = queue.New[int](4)
				let got = process.Run(process.Limits{..process.Unlimited, Steps: 1 + limit}) {
					scope.OnExit() {
						marks.Push(1) catch {
						case queue.Closed:
						}
					}
					burnRec(work, q)
					work
				} catch {
				case process.OverSteps as o:
					if o.Limit != 1+limit || work <= limit || marks.Len() != 1 {
						return fail("unexpected OverSteps limit=\(o.Limit) work=\(work) marks=\(marks.Len())")
					}
					-1
				case process.OverTime, process.Crashed:
					return fail("unexpected non-step exit")
				}
				if work <= limit && (got != work || marks.Len() != 1) {
					return fail("expected completion for work=\(work) limit=\(limit) got=\(got) marks=\(marks.Len())")
				}
				if work > limit && got != -1 {
					return fail("expected OverSteps for work=\(work) limit=\(limit) got=\(got)")
				}
			}
		}

		let local = process.Local
		for limit in 1..7 {
			for work in 1..8 {
				let n = limit % 4
				let total = 2 + n + limit
				let rep = local.Play(process.Limits{..process.Unlimited, Steps: total}, process.Script{Cleanups: n, Waits: 0ns, Steps: work, End: process.Gives{Value: 1}}, m)
				if rep.Cleaned != n {
					return fail("local Play cleaned mismatch")
				}
				let ok = match rep.Ended {
				case process.Gives as g:
					work <= limit && g.Value == 1
				case process.OverSteps as o:
					work > limit && o.Limit == total
				case _:
					false
				}
				if !ok {
					return fail("local Play boundary mismatch work=\(work) limit=\(limit)")
				}
			}
		}

		let loopHit = process.Run(process.Limits{..process.Unlimited, Steps: 5}) {
			spin(100, q)
		} catch {
		case process.OverSteps as o:
			o.Limit
		case process.OverTime, process.Crashed:
			return fail("loop did not hit OverSteps")
		}
		if loopHit != 5 {
			return fail("loop OverSteps limit mismatch")
		}

		let childHit = process.Run(process.Limits{..process.Unlimited, Steps: 6}) {
			let c = task.Go() { spin(200, q) }
			c.Wait()
		} catch {
		case process.OverSteps as o:
			o.Limit
		case process.OverTime, process.Crashed:
			return fail("child tree did not hit OverSteps")
		}
		if childHit != 6 {
			return fail("child tree OverSteps limit mismatch")
		}

		let sibDone = queue.New[int](1)
		let sibling = task.Go() {
			let v = spin(50, q)
			sibDone.Push(v) catch {
			case queue.Closed:
			}
			v
		}
		let _ = process.Run(process.Limits{..process.Unlimited, Steps: 4}) {
			spin(200, q)
		} catch {
		case process.OverSteps:
			0
		case process.OverTime, process.Crashed:
			return fail("budgeted sibling did not hit OverSteps")
		}
		if sibling.Wait() != 1225 {
			return fail("sibling task was affected by budgeted process")
		}

		let bothMarks = queue.New[int](2)
		let bothStepsFirst = process.Run(process.Limits{..process.Unlimited, Time: 10ms, Steps: 5}) {
			scope.OnExit() {
				bothMarks.Push(1) catch {
				case queue.Closed:
				}
			}
			burnRec(20, q)
			Scheduler.Sleep(20ms)
			0
		} catch {
		case process.OverSteps as o:
			o.Limit
		case process.OverTime, process.Crashed:
			return fail("expected OverSteps before OverTime")
		}
		if bothStepsFirst != 5 || bothMarks.Len() != 1 {
			return fail("both-limits steps-first mismatch")
		}

		let timeMarks = queue.New[int](2)
		let runner = task.Go() {
			process.Run(process.Limits{..process.Unlimited, Time: 2ms, Steps: 100}) {
				scope.OnExit() {
					timeMarks.Push(1) catch {
					case queue.Closed:
					}
				}
				burnRec(3, q)
				Scheduler.Sleep(5ms)
				0
			} catch {
			case process.OverTime as o:
				if o.Limit != 2ms { -1 } else { 42 }
			case process.OverSteps, process.Crashed:
				-2
			}
		}
		for m.Sleepers() < 2 {
		}
		m.Advance(3ms)
		let timeResult = runner.Wait()
		if timeResult != 42 || timeMarks.Len() != 1 {
			return fail("both-limits time-first mismatch")
		}
	}
}
`

// TestStepBudgetCompiledEndToEnd lowers and executes a compiled Ergo program
// using process.Run with Limits.Steps under both the reference poll-every-edge
// mode and the default amortized mode, verifying exact step counts, loop
// interruption, child subtree accounting, sibling isolation, cleanup before
// return, and Time vs Steps racing.
func TestStepBudgetCompiledEndToEnd(t *testing.T) {
	needsBuild(t)
	for _, mode := range []string{"each", "amortized"} {
		t.Run(mode, func(t *testing.T) {
			defer SetPollAmortLoweringForTest(mode)()
			name := "stepbudget_" + mode
			exes := buildAll(t, []*lowered{mustLower(t, name, stepBudgetCompiledSource)}, true)
			ctx, cancel := testwait.Context(t, context.Background())
			defer cancel()
			cmd := exec.CommandContext(ctx, exes[name])
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := runProgram(t, cmd)
			if ctx.Err() != nil {
				t.Fatalf("mode %s hung\n%s", mode, firstLines(out.String(), 20))
			}
			if err != nil {
				t.Fatalf("mode %s exit %v\n%s", mode, err, firstLines(out.String(), 20))
			}
		})
	}
}
