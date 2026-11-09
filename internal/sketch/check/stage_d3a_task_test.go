package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Task escape (D3a rewrite, spec ruling item 3): one coinductive holdsTask,
// a type-position rule and an allow-list of uses. The carriers of D3a
// rounds 1-3 are the witnesses, each with the exact diagnostics (runD1).

// taskWork is a program head that starts Tasks.
const taskWork = "import task\nfunc work() int {\n\t1\n}\n"

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
