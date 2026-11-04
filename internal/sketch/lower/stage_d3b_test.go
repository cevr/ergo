package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
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
// meet its requirements where a context builds it (#37). The checker refuses
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
		"ergo.Go[int64](&fs, ergo.Site(\"start calls task.Go at /ergo-test/main.ergo:14:2\"), func(t *rt.Task) (int64, error) {\n\t\te := *env\n\t\te.task = t\n",
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

// TestTaskInsideAnotherTypeChecked: a result that holds a Task inside
// another type, or a Task as a type argument, was a lowering refusal in
// stage D3b; since stage E2 (C13, C14) the checker refuses it (S108), so
// lowering never sees one (Rule 2).
func TestTaskInsideAnotherTypeChecked(t *testing.T) {
	cases := []struct {
		name, src string
		line      int
	}{
		{"option-result", "type Fault {}\nfunc maybe() Option[Task[int, Fault]] {\n\tNone\n}\n", 2},
		{"type-argument", "import task\nfunc work() int { 1 }\nfunc id[T](v T) T { v }\nfunc f() int {\n\tlet t = id(task.Go() { work() })\n\tt.Wait()\n}\n", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := lowerSource("r", tc.src, false)
			if len(o.diags) != 1 || o.diags[0].Code != check.CodeType || o.diags[0].Line != tc.line ||
				!strings.Contains(o.diags[0].Message, "holds a Task, and a type argument never does") {
				t.Fatalf("want one S108 on line %d; got %v", tc.line, o.diags)
			}
		})
	}
}

// An operation that returns a Task takes its caller's frame scope as the
// destination, as a function does (Rule 2; stage E2, C15): a call of a
// value's operation, a method value, an operations conversion's adapter, a
// type parameter's witness, a type parameter converted to the operation
// type, and a capability a context provides all run. Before stage E2 the first
// two were refused and the witness call did not compile. The program is
// lowered in process first, so a mutant of the lowering (a go test
// -overlay) reaches it; wantRun builds the CLI with go run.
func TestErgoRunTaskReturningOperations(t *testing.T) {
	mustLower(t, "taskops", taskOpsProgram)
	wantRun(t, taskOpsProgram, "3 4 12 101 22\n")
}

const taskOpsProgram = `import task

type Fault {}

type Starts {
	Start(k int) Task[int, Fault],
}

type Impl { N int }

func Start(i Impl, k int) Task[int, Fault] {
	let n = i.N + k
	task.Go() { n }
}

func boxed[T Starts](x T) Starts {
	x
}

func viaParam[T Starts](x T) int fails Fault {
	let t = x.Start(10)
	try t.Wait()
}

func viaValue(s Starts) int fails Fault {
	let f = s.Start
	let t = f(20)
	try t.Wait()
}

func viaCap() int fails Fault needs Starts {
	let t = Starts.Start(100)
	try t.Wait()
}

let W = context { Starts: Impl{N: 1} }

func main() fails Fault needs Console {
	let s Starts = Impl{N: 2}
	let direct = try s.Start(1).Wait()
	let t = boxed(Impl{N: 3}).Start(1)
	let x = try t.Wait()
	let y = try viaParam(s)
	let z = try with W { viaCap() }
	let v = try viaValue(s)
	Console.Println("\(direct) \(x) \(y) \(z) \(v)")
}
`

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

// TestTimeoutAfterRefusedNamingLowerV1: timeout.After is the one std
// function that does not lower in stage D3b. Its Go body is runtime/rt.After,
// which takes an rt.Scheduler (NewTimer) where the Ergo Scheduler is
// Sleep-shaped, and fails with rt.Expired where timeout.Expired is a program
// type; it needs a runtime/v1 body, which runtime/** owns (a lower-v1
// Follow-up). The refusal names that owner at the call (stage D3b round 1
// review m1).
func TestTimeoutAfterRefusedNamingLowerV1(t *testing.T) {
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
	if r := o.refused[0]; r.Line != 6 || !strings.Contains(r.Message, "lower-v1 (a runtime/v1 body for timeout.After)") {
		t.Fatalf("want a refusal of timeout.After on line 6 naming lower-v1 (a runtime/v1 body for timeout.After); got %+v", r)
	}
}

// TestInterpolationOfOtherTypes: a value of a type that is not basic is
// never Go's %v of a lowered representation. Since check-v1 stage E6a
// (D-E6) a record is refused by the checker at the value (S109, #49), so it
// never reaches the lowering, and a type defined over a basic type lowers
// as its basic value.
func TestInterpolationOfOtherTypes(t *testing.T) {
	o := lowerSource("r", "type P { X int }\nfunc f(p P) string {\n\t\"p \\(p)\"\n}\n", false)
	if len(o.diags) != 1 || o.diags[0].Code != "S109" || o.diags[0].Line != 3 || o.diags[0].Column != 7 || !strings.Contains(o.diags[0].Message, "#49") {
		t.Fatalf("record: want one S109 naming #49 at 3:7; got %v", o.diags)
	}
	o = lowerSource("r", "type Celsius int\nfunc f(c Celsius) string {\n\t\"\\(c) C\"\n}\n", false)
	if len(o.diags) != 0 || o.panicked != nil || len(o.refused) != 0 || o.err != nil {
		t.Fatalf("defined: want a clean lowering; got %v, refusals %v, panic %v, err %v", o.diags, o.refused, o.panicked, o.err)
	}
	if !strings.Contains(o.mainGo(), "strconv.AppendInt(make([]byte, 0, 20), int64(c), 10)") {
		t.Fatalf("defined: want c converted to int64 and appended:\n%s", o.mainGo())
	}
}
