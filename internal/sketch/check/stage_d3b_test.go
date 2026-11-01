package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Stage D3b round 2 (review m4): the checker owns the Task shape of a
// function's result. ResultIsTask holds for a declaration, a function type,
// a function value and a literal whose one result is a Task itself, and
// not for a result that holds a Task inside another type (TaskResults'
// case) or holds none.
func TestSignatureResultIsTask(t *testing.T) {
	src := `import task
type Fault {}
func work() int fails Fault { 1 }
func keep(t Task[int, Fault]) Task[int, Fault] { t }
func maybe() Option[Task[int, Fault]] { None }
func f(g func() Task[int, Fault]) int fails Fault {
	let k = keep
	let lit = func() Task[int, Fault] { task.Go() { work() } }
	try k(lit()).Wait()
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
	got := map[string]bool{}
	for id, o := range pkg.Info.Defs {
		switch o := o.(type) {
		case *check.Func:
			got["func "+id.Name] = o.Sig.ResultIsTask()
		case *check.Var:
			if s, ok := o.Type.(*check.Signature); ok {
				got["var "+id.Name] = s.ResultIsTask()
			}
		}
	}
	for n, s := range pkg.Info.Literals {
		if _, ok := n.(*syntax.FuncLit); ok {
			got["literal"] = s.ResultIsTask()
		}
	}
	want := map[string]bool{"func work": false, "func keep": true, "func maybe": false, "func f": false, "var g": true, "var k": true, "var lit": true, "literal": true}
	for k, w := range want {
		if g, ok := got[k]; !ok || g != w {
			t.Errorf("%s: ResultIsTask %v (recorded %v), want %v", k, g, ok, w)
		}
	}
	if fs := pkg.Info.TaskResults; len(fs) != 2 {
		t.Errorf("TaskResults has %d functions, want keep and maybe", len(fs))
	}
}

// Stage D3b, Rule 5: an operation type's value is already a reference, so
// an operation called through a pointer to one is S108 at the call, for a
// std operation type and for the program's own alike. The value form and a
// record's method through a pointer stay accepted.
func TestOperationThroughPointerRefused(t *testing.T) {
	cases := []d1Case{
		{"std-queue", "import queue\nfunc f(p *queue.Queue[int]) {\n\tp.Close()\n}\n", []string{"S108:3"}},
		{"std-queue-fallible", "import queue\nfunc f(p *queue.Queue[int]) fails queue.Closed {\n\ttry p.Push(1)\n}\n", []string{"S108:3"}},
		{"own", "type Shape { Area() int }\nfunc f(p *Shape) int {\n\tp.Area()\n}\n", []string{"S108:3"}},
		{"value", "import queue\nfunc f(q queue.Queue[int]) {\n\tq.Close()\n}\n", nil},
		{"record-method", "type P { X int }\nfunc Get(p P) int { p.X }\nfunc f(p *P) int {\n\tp.X\n}\n", nil},
	}
	runD1(t, cases)
	for _, tc := range cases[:3] {
		_, d := check.CheckSource(tc.src)
		col := strings.Index(strings.Split(tc.src, "\n")[2], "p.") + 1 // the call's receiver
		if len(d) != 1 || d[0].Column != col || !strings.Contains(d[0].Message, "an operation type is already a reference; call it on the value") {
			t.Errorf("%s: want the rule's text at 3:%d; got %s", tc.name, col, describe(d))
		}
	}
}
