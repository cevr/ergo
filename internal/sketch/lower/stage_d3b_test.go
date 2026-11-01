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

// TestEveryFunctionTakesTheEnvironment: a pure function, a method, a
// package initializer and the entry all bind the environment, which holds
// the running task; no shared empty environment remains (Rule 1, D1). The
// program runs, with a pure caller of a function value and a package let
// that calls a function.
func TestEveryFunctionTakesTheEnvironment(t *testing.T) {
	src := `type Box { N int }

func Twice(b Box) int { b.N * 2 }

func pure(n int) int { n + 1 }

func apply(f func(int) int, n int) int { f(n) }

let start = pure(1)

func main() needs Console {
	let b = Box{N: start}
	if apply(pure, b.Twice()) == 5 {
		Console.Println("ok")
	}
}
`
	o := mustLower(t, "env", src)
	goSrc := o.mainGo()
	for _, want := range []string{
		"type Env struct {\n\ttask    *rt.Task\n\tConsole Console\n}",
		"func pure(env *Env, n int) int {",
		"func (b Box) Twice(env *Env) int {",
		"func apply(env *Env, f func(*Env, int) (int, error), n int) int {",
		"func ergoInit(env *Env) {",
		"ergo.Main(func(t *rt.Task) error {\n\t\tenv := &Env{task: t, Console: hostConsole{}}\n\t\tergoInit(env)\n\t\tmain_1(env)",
		"func (hostConsole) Println(_ *rt.Task, line string) { ergo.Console{}.Println(line) }",
	} {
		if !strings.Contains(goSrc, want) {
			t.Errorf("the lowered Go lacks %q:\n%s", want, goSrc)
		}
	}
	if strings.Contains(goSrc, "noEnv") {
		t.Errorf("the shared empty environment remains:\n%s", goSrc)
	}
	wantRun(t, src, "ok\n")
}

// TestProviderNeedingCapabilitiesRefused:an operation takes the caller's
// task first and no capability (rows.md, Rows 7), so a table function whose
// row needs a capability cannot provide an operation until constructors
// meet its requirements where a wire builds it (#37). The checker refuses
// it at the conversion (S108), so the lowering's guard in operations is
// unreachable (stage D3b ruling, D2).
func TestProviderNeedingCapabilitiesRefused(t *testing.T) {
	src := `type Shape { Area() int }

type Circle { R int }

func Area(c Circle) int needs Console {
	Console.Println("area")
	return 3 * c.R * c.R
}

func total(s Shape) int { s.Area() }

func main() needs Console {
	let n = total(Circle{R: 2})
	Console.Println("\(n)")
}
`
	o := lowerSource("provider", src, true)
	if len(o.diags) != 1 {
		t.Fatalf("want one checker diagnostic; got %v", o.diags)
	}
	d := o.diags[0].String()
	if !strings.HasPrefix(d, "13:16: S108 ") || !strings.Contains(d, "requires {Console}") || !strings.Contains(d, "#37") {
		t.Fatalf("want S108 at 13:16 naming the needed Console and #37; got %s", d)
	}
}

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

// TestTaskGoAndTaskResults: a function that starts a task or receives one by
// return opens its frame scope, its error result named for End; a function
// that returns a Task takes the caller's scope as its destination and moves
// the Task there on return, through a direct call, a function value, a
// literal and a fallible return; task.Go's block runs on a copy of the
// environment that holds the child's task, with its Site; the exit actions
// run after the body, last registered first (Rule 2).
func TestTaskGoAndTaskResults(t *testing.T) {
	o := mustLower(t, "tasks", taskProgram)
	goSrc := stripDirectives(o.mainGo())
	for _, want := range []string{
		"func start(env *Env, dest *ergo.Scope, n int64) *ergo.Task[int64] {\n\tfs := ergo.NewScope(env.task)\n\tdefer fs.End(nil)\n",
		"return v_1.Move(dest)\n",
		"func startChecked(env *Env, dest *ergo.Scope, n int64) (_ *ergo.Task[int64], err error) {\n\tfs := ergo.NewScope(env.task)\n\tdefer fs.End(&err)\n",
		"func main_1(env *Env) (err error) {\n\tfs := ergo.NewScope(env.task)\n\tdefer fs.End(&err)\n",
		"ergo.Go[int64](&fs, ergo.Site(\"task.Go at /ergo-test/main.ergo:14:2\"), func(t *rt.Task) (int64, error) {\n\t\te := *env\n\t\te.task = t\n",
		"ergo.OnExit(&fs, func() {\n",
		"dest *ergo.Scope, p int64) (*ergo.Task[int64], error) {\n\t\treturn start(env_4, dest, p), nil\n",
		"v_3, err_3 := s(env, &fs, int64(4))\n",
		"v_5 := start(env_5, &fs_1, n)\n\t\treturn v_5.Move(dest_1), nil\n",
		"v_6, err_6 := lit(env, &fs, int64(5))\n",
		"v_8, err_8 := startChecked(env, &fs, int64(6))\n",
	} {
		if !strings.Contains(goSrc, want) {
			t.Errorf("the lowered Go lacks %q:\n%s", want, goSrc)
		}
	}
	wantRun(t, taskProgram, "waited\nbody\nexit 2\nexit 1\n")
}

