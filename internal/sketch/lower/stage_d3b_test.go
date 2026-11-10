package lower

import (
	"strings"
	"testing"
)

// Stage D3b (check-v1): every lowered function takes the environment, which
// carries the running task (Rule 1); frame scopes and Task destinations,
// task.Go and scope.OnExit (Rule 2); the queue operations with the caller's
// task, the Values adapter and queue.Closed (Rule 3); the runtime files in
// the emitted module (Rule 4); and the pointer-receiver rule (Rule 5).

// taskProgram starts tasks, returns them through a direct call, a function
// value, a literal and a fallible function, stops one, and registers two
// exit actions on main.
const taskProgram = `import task
import scope

type Fault {}

func work(n int64) int64 fails Fault {
	if n < 0 {
		return Fault
	}
	return n * 2
}

func start(n int64) Task[int64, Fault] {
	task.Go() { work(n) }
}

func startChecked(n int64) Task[int64, Fault] fails Fault {
	if n < 0 {
		return Fault
	}
	let t = start(n)
	t
}

func main() fails Fault needs Console {
	scope.OnExit() { Console.Println("exit 1") }
	scope.OnExit() { Console.Println("exit 2") }
	let direct = try task.Go() { work(21) }.Wait()
	let s = start
	let viaValue = try s(4).Wait()
	let lit = func(n int64) Task[int64, Fault] { start(n) }
	let viaLiteral = try lit(5).Wait()
	let checked = try startChecked(6)
	let viaFallible = try checked.Wait()
	if direct == 42 && viaValue == 8 && viaLiteral == 10 && viaFallible == 12 {
		Console.Println("waited")
	}
	let k = task.Go() { work(1) }
	k.Stop()
	Console.Println("body")
}
`

// TestTaskGoAndTaskResults: a Task started, returned through a direct
// call, a function value, a literal and a fallible return, waited for and
// stopped, runs its block and its exit actions after the body, last
// registered first (Rule 2). A returned Task is an owned value (#197 L8
// B3t): the caller registers it, so no destination frame is passed.
func TestTaskGoAndTaskResults(t *testing.T) {
	needsBuild(t)
	wantRun(t, taskProgram, "waited\nbody\nexit 2\nexit 1\n")
}

// stripDirectives is Go source without its //line and /*line*/ directives.
func stripDirectives(s string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if strings.HasPrefix(line, "//line ") {
			continue
		}
		for {
			i := strings.Index(line, "/*line ")
			if i < 0 {
				break
			}
			j := strings.Index(line[i:], "*/")
			if j < 0 {
				break
			}
			line = line[:i] + line[i+j+2:]
		}
		b.WriteString(line)
	}
	return b.String()
}

// queueProgram moves values from a producer task to its consumer through a
// queue, and catches queue.Closed from Push and from Pop on a closed queue.
const queueProgram = `import queue
import task

func produce(q queue.Queue[int], n int) int fails queue.Closed {
	for i in 1..n {
		try q.Push(i)
	}
	q.End()
	n - 1
}

func consume(q queue.Queue[int]) int {
	let mut sum = 0
	for {
		let v = q.Pop() catch {
		case queue.Closed:
			return sum
		}
		sum += v
	}
	sum
}

func main() fails queue.Closed needs Console {
	let q = queue.New[int](0)
	let p = task.Go() { produce(q, 5) }
	let sum = consume(q)
	let made = try p.Wait()
	if sum == 10 && made == 4 {
		Console.Println("moved")
	}
	q.Push(7) catch {
	case queue.Closed:
		Console.Println("closed")
	}
}
`

// TestErgoRunQueueOperations: Push and Pop pass the caller's task (Rule 3),
// a producer and a consumer meet on an unbuffered queue, and a lowered catch
// matches the runtime's Closed (queue.Closed lowers to ergo.Closed).
func TestErgoRunQueueOperations(t *testing.T) {
	needsBuild(t)
	o := mustLower(t, "queues", queueProgram)
	goSrc := stripDirectives(o.mainGo())
	for _, want := range []string{"(q).Push(env.task, ", "(q).Pop(env.task)", "(q).End()", "ergo.New[int](", "ergo.Closed"} {
		if !strings.Contains(goSrc, want) {
			t.Errorf("the lowered Go lacks %q:\n%s", want, goSrc)
		}
	}
	wantRun(t, queueProgram, "moved\nclosed\n")
}

// TestRuntimeFilesHoldRT: an emitted module holds runtime/v1 and the
// runtime/rt package it imports, whose *Task the environment carries (Rule
// 4).
func TestRuntimeFilesHoldRT(t *testing.T) {
	have := map[string]bool{}
	for _, f := range Runtime() {
		have[f.Path] = true
	}
	for _, want := range []string{runtimeDir + "/go.mod", runtimeDir + "/runtime/v1/queue.go", runtimeDir + "/runtime/v1/task.go", runtimeDir + "/runtime/rt/task.go", runtimeDir + "/runtime/rt/scope.go"} {
		if !have[want] {
			t.Errorf("the emitted runtime lacks %s; it has %v", want, have)
		}
	}
}

// TestErgoRunSliceWithCapacity: slices.WithCapacity calls its runtime/v1
// body, which has no hidden argument (Rule 6).
func TestErgoRunSliceWithCapacity(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import slices

func main() needs Console {
	let mut xs = slices.WithCapacity[int](4)
	xs = xs.Append(7)
	xs = xs.Append(8)
	Console.Println("\(len(xs)) \(xs[1])")
}
`, "2 8\n")
}

// timeout.After, which stage D3b refused naming lower-v1, lowers since
// lower-v1 stage 4 to v1.After (stage4_timeout_test.go).
