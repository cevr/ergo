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
