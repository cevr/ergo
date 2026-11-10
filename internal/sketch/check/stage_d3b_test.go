package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Stage D3b, Rule 5: an operation type's value is already a reference, so
// an operation called through a pointer to one is S108 at the call, for a
// std operation type and for the program's own alike. The value form and a
// record's method through a pointer stay accepted.
func TestOperationThroughPointerRefused(t *testing.T) {
	cases := []d1Case{
		{"std-queue", "import queue\nfunc f(p *queue.Queue[int]) {\n\tp.CloseSend()\n}\n", []string{"S108:3"}},
		{"std-queue-fallible", "import queue\nfunc f(p *queue.Queue[int]) fails queue.Closed {\n\ttry p.Push(1)\n}\n", []string{"S108:3"}},
		{"own", "type Shape { Area() int }\nfunc f(p *Shape) int {\n\tp.Area()\n}\n", []string{"S108:3"}},
		{"value", "import queue\nfunc f(q queue.Queue[int]) {\n\tq.CloseSend()\n}\n", nil},
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
