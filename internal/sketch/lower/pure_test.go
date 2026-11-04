package lower

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// TestPureFunctionsTakeNoEnv (lower-v1 stage 5, D-S5-10; spec D5, the
// zero-overhead pure-call contract) is model-based over function kinds and
// rows. Each program declares a subject of one kind (plain, generic,
// recursive, mutually recursive, same-named on a local type (196m), used
// as a value, called from a with body, from a task body, from a test) with
// one row variant (pure, fails, needs, or no rows but a call of a failing
// function). The model says which declared functions are pure and what
// the program prints. Every pure function is emitted with no *Env
// parameter, and its callers pass none (else go build fails); every other
// function keeps the environment; each program vets, builds and prints
// the model's output; and a direct call of a pure function allocates
// nothing (testing.AllocsPerRun beside the emitted code).
func TestPureFunctionsTakeNoEnv(t *testing.T) {
	var cases []behavior
	var models []pureModel
	var tests []*lowered
	var testModels []pureModel
	for _, k := range pureKinds {
		for _, v := range pureVariants {
			if k == "test" && v == "needs" {
				// A test declares no capability row, and no with in it
				// binds Console (S102): the needs subject is covered by
				// the other kinds.
				continue
			}
			m := pureProgram(k, v)
			if k == "test" {
				tests = append(tests, lowerTestBuild(t, m.name, m.src))
				testModels = append(testModels, m)
				continue
			}
			cases = append(cases, behavior{name: m.name, src: m.src, stdout: m.stdout})
			models = append(models, m)
		}
	}
	built := runBehaviors(t, cases)
	for _, m := range models {
		if o := built[m.name]; o != nil {
			checkEnvParams(t, m, o.mainGo())
		}
	}
	exes := buildAll(t, tests, true)
	for i, o := range tests {
		m := testModels[i]
		checkEnvParams(t, m, o.mainGo())
		if exes == nil {
			continue
		}
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 0 || stdout != m.stdout {
			t.Errorf("%s: exit %d stdout %q, want %q\nstderr %s", m.name, code, stdout, m.stdout, firstLines(stderr, 12))
		}
	}

	// A direct call of a pure function allocates nothing: plain, generic,
	// recursive, mutually recursive and a loop kernel, called from Go beside
	// the emitted code.
	alloc := mustLower(t, "alloc", pureAllocSource)
	checkEnvParams(t, pureModel{name: "alloc", pure: []string{"plain", "gen", "rec", "even", "odd", "fold"}, impure: []string{"main"}}, alloc.mainGo())
	goTestEmitted(t, alloc, pureAllocDriver)
}

// pureKinds and pureVariants span the model's domain.
var (
	pureKinds    = []string{"plain", "generic", "recursive", "mutual", "method", "value", "with", "task", "test"}
	pureVariants = []string{"pure", "fails", "needs", "callsImpure"}
)

// pureModel is one program and the model's verdict: the functions it
// emits with no environment, those that keep one, and its stdout.
type pureModel struct {
	name, src, stdout string
	pure, impure      []string // "f" for a function, "T.f" for a Go method on T
}

