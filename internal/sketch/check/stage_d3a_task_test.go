package check_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Task escape (D3a rewrite, spec ruling item 3): one coinductive holdsTask,
// a type-position rule and an allow-list of uses. The carriers of D3a
// rounds 1-3 are the witnesses, each with the exact diagnostics (runD1).

// taskWork is a program head that starts Tasks.
const taskWork = "import task\nfunc work() int {\n\t1\n}\n"

// The allowed uses: a local let in its own frame, a return (a return
// statement, a block's final value, through if, match and paren, the
// elements of a returned tuple literal), an argument, the receiver of a
// called Wait or Stop, and the receiver of a dot call.
func TestTaskEscapeAdmitted(t *testing.T) {
	runD1(t, []d1Case{
		{"wait", taskWork + "func f() int {\n\tlet t = task.Go() { work() }\n\tt.Wait()\n}\n", nil},
		{"stop", taskWork + "func f() {\n\tlet t = task.Go() { work() }\n\tt.Stop()\n}\n", nil},
		{"paren-wait", taskWork + "func f() int {\n\tlet t = task.Go() { work() }\n\t(t.Wait)()\n}\n", nil},
		{"return", "func keep[T, E](t Task[T, E]) Task[T, E] {\n\tt\n}\n", nil},
		{"return-statement", "func keep[T, E](t Task[T, E]) Task[T, E] {\n\treturn t\n}\n", nil},
		{"return-if", "func pick[T, E](c bool, a Task[T, E], b Task[T, E]) Task[T, E] {\n\tif c { a } else { b }\n}\n", nil},
		{"pass", taskWork + "func wait[T, E](t Task[T, E]) T fails E {\n\tt.Wait()\n}\nfunc f() int {\n\twait(task.Go() { work() })\n}\n", nil},
		{"pass-value", "func wait[T, E](t Task[T, E]) T fails E {\n\tt.Wait()\n}\nfunc f[T, E](t Task[T, E], w func(t Task[T, E]) T fails E) T fails E {\n\tw(t)\n}\n", nil},
		{"annotated-local", "type Fault {}\nfunc f(t Task[int, Fault]) Task[int, Fault] {\n\tlet own Task[int, Fault] = t\n\town\n}\n", nil},
		{"literal-own-task", taskWork + "func f() func() int {\n\tfunc() int {\n\t\tlet t = task.Go() { work() }\n\t\tt.Wait()\n\t}\n}\n", nil},
		{"literal-param", "func f[T, E]() func(t Task[T, E]) T fails E {\n\tfunc(t Task[T, E]) T fails E { t.Wait() }\n}\n", nil},
		{"function-type-field", "type Job[T, E] { start func() Task[T, E] }\n", nil},
	})
}

// Stage E2 (C13): a type argument, written or inferred, holds no Task, so
// a Task inside an Option, and a phantom argument (D3a r3 M1's target),
// are S108 where the type argument is written.
func TestTaskTypeArgumentCarriers(t *testing.T) {
	runD1(t, []d1Case{
		{"local-option", "func f[T, E](t Task[T, E]) Option[Task[T, E]] {\n\tlet o Option[Task[T, E]] = None\n\to\n}\n", []string{"S108:1", "S108:2"}},
		{"phantom-target", "type Fault {}\ntype Phantom[T] { Wait() int fails Fault }\nfunc f(t Task[int, Fault]) Phantom[Task[int, Fault]] {\n\tt\n}\n", []string{"S108:3"}},
	})
}

