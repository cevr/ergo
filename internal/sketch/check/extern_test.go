package check_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// p3Text is the P3 message (check-v1 spec add-on), quoted exactly.
const p3Text = "an extern function is called directly; wrap it in a literal: func() { f() }"

// judgeWant checks src as accept leg 2 judges a rule: exactly its
// `// want CODE` lines, one diagnostic per code on its line, and no cascade.
// In these programs every S108 is the P3 refusal, so each carries the P3
// message, quoted exactly.
func judgeWant(t *testing.T, src string) []check.Diagnostic {
	t.Helper()
	_, diags := check.CheckSource(src)
	want, have := expected(src), got(diags)
	if strings.Join(want, " ") != strings.Join(have, " ") {
		t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
	}
	for _, d := range diags {
		if d.Code == "S108" && d.Message != p3Text {
			t.Errorf("%s: want the P3 message", d)
		}
	}
	return diags
}

// TestExternRules is the A2 table of check-v1 stage E4: the call-only rule
// (C25, P3) and the std imports (C27).
func TestExternRules(t *testing.T) {
	cases := map[string]string{
		// probe stdvalue (the former lower TestLowerE1StdValueRefused):
		// the reference is invalid after its S108, so mk adds nothing.
		"std-instance-value": `import queue

func main() needs Console {
	let mk = queue.New[int] // want S108
	let q = mk(1)
	q.Close()
	Console.Println("made")
}
`,
		"std-generic-uninstantiated": `import queue

func main() {
	let mk = queue.New // want S108
	_ = mk
}
`,
		// probe slots, verbatim: a user extern in a let, a field and an
		// element; a parenthesised callee is a direct call; "abc".Upper is
		// a method lookup, not the extern. c and d are never used: their
		// S110 is the probe's own, not a cascade (a has no type, so it has
		// none).
		"user-extern-slots": `extern go strings.ToUpper func Upper(s string) string

type Box { F func(string) string }

func main() needs Console {
	let a = Upper // want S108
	let b = (Upper)("x")
	let c = Box{F: Upper} // want S108 S110
	let d = []func(string) string{Upper} // want S108 S110
	let e = "abc".Upper // want S104
	let g = "abc".Upper() // want S104
	Console.Println(b)
}
`,
		// probe methodvalue: xs.Append keeps its S104; a std and a prelude
		// extern as values are P3; h == h then meets an invalid operand.
		"std-and-prelude-values": `import scope

func main() needs Console {
	let xs = []int{1}
	let f = xs.Append // want S104
	let g = scope.OnExit[Console] // want S108
	let h = Sleep // want S108
	Console.Println("\(len(f(2)))")
	g(func() { Console.Println("x") })
	Console.Println("\(h == h)")
}
`,
		"scope-onexit-value": `import scope

func main() needs Console {
	let f = scope.OnExit[Console] // want S108
	_ = f
}
`,
		"prelude-sleep-value": `func main() {
	let h = Sleep // want S108
	_ = h
}
`,
		"wrapping-literal": `import slice

extern go strings.ToUpper func Upper(s string) string

func main() needs Console {
	let f = func() string { Upper("x") }
	let g = func(n int) []int { slice.WithCapacity[int](n) }
	Console.Println(f())
	_ = g
}
`,
		"direct-call-forms": `import queue

extern go strings.ToUpper func Upper(s string) string

func main() needs Console {
	let a = Upper("x")
	let b = (Upper)("x")
	let c = ((Upper))("x")
	let q = queue.New[int](1)
	let r = (queue.New[int])(1)
	q.Close()
	r.Close()
	Console.Println(a + b + c)
}
`,
		"append-uncalled": `func main() {
	let xs = []int{1}
	let f = xs.Append // want S104
	_ = f
}
`,
		// Satisfaction through an extern operation (196m) is a direct call
		// by the conversion's adapter, not a value use.
		"live-scheduler": `func wait(s Scheduler) { s.Sleep(1s) }

func main() {
	wait(LiveScheduler)
}
`,
		// C27: lower-v1 3b-std landed task, scope, slice and queue; each
		// import checks clean.
		"import-task": `import task

func main() needs Console {
	let t = task.Go() { 1 }
	Console.Println("\(t.Wait())")
}
`,
		"import-scope": `import scope

func main() needs Console {
	scope.OnExit() { Console.Println("bye") }
}
`,
		"import-slice": `import slice

func main() needs Console {
	let xs = slice.WithCapacity[int](2)
	Console.Println("\(len(xs))")
}
`,
		"import-queue": `import queue

func main() needs Console {
	let q = queue.New[int](1)
	q.Close()
	Console.Println("ok")
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeWant(t, src) })
	}
	for _, rule := range []string{"Esub4-extern-call-only.pos", "Esub4-extern-call-only.neg"} {
		t.Run(rule, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "v1", rule+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			judgeWant(t, string(data))
		})
	}
}

// shadowImporter serves a program package at every path it holds.
type shadowImporter map[string]*check.Package

func (s shadowImporter) Import(path string) (*check.Package, error) {
	if p, ok := s[path]; ok {
		return p, nil
	}
	return check.Std.Import(path)
}

// TestStdPathReserved is C30's checker half: an import of a std path
// resolves to std whatever importer Check was given, so a program package
// served at log cannot shadow std's log. The program package checked under
// that path has no std identity; std's has it.
func TestStdPathReserved(t *testing.T) {
	lib, err := syntax.Parse("func Shadow() int { 1 }\n")
	if err != nil {
		t.Fatal(err)
	}
	fake, ds := check.Check("log", []*syntax.File{lib}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	if fake.Std() {
		t.Fatal("a program package checked under the path log has std identity")
	}
	for _, path := range []string{"log", "trace", "queue", "scope", "slice", "task", "timeout"} {
		t.Run(path, func(t *testing.T) {
			main, err := syntax.Parse("import " + path + "\n\nfunc f() int { " + path + ".Shadow() }\n")
			if err != nil {
				t.Fatal(err)
			}
			pkg, ds := check.Check("main", []*syntax.File{main}, shadowImporter{path: fake})
			want, err := check.Std.Import(path)
			if err != nil || !want.Std() {
				t.Fatalf("std %s: %v, std identity %v", path, err, want != nil && want.Std())
			}
			var bound *check.Package
			for id, obj := range pkg.Info.Uses {
				if pn, ok := obj.(*check.PkgName); ok && id.Name == path {
					bound = pn.Pkg
				}
			}
			if bound != want {
				t.Fatalf("import %s resolved to %p (std identity %v), want std's %p\n\t%s", path, bound, bound.Std(), want, describe(ds))
			}
			// std's package declares no Shadow: the one diagnostic says so.
			if len(ds) != 1 || ds[0].Code != "S104" {
				t.Fatalf("want one S104 for Shadow, got\n\t%s", describe(ds))
			}
		})
	}
}