// pureProgram builds the program of kind k and row variant v. Every
// subject computes 2n+1 from its argument n (recursive kinds sum it down
// to 1); the needs variant prints "needs N" first; the callsImpure variant
// takes n from a failing helper and catches its failure.
func pureProgram(k, v string) pureModel {
	m := pureModel{name: k + "_" + v}
	rows, call, pre, base := "", func(c string) string { return c }, "", "n*2 + 1"
	switch v {
	case "fails":
		rows = " fails Bad"
		call = func(c string) string { return "(" + c + " catch {\n\tcase Bad:\n\t\t0\n\t})" }
		pre = "\tif n < 0 {\n\t\treturn Bad\n\t}\n"
	case "needs":
		rows = " needs Console"
		pre = "\tConsole.Println(\"needs \\(n)\")\n"
	case "callsImpure":
		base = "(helper(n) catch {\n\tcase Bad:\n\t\t0\n\t})*2 + 1"
	}
	var src, out strings.Builder
	if k == "task" {
		src.WriteString("import task\n\n") // an import comes first
	}
	src.WriteString("type Bad {}\n\nfunc helper(n int) int fails Bad {\n\tif n < 0 {\n\t\treturn Bad\n\t}\n\treturn n\n}\n\n")
	m.impure = append(m.impure, "helper") // its row fails Bad
	needsLines := func(ns ...int) {
		if v == "needs" {
			for _, n := range ns {
				fmt.Fprintf(&out, "needs %d\n", n)
			}
		}
	}
	subject := func(name string) {
		if v == "pure" {
			m.pure = append(m.pure, name)
		} else {
			m.impure = append(m.impure, name)
		}
	}
	// main catches every failure, so its row is Console alone.
	mainRows := " needs Console"
	switch k {
	case "plain", "value", "with", "task", "test":
		fmt.Fprintf(&src, "func subj(n int) int%s {\n%s\treturn %s\n}\n\n", rows, pre, base)
		subject("subj")
	case "generic":
		fmt.Fprintf(&src, "func subj[T](x T, n int) int%s {\n%s\treturn %s\n}\n\n", rows, pre, base)
		subject("subj")
	case "recursive", "mutual":
		// try stands at the start of an expression, so the recursive call
		// binds a local first.
		pairs := [][2]string{{"subj", "subj"}}
		if k == "mutual" {
			pairs = [][2]string{{"subj", "peer"}, {"peer", "subj"}}
		}
		try := ""
		if v == "fails" {
			try = "try "
		}
		for _, p := range pairs {
			fmt.Fprintf(&src, "func %s(n int) int%s {\n\tif n <= 0 {\n\t\treturn 0\n\t}\n%s\tlet r = %s%s(n - 1)\n\treturn %s + r\n}\n\n", p[0], rows, pre, try, p[1], base)
			subject(p[0])
		}
	case "method":
		src.WriteString("type Box { N int }\n\ntype Cup { N int }\n\n")
		fmt.Fprintf(&src, "func size(b Box, n int) int%s {\n%s\treturn b.N + %s\n}\n\n", rows, pre, base)
		fmt.Fprintf(&src, "func size(c Cup, n int) int%s {\n%s\treturn c.N*10 + %s\n}\n\n", rows, pre, base)
		subject("Box.size")
		subject("Cup.size")
	}
	if k == "value" {
		// apply calls a function value, an opaque call: never pure.
		gcall := "g(n)"
		if v == "fails" {
			gcall = "try g(n)"
		}
		fmt.Fprintf(&src, "func apply(g func(int) int%s, n int) int%s {\n\treturn %s\n}\n\n", rows, rows, gcall)
		m.impure = append(m.impure, "apply")
	}
	if k == "with" {
		// Get is an operation's table function with no rows: pure, and the
		// operations conversion calls it with no environment.
		src.WriteString("type Num { Get() int }\n\ntype One {}\n\nfunc Get(_ One) int { 1 }\n\n")
		m.pure = append(m.pure, "One.Get")
	}
	switch k {
	case "plain", "recursive", "mutual":
		fmt.Fprintf(&src, "func main()%s {\n\tlet v = %s\n\tConsole.Println(\"\\(v)\")\n}\n", mainRows, call("subj(3)"))
		if k == "plain" {
			needsLines(3)
			out.WriteString("7\n")
		} else {
			needsLines(3, 2, 1)
			out.WriteString("15\n")
		}
	case "generic":
		fmt.Fprintf(&src, "func main()%s {\n\tlet a = %s\n\tlet b = %s\n\tConsole.Println(\"\\(a) \\(b)\")\n}\n", mainRows, call("subj(true, 3)"), call("subj(\"s\", 4)"))
		needsLines(3, 4)
		out.WriteString("7 9\n")
	case "method":
		fmt.Fprintf(&src, "func main()%s {\n\tlet a = %s\n\tlet b = %s\n\tConsole.Println(\"\\(a) \\(b)\")\n}\n", mainRows, call("Box{N: 1}.size(3)"), call("Cup{N: 2}.size(4)"))
		needsLines(3, 4)
		out.WriteString("8 29\n")
	case "value":
		fmt.Fprintf(&src, "func main()%s {\n\tlet f = subj\n\tlet a = %s\n\tlet b = %s\n\tConsole.Println(\"\\(a) \\(b)\")\n}\n", mainRows, call("f(3)"), call("apply(subj, 4)"))
		needsLines(3, 4)
		out.WriteString("7 9\n")
	case "with":
		fmt.Fprintf(&src, "func main()%s {\n\twith context { Num: One } {\n\t\tlet v = %s + Num.Get()\n\t\tConsole.Println(\"\\(v)\")\n\t}\n}\n", mainRows, call("subj(3)"))
		needsLines(3)
		out.WriteString("8\n")
	case "task":
		fmt.Fprintf(&src, "func main()%s {\n\tlet t = task.Go() { %s }\n\tlet v = t.Wait()\n\tConsole.Println(\"\\(v)\")\n}\n", mainRows, call("subj(3)"))
		needsLines(3)
		out.WriteString("7\n")
	case "test":
		fmt.Fprintf(&src, "test Subj {\n\tassert %s == 7\n}\n", call("subj(3)"))
		needsLines(3)
		out.WriteString("ok Subj\nPASS (1 tests)\n")
	}
	if k != "test" {
		m.impure = append(m.impure, "main")
	}
	m.src, m.stdout = src.String(), out.String()
	return m
}