// Every other use, and every Task-holding type outside a whole local,
// parameter or result type, is one S108 with the rule's text.
func TestTaskEscapeRefusals(t *testing.T) {
	cases := []d1Case{
		{"record-field", "type Box[T, E] { t Task[T, E] }\n", []string{"S108:1"}},
		{"variant-field", "type U[T, E] = Running{ t Task[T, E] } | Idle{}\n", []string{"S108:1"}},
		{"concrete-field", "type Fault {}\ntype Box { t Task[int, Fault] }\n", []string{"S108:2"}},
		{"field-option", "type Box[T, E] { o Option[Task[T, E]] }\n", []string{"S108:1"}},
		{"slice-param", "func f[T, E](ts []Task[T, E]) {}\n", []string{"S108:1"}},
		{"map-value", "func f[T, E](m Map[string, Task[T, E]]) {}\n", []string{"S108:1"}},
		{"tuple-type", "type Fault {}\nfunc f(p (Task[int, Fault], int)) {}\n", []string{"S108:2"}},
		// Several results are one tuple (196t): a Task is not its element.
		// A refused written type covers the values assigned to it: one
		// mistake, one diagnostic.
		{"tuple-result", "func two[T, E](t Task[T, E]) (Task[T, E], int) {\n\t(t, 1)\n}\n", []string{"S108:1"}},
		{"tuple-result-names", "func two[T, E](t Task[T, E]) (Task[T, E], Task[T, E]) {\n\t(t, t)\n}\nfunc f[T, E](t Task[T, E]) T fails E {\n\tlet a, b = two(t)\n\t_ = a\n\t_ = b\n\tt.Wait()\n}\n", []string{"S108:1"}},
		{"field-literal", "type Box[T, E] { f Task[T, E] }\nfunc f[T, E](t Task[T, E]) T fails E {\n\tlet b = Box[T, E]{f: t}\n\t_ = b\n\tt.Wait()\n}\n", []string{"S108:1"}},
		{"slice-literal", "func f[T, E](t Task[T, E]) []Task[T, E] {\n\t[]Task[T, E]{t}\n}\n", []string{"S108:1", "S108:2"}},
		{"tuple-literal", "func f[T, E](t Task[T, E]) T fails E {\n\tlet p = (t, 1)\n\tt.Wait()\n}\n", []string{"S108:2", "S110:2"}},
		{"defined-slice", "type Tasks[T, E] []Task[T, E]\n", []string{"S108:1"}},
		{"defined-instance", "type Fault {}\ntype Box[T] { v T }\ntype B Box[Task[int, Fault]]\n", []string{"S108:3"}},
		{"package-let", "type Fault {}\nfunc start() Task[int, Fault] {\n\tstart()\n}\nlet t = start()\n", []string{"S108:5"}},
		{"package-let-type", "type Fault {}\nlet o Option[Task[int, Fault]] = None\n", []string{"S108:2"}},
		{"discard", taskWork + "func f() {\n\ttask.Go() { work() }\n}\n", []string{"S108:6"}},
		{"blank-assign", taskWork + "func f() {\n\tlet t = task.Go() { work() }\n\t_ = t\n}\n", []string{"S108:7"}},
		{"reassign", taskWork + "func f() int {\n\tlet mut t = task.Go() { work() }\n\tt = task.Go() { work() }\n\tt.Wait()\n}\n", []string{"S108:7"}},
		{"blank-let", taskWork + "func f() {\n\tlet t = task.Go() { work() }\n\tlet _ = t\n}\n", []string{"S108:7"}},
		{"match-subject", "type Fault {}\nfunc f(o Option[Task[int, Fault]]) int {\n\tmatch o {\n\t\tcase Some{_}: 1\n\t\tcase None: 0\n\t}\n}\n", []string{"S108:2"}}, // C13: at the type argument
		{"method-field-t1", taskWork + "type Holder { f func() int }\nfunc f() Holder {\n\tlet t = task.Go() { work() }\n\tHolder{f: t.Wait}\n}\n", []string{"S108:8"}},
		{"method-capture-t6", taskWork + "func f() func() int {\n\tlet t = task.Go() { work() }\n\tlet w = t.Wait\n\tfunc() int { w() }\n}\n", []string{"S108:7"}},
		{"method-tuple-t7", "type Fault {}\nfunc g(t Task[int, Fault]) (func() int fails Fault, int) {\n\t(t.Wait, 1)\n}\n", []string{"S108:3"}},
		{"outer-write-t3b", taskWork + "type Fault {}\nfunc f() func() int {\n\tlet mut o Option[Task[int, Fault]] = None\n\tlet g = func() int { o = Some{task.Go() { work() }}; 0 }\n\tlet _ = o\n\tg\n}\n", []string{"S108:7", "S108:8"}},                         // C13: Option's and Some's type arguments
		{"param-store-t10", "type Fault {}\nfunc f() func(t Task[int, Fault]) {\n\tlet mut o Option[Task[int, Fault]] = None\n\tlet g = func(t Task[int, Fault]) { o = Some{t} }\n\tlet _ = o\n\tg\n}\n", []string{"S108:3", "S108:4"}},                              // C13: Option's and Some's type arguments
		{"outer-field-write", taskWork + "type Holder[T] { Value T, N int }\nfunc f() func() int {\n\tlet mut h = Holder{Value: task.Go() { work() }, N: 0}\n\tlet g = func() int { h.Value = task.Go() { work() }; 0 }\n\tlet _ = h\n\tg\n}\n", []string{"S108:7"}}, // C13: Holder's inferred type argument; h is then invalid
		{"outer-compound-write", taskWork + "type Holder[T] { Value T, N int }\nfunc f() func() int {\n\tlet mut h = Holder{Value: task.Go() { work() }, N: 0}\n\tlet g = func() int { h.N += 1; 0 }\n\tlet _ = h\n\tg\n}\n", []string{"S108:7"}},
		{"outer-index-write", taskWork + "func keep[T](x T) []T { []T{x} }\nfunc f() func() int {\n\tlet mut ts = keep(task.Go() { work() })\n\tlet g = func() int { ts[0] = task.Go() { work() }; 0 }\n\tlet _ = len(ts)\n\tg\n}\n", []string{"S108:7"}}, // C13: keep's inferred type argument; ts is then invalid
		{"pointer-t8", taskWork + "type Fault {}\nfunc put(p *Task[int, Fault]) { *p = task.Go() { work() } }\n", []string{"S108:6"}},
		{"borrow-task", taskWork + "func f() {\n\tlet mut t = task.Go() { work() }\n\t_ = &t\n}\n", []string{"S108:7"}},
		{"pointer-local", "type Fault {}\nfunc f() {\n\tlet p *Task[int, Fault]\n\t_ = p\n}\n", []string{"S108:3"}},
		{"pointer-field", "type Fault {}\ntype Box { p *Task[int, Fault] }\n", []string{"S108:2"}},
		{"capture", taskWork + "func f() func() int {\n\tlet t = task.Go() { work() }\n\tfunc() int { t.Wait() }\n}\n", []string{"S108:7"}},
		{"capture-param", "func f[T, E](t Task[T, E]) func() T fails E {\n\tfunc() T fails E { t.Wait() }\n}\n", []string{"S108:2"}},
		{"capture-through-block", taskWork + "func f() func() int {\n\tlet t = task.Go() { work() }\n\tfunc() int {\n\t\tlet u = task.Go() { t.Wait() }\n\t\tu.Wait()\n\t}\n}\n", []string{"S108:8"}},
		// A trailing block, the go block included, is a frame.
		{"go-block-reads", taskWork + "func f() int {\n\tlet t = task.Go() { work() }\n\tlet u = task.Go() { t.Wait() + 1 }\n\tu.Wait()\n}\n", []string{"S108:7"}},
		{"capture-returned", "func f[T, E](t Task[T, E]) func() Task[T, E] {\n\tfunc() Task[T, E] { t }\n}\n", []string{"S108:2"}},
	}
	runD1(t, cases)
	for _, tc := range cases {
		_, d := check.CheckSource(tc.src)
		for _, x := range d {
			// A borrow outside a parameter has its own rule, reported first.
			if x.Code == check.CodeType && !strings.Contains(x.Message, "a Task leaves its function only by return; wait on it or return it") &&
				!strings.Contains(x.Message, "a borrow *T is only a parameter or receiver type") {
				t.Errorf("%s: want the Task-escape text, got %s", tc.name, describe(d))
			}
		}
	}
}