// TestFrameScopeOnlyWhereNeeded: a function with no task.Go, no OnExit and
// no call whose result holds a Task opens no frame scope; a task.Go inside
// a literal or a trailing block gives that literal the scope, not its
// enclosing function (Rule 2).
func TestFrameScopeOnlyWhereNeeded(t *testing.T) {
	src := `import task

func work() int { 1 }

func plain(n int) int { n + 1 }

func run(f func() int) int { f() }

func outer() int {
	let f = func() int {
		let t = task.Go() { work() }
		t.Wait()
	}
	f() + run() { let u = task.Go() { work() }
		u.Wait() }
}

func main() needs Console {
	if plain(1) + outer() == 4 {
		Console.Println("ok")
	}
}
`
	o := mustLower(t, "frames", src)
	goSrc := stripDirectives(o.mainGo())
	if n := strings.Count(goSrc, "NewScope("); n != 2 {
		t.Errorf("want 2 frame scopes (the literal and the trailing block); got %d:\n%s", n, goSrc)
	}
	for _, head := range []string{"func plain(env *Env, n int) int {\n", "func outer(env *Env) int {\n", "func main_1(env *Env) {\n"} {
		i := strings.Index(goSrc, head)
		if i < 0 {
			t.Fatalf("no %q in:\n%s", head, goSrc)
		}
		if strings.HasPrefix(goSrc[i+len(head):], "\tfs") {
			t.Errorf("%q opens a frame scope:\n%s", head, goSrc)
		}
	}
	wantRun(t, src, "ok\n")
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

// TestTaskInsideAnotherTypeRefused: a result that holds a Task inside
// another type, or a Task as a type argument, is stage E's: a located
// refusal, never wrong Go (Rule 2; the ruling's stage E carry).
func TestTaskInsideAnotherTypeRefused(t *testing.T) {
	cases := []struct {
		name, src string
		line, col int
	}{
		{"option-result", "type Fault {}\nfunc maybe() Option[Task[int, Fault]] {\n\tNone\n}\n", 2, 6},
		{"type-argument", "import task\nfunc work() int { 1 }\nfunc id[T](v T) T { v }\nfunc f() int {\n\tlet t = id(task.Go() { work() })\n\tt.Wait()\n}\n", 5, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := lowerSource("r", tc.src, false)
			if len(o.diags) != 0 {
				t.Fatalf("want a clean check; got %v", o.diags)
			}
			if o.panicked != nil || len(o.refused) == 0 {
				t.Fatalf("want a refusal; got refusals %v, panic %v, err %v\n%s", o.refused, o.panicked, o.err, o.mainGo())
			}
			r := o.refused[0]
			if r.Line != tc.line || r.Column != tc.col || !strings.Contains(r.Message, "check-v1 stage E") {
				t.Fatalf("want a refusal at %d:%d naming check-v1 stage E; got %+v", tc.line, tc.col, r)
			}
		})
	}
}

// queueProgram moves values from a producer task to its consumer through a
// queue, and catches queue.Closed from Push and from Pop on a closed queue.
const queueProgram = `import queue
import task

func produce(q queue.Queue[int], n int) int fails queue.Closed {
	for i in 1..n {
		try q.Push(i)
	}
	q.Close()
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
	o := mustLower(t, "queues", queueProgram)
	goSrc := stripDirectives(o.mainGo())
	for _, want := range []string{"(q).Push(env.task, ", "(q).Pop(env.task)", "(q).Close()", "ergo.New[int](", "ergo.Closed"} {
		if !strings.Contains(goSrc, want) {
			t.Errorf("the lowered Go lacks %q:\n%s", want, goSrc)
		}
	}
	wantRun(t, queueProgram, "moved\nclosed\n")
}

// TestErgoRunStopReachesPop: a stop reaches a task waiting on Pop of an
// empty open queue, so the waiting task ends and the program exits (Rule 3,
// the poller.ergo hang).
func TestErgoRunStopReachesPop(t *testing.T) {
	wantRun(t, `import queue
