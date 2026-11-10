package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestScriptDiagnostics: the text and line of each refusal of a
// testing.Script (stdlib.md, Testing 7 and 9). Mutants: a type parameter
// admitted (script-typeparam-admitted), a type that is not a capability
// admitted (script-noncap-admitted), an operation that takes a function
// admitted (script-func-param-admitted), one that returns a Task admitted
// (script-task-admitted), a literal with fields admitted
// (script-literal-fields-admitted), a Script with an operation its
// capability lacks taken as a provider (script-implements-any), an
// operation that takes a borrow admitted (c4-script-borrow-admitted), and
// one that takes a resource admitted (c4-script-resource-admitted), or
// never checked (c4-script-kept-unchecked). What a
// Script records and answers is the testing laws' (TestStdLawsRun).
func TestScriptDiagnostics(t *testing.T) {
	const head = "import testing\n\ntype Count { Len() int }\ntype Each { Each(f func(n int)) }\ntype Spawn { Start() Task[int] }\ntype More { Len() int, Cap() int }\ntype Box { n int }\n"
	rows := []struct{ name, src, want string }{
		{"type parameter", head + "func g[T](s testing.Script[T]) {}\n", "S108:8 testing.Script[T]: T is a type parameter"},
		{"not a capability", head + "func f(s testing.Script[Box]) {}\n", "S108:8 testing.Script[Box]: Box is not a capability"},
		{"function parameter", head + "func f(s testing.Script[Each]) {}\n", "S108:8 testing.Script[Each]: its operation Each takes a function"},
		{"Task result", head + "func f(s testing.Script[Spawn]) {}\n", "S108:8 testing.Script[Spawn]: its operation Start returns a Task"},
		{"literal with fields", head + "func f() testing.Script[Count] {\n\ttesting.Script[Count]{Len: 1}\n}\n", "S108:9 is made with no fields, testing.Script[Count]{}"},
		{"wider capability", head + "func f(s testing.Script[Count]) More { s }\n", "S108:8 testing.Script[Count]: Count has no method Cap"},
		{"borrow parameter", head + "type Fill { Fill(b *Box) }\nfunc f(s testing.Script[Fill]) {}\n", "S108:9 testing.Script[Fill]: its operation Fill takes the borrow b *Box"},
		{"resource parameter", head + "func f(s testing.Script[Pool]) {}\ntype Pool { Use(c Conn) }\ntype Conn { Close() }\n", "S108:8 testing.Script[Pool]: its operation Use takes the resource c Conn"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, d := check.CheckSource(r.src)
			head, msg, _ := strings.Cut(r.want, " ")
			for _, x := range d {
				if x.Code+":"+strconv.Itoa(x.Line) == head && strings.Contains(x.Message, msg) {
					return
				}
			}
			t.Fatalf("want %s: %s", r.want, describe(d))
		})
	}
}