// Info.TaskResults holds each function whose result holds a Task, and no
// other.
func TestTaskResultsInfo(t *testing.T) {
	src := taskWork + `func keep[T, E](t Task[T, E]) Task[T, E] {
	t
}

func maybe[T, E](t Task[T, E]) Option[T] {
	None
}

func wait[T, E](t Task[T, E]) T fails E {
	t.Wait()
}

func f() int {
	wait(keep(task.Go() { work() }))
}
`
	// Since stage E2 (C13) no type argument holds a Task, so a result that
	// holds one is a Task itself: maybe's Option[T] holds none.
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	var got []string
	for f := range pkg.Info.TaskResults {
		got = append(got, f.Name)
	}
	slices.Sort(got)
	if strings.Join(got, " ") != "keep" {
		t.Fatalf("TaskResults %v, want [keep]", got)
	}
}

// Calls[x].TaskResult marks each direct call, each call through a function
// value and each operation call, of a value or of a capability, whose
// result holds a Task, and no other call (stage D3b, Rule 2; D3a review m3;
// stage E2, C15).
func TestCallTaskResult(t *testing.T) {
	src := `import task
type Fault {}
type Starts { Start() Task[int, Fault] }
func work() int fails Fault {
	1
}

func ops(s Starts) int fails Fault needs Starts {
	let a = try wait(s.Start())
	let b = try wait(Starts.Start())
	a + b
}

func keep(t Task[int, Fault]) Task[int, Fault] {
	t
}

func wait(t Task[int, Fault]) int fails Fault {
	t.Wait()
}

func f() int fails Fault {
	let k = keep
	let a = try wait(keep(task.Go() { work() }))
	let b = try wait(k(task.Go() { work() }))
	let g = func() Task[int, Fault] { task.Go() { work() } }
	let c = try wait(g())
	a + b + c
}
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	got := map[string]int{}
	for x, call := range pkg.Info.Calls {
		if call.TaskResult {
			text := src[x.Pos():x.End()]
			got[fmt.Sprintf("%v", call.Kind)+" "+text[:min(4, len(text))]]++
		}
	}
	want := map[string]int{
		fmt.Sprintf("%v", check.CallFunc) + " keep":       1,
		fmt.Sprintf("%v", check.CallFuncValue) + " k(ta":  1,
		fmt.Sprintf("%v", check.CallFuncValue) + " g()":   1,
		fmt.Sprintf("%v", check.CallFunc) + " task":       3,
		fmt.Sprintf("%v", check.CallOp) + " s.St":         1,
		fmt.Sprintf("%v", check.CallProviderOp) + " Star": 1,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("TaskResult calls %v, want %v", got, want)
	}
}

// Recursive source types terminate through CheckSource, in parameters,
// locals and captured locals (holdsTask is coinductive).
func TestRecursiveTaskFreeTypes(t *testing.T) {
	runD1(t, []d1Case{
		{"slice", "type L[T] []L[T]\nfunc f(x L[int]) int { let local = x; let g = func() int { len(local) }; g() }\n", nil},
		{"map", "type M[T] Map[string, M[T]]\nfunc f(x M[int]) int { let local = x; let g = func() int { len(local) }; g() }\n", nil},
		{"tuple", "type P[T] (T, []P[T])\nfunc f(x P[int]) int { let local = x; let g = func() int { _ = local; 1 }; g() }\n", nil},
		{"mutual", "type A[T] []B[T]\ntype B[T] Map[string, A[T]]\nfunc f(x A[int]) int { let local = x; let g = func() int { len(local) }; g() }\n", nil},
		{"record", "type R { next []R, n int }\nfunc f(x R) int { let local = x; let g = func() int { local.n }; g() }\n", nil},
		{"union", "type U = Leaf{} | Node{ kids []U }\nfunc f(x U) U { let local = x; let g = func() U { local }; g() }\n", nil},
	})
}

func TestGrowingRecursiveTypesReportInstantiationCycle(t *testing.T) {
	for _, tc := range []struct{ name, declaration, typ, use string }{
		{"slice-param", "type E[T] []E[[]T]", "E[int]", "_ = x"},
		{"slice-capture", "type E[T] []E[[]T]", "E[int]", "let local = x; let g = func() int { len(local) }; _ = g()"},
		{"map-param", "type F[T] Map[string, F[(T, T)]]", "F[int]", "_ = x"},
		{"map-capture", "type F[T] Map[string, F[(T, T)]]", "F[int]", "let local = x; let g = func() int { len(local) }; _ = g()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.declaration + "\nfunc f(x " + tc.typ + ") { " + tc.use + " }\n"
			_, d := check.CheckSource(source)
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 1 || !strings.Contains(d[0].Message, "instantiation cycle") {
				t.Fatalf("want one S108 instantiation cycle, got %s", describe(d))
			}
		})
	}
}

// The Task guard of assignability: a Task never becomes an operation value
// that holds no Task, at any assignment site; one S108 at the value.
func TestTaskCannotBecomeOperationValue(t *testing.T) {
	const types = "type Fault {}\ntype Waiter { Wait() int fails Fault }\n"
	for _, tc := range []struct {
		name, body string
		line       int
	}{
		{"field", "type Holder { w Waiter }\nfunc f(t Task[int, Fault]) Holder { Holder{w: t} }\n", 4},
		{"element", "func f(t Task[int, Fault]) []Waiter { []Waiter{t} }\n", 3},
		{"map", "func f(t Task[int, Fault]) Map[string, Waiter] { Map[string, Waiter]{\"t\": t} }\n", 3},
		{"capture", "func f(t Task[int, Fault]) func() int fails Fault { let w Waiter = t; func() int fails Fault { w.Wait() } }\n", 3},
		{"parameter-store", "func store(w Waiter) []Waiter { []Waiter{w} }\nfunc f(t Task[int, Fault]) []Waiter { store(t) }\n", 4},
		{"result", "func f(t Task[int, Fault]) Waiter { t }\n", 3},
		{"assignment", "func f(t Task[int, Fault], seed Waiter) Waiter { let mut w Waiter = seed; w = t; w }\n", 3},
		{"started-task", "func work() int fails Fault { Fault }\nfunc f() Waiter { let t = task.Go() { work() }; t }\n", 5},
		{"if-join", "func f(c bool, t Task[int, Fault], w Waiter) Waiter { if c { t } else { w } }\n", 3},
		{"match-join", "func f(o Option[int], t Task[int, Fault], w Waiter) Waiter { match o { case Some{_}: t\n case None: w } }\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := types + tc.body
			if tc.name == "started-task" {
				source = "import task\n" + source
			}
			_, d := check.CheckSource(source)
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != tc.line || !strings.Contains(d[0].Message, "a Task leaves its function only by return; wait on it or return it") {
				t.Fatalf("want one Task escape S108 at line %d, got %s", tc.line, describe(d))
			}
		})
	}
	runD1(t, []d1Case{
		{"own-operation-type", types + "func f(t Task[int, Fault]) Task[int, Fault] { let own Task[int, Fault] = t; own }\n", nil},
		{"ordinary-operation-value", types + "func f(w Waiter) []Waiter { []Waiter{w} }\n", nil},
	})
}

// A defined type over Task is refused once at its definition (the base
// rule); its conversions and uses add nothing.
func TestInvalidTaskConversionsDoNotCascade(t *testing.T) {
	source := "type Fault {}\ntype MyTask Task[int, Fault]\nfunc f(t Task[int, Fault]) MyTask { let m = MyTask(t); _ = m; MyTask(t) }\nfunc g(t Task[int, Fault]) MyTask { MyTask(t) }\n"
	_, d := check.CheckSource(source)
	if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 2 || !strings.Contains(d[0].Message, "and Task[int, Fault] is an operation type") {
		t.Fatalf("want one S108 at the refused definition, got %d: %q", len(d), describe(d))
	}
}
