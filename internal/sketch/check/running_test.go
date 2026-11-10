package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestWithValueHoldsNoRunningTask fixes the text and position of the one
// rule specific to running code (running.go; #197 L8 B3t): the value of an
// owning with holds no Task or tasks.Set, directly or in a part, since its
// code may use the providers the with closes. It kills the mutants
// "running finds nothing" and "running skips a record's fields" (check
// ledger L8-B3t); the control, a with whose value is the Task's waited
// result, checks.
func TestWithValueHoldsNoRunningTask(t *testing.T) {
	const decls = "import task\n\ntype Fault {}\ntype A { Use() }\ntype First {}\nfunc Use(_ First) {}\nfunc Close(_ First) {}\ntype Holder { t Task[int, Fault] }\n\n"
	for _, c := range []struct{ name, body, want string }{
		{"task", "let t = with context { A: First } { task.Go() { A.Use()\n1 } }\nt.Stop()", "holds Task[int,"},
		{"field", "let h = with context { A: First } { Holder{t: task.Go() { A.Use()\n1 } } }\nh.t.Stop()", "holds Task[int,"},
		{"waited", "let n = with context { A: First } { let t = task.Go() { A.Use()\n1 }\nt.Wait() }\n_ = n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := decls + "func f() {\n" + c.body + "\n}\n"
			_, d := check.CheckSource(src)
			if c.want == "" {
				if len(d) != 0 {
					t.Fatalf("want clean, got %s", describe(d))
				}
				return
			}
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 11 || !strings.Contains(d[0].Message, c.want) || !strings.Contains(d[0].Message, "keeps running after the block") {
				t.Fatalf("want one S108 at line 11 naming %q, got %s", c.want, describe(d))
			}
		})
	}
}
