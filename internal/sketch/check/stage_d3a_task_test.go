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

// A defined type over Task is refused once at its definition (the base
// rule); its conversions and uses add nothing.
func TestInvalidTaskConversionsDoNotCascade(t *testing.T) {
	source := "type Fault {}\ntype MyTask Task[int, Fault]\nfunc f(t Task[int, Fault]) MyTask { let m = MyTask(t); _ = m; MyTask(t) }\nfunc g(t Task[int, Fault]) MyTask { MyTask(t) }\n"
	_, d := check.CheckSource(source)
	if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 2 || !strings.Contains(d[0].Message, "and Task[int, Fault] is an operation type") {
		t.Fatalf("want one S108 at the refused definition, got %d: %q", len(d), describe(d))
	}
}