// envParams lists each function and Go method of the emitted file with
// whether its first parameter is the environment (*Env): "f" for a
// function, "T.f" for a method on T.
func envParams(t *testing.T, code string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", code, 0)
	if err != nil {
		t.Fatalf("parse the emitted Go: %v", err)
	}
	out := map[string]bool{}
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		key := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) == 1 {
			rt := fn.Recv.List[0].Type
			if s, ok := rt.(*ast.StarExpr); ok {
				rt = s.X
			}
			if id, ok := rt.(*ast.Ident); ok {
				key = id.Name + "." + key
			}
		}
		env := false
		if ps := fn.Type.Params.List; len(ps) > 0 {
			if s, ok := ps[0].Type.(*ast.StarExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "Env" {
					env = true
				}
			}
		}
		out[key] = env
	}
	return out
}

// checkEnvParams compares the emitted functions' environment parameters
// with the model.
func checkEnvParams(t *testing.T, m pureModel, code string) {
	t.Helper()
	got := envParams(t, code)
	check := func(names []string, want bool) {
		for _, n := range names {
			env, ok := got[n]
			if n == "main" {
				// The Ergo main is the Go function the generated Go main runs.
				env, ok = got["main_1"]
				if !ok {
					env, ok = got["ergoMain"]
				}
				if !ok {
					continue
				}
			}
			switch {
			case !ok:
				var have []string
				for k := range got {
					have = append(have, k)
				}
				sort.Strings(have)
				t.Errorf("%s: no Go function %s (have %v)", m.name, n, have)
			case env != want:
				t.Errorf("%s: %s takes the environment %v, want %v", m.name, n, env, want)
			}
		}
	}
	check(m.pure, false)
	check(m.impure, true)
}

// pureAllocSource holds pure functions of five kinds for the allocation
// check: plain, generic, recursive, mutually recursive, and a loop kernel
// (a pure frame has no interruption point, runtime.md I1).
const pureAllocSource = `func plain(n int) int { n*2 + 1 }

func gen[T](x T, n int) int { n*2 + 1 }

func rec(n int) int {
	if n <= 0 {
		return 0
	}
	return n + rec(n - 1)
}

func even(n int) bool {
	if n == 0 {
		return true
	}
	return odd(n - 1)
}

func odd(n int) bool {
	if n == 0 {
		return false
	}
	return even(n - 1)
}

func fold(n int) int {
	let mut total = 0
	for i in 0..n {
		total += (i*17 + 3) % 97
	}
	total
}

func main() needs Console {
	Console.Println("\(plain(1) + gen(true, 1) + rec(3) + fold(3))")
	if even(4) {
		Console.Println("even")
	}
}
`

// pureAllocDriver calls each pure function directly from Go, with no
// environment, and counts its allocations.
const pureAllocDriver = `package main

import "testing"

var sink int

func TestPureCallsAllocateNothing(t *testing.T) {
	for name, f := range map[string]func(){
		"plain": func() { sink += plain(sink & 7) },
		"gen":   func() { sink += gen[bool](true, sink&7) },
		"rec":   func() { sink += rec(sink & 7) },
		"even":  func() { if even(sink & 7) { sink++ } },
		"fold":  func() { sink += fold(sink & 63) },
	} {
		if n := testing.AllocsPerRun(200, f); n != 0 {
			t.Errorf("a direct call of the pure %s allocates %v times", name, n)
		}
	}
	if plain(3) != 7 || gen[string]("s", 4) != 9 || rec(3) != 6 || !even(4) || odd(4) || fold(3) != 60 {
		t.Errorf("the pure functions compute the wrong values")
	}
}
`