import task

func consume(q queue.Queue[int]) int fails queue.Closed {
	let v = try q.Pop()
	v
}

func main() needs Console {
	let q = queue.New[int](1)
	let t = task.Go() { consume(q) }
	t.Stop()
	Console.Println("stopped")
}
`, "stopped\n")
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

// TestErgoRunInterpolation: a value of a basic type in `"\(v)"` prints as
// Go's %v prints it (grammar.md, Strings 5; the stage D3b ruling on
// interpolation): strings, bools, signed and unsigned integers, floats of
// both sizes and durations; segments keep their escapes, `\\(` is a
// backslash and a parenthesis, an interpolation may nest, and values are
// read left to right.
func TestErgoRunInterpolation(t *testing.T) {
	wantRun(t, `func next(n int) int {
	n + 1
}

func main() needs Console {
	let s = "go"
	let b = true
	let i = -42
	let i8 int8 = -8
	let u uint64 = 18446744073709551615
	let f = 1.5
	let g float32 = 0.1
	let big = 1000000.0
	let d = 1500ms
	Console.Println("\(s) \(b) \(i) \(i8) \(u)")
	Console.Println("\(f) \(g) \(big) \(d)")
	Console.Println("tab\t\"q\" \\(x) \(i)")
	Console.Println("\(next(1))\(next(2))")
	Console.Println("a \("b \(next(0)) c") d")
	Console.Println("\(s)")
}
`, "go true -42 -8 18446744073709551615\n1.5 0.1 1e+06 1.5s\ntab\t\"q\" \\(x) -42\n23\na b 1 c d\ngo\n")
}

// TestErgoRunSliceWithCapacity: slice.WithCapacity calls its runtime/v1
// body, which has no hidden argument (Rule 6).
func TestErgoRunSliceWithCapacity(t *testing.T) {
	wantRun(t, `import slice

func main() needs Console {
	let mut xs = slice.WithCapacity[int](4)
	xs = xs.Append(7)
	xs = xs.Append(8)
	Console.Println("\(len(xs)) \(xs[1])")
}
`, "2 8\n")
}

// TestTimeoutAfterRefusedNamingStageE: timeout.After is the one std
// function that does not lower in stage D3b. Its Go body is runtime/rt.After,
// which takes an rt.Scheduler (NewTimer) where the Ergo Scheduler is
// Sleep-shaped, and fails with rt.Expired where timeout.Expired is a program
// type; it needs a runtime/v1 body (a lower-v1 Follow-up). The refusal names
// check-v1 stage E at the call.
func TestTimeoutAfterRefusedNamingStageE(t *testing.T) {
	o := lowerSource("r", `import timeout

func work() int { 1 }

func f() int fails timeout.Expired needs Scheduler {
	timeout.After(5s) { work() }
}
`, false)
	if len(o.diags) != 0 {
		t.Fatalf("want a clean check; got %v", o.diags)
	}
	if o.panicked != nil || len(o.refused) != 1 {
		t.Fatalf("want one refusal; got refusals %v, panic %v, err %v", o.refused, o.panicked, o.err)
	}
	if r := o.refused[0]; r.Line != 6 || !strings.Contains(r.Message, "After") || !strings.Contains(r.Message, "check-v1 stage E") {
		t.Fatalf("want a refusal of timeout.After on line 6 naming check-v1 stage E; got %+v", r)
	}
}

// TestInterpolationOfOtherTypesRefused: a value of a type that is not basic
// (a record, a defined type) is check-v1 stage E's: a located refusal at the
// value, never Go's %v of a lowered representation.
func TestInterpolationOfOtherTypesRefused(t *testing.T) {
	cases := []struct {
		name, src string
		line, col int
	}{
		{"record", "type P { X int }\nfunc f(p P) string {\n\t\"p \\(p)\"\n}\n", 3, 7},
		{"defined", "type Celsius int\nfunc f(c Celsius) string {\n\t\"\\(c) C\"\n}\n", 3, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := lowerSource("r", tc.src, false)
			if len(o.diags) != 0 {
				t.Fatalf("want a clean check; got %v", o.diags)
			}
			if o.panicked != nil || len(o.refused) == 0 {
				t.Fatalf("want a refusal; got refusals %v, panic %v, err %v\n%s", o.refused, o.panicked, o.err, o.mainGo())
			}
			r := o.refused[0]
			if r.Line != tc.line || r.Column != tc.col || !strings.Contains(r.Message, "check-v1 stage E") {
				t.Fatalf("want a refusal at %d:%d naming check-v1 stage E; got %+v", tc.line, tc.col, r)
			}
		})
	}
}
