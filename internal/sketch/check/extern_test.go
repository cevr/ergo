package check_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
)

// p3Text is the P3 message (check-v1 spec add-on), quoted exactly.
const p3Text = "an extern function is called directly; wrap it in a literal: func() { f() }"

// p3Named is the P3 message that names the extern and the literal to pass
// (K4, G-ex-f-3): "Upper is an extern and is not a value; pass
// func(s string) string { Upper(s) } (grammar.md, Extern)".
var p3Named = regexp.MustCompile(`^([\w.]+) is an extern and is not a value; pass func\([^{]*\)[^{]* \{ ([\w.]+)\([\w, ]*\) \} \(grammar\.md, Extern\)$`)

// isP3 reports the P3 message in either form: the named form when the
// extern's literal can be spelled, the plain text otherwise.
func isP3(msg string) bool {
	if msg == p3Text {
		return true
	}
	m := p3Named.FindStringSubmatch(msg)
	return m != nil && m[1] == m[2]
}

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
		if d.Code == "S108" && !isP3(d.Message) {
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
		// probe slots: "abc".Upper is a method lookup, not the extern. (The
		// probe's value slots, S108 before G1, are TestExternCallOnlyDomain's
		// property now.)
		"user-extern-slots": `extern go strings.ToUpper func Upper(s string) string

func main() needs Console {
	let b = (Upper)("x")
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
	let h = Sleep // want S104
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
	let h = Sleep // want S104
	_ = h
}
`,
		"wrapping-literal": `import slices

extern go strings.ToUpper func Upper(s string) string

func main() needs Console {
	let f = func() string { Upper("x") }
	let g = func(n int) []int { slices.WithCapacity[int](n) }
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

func main() needs Ambient {
	wait(NewLiveScheduler())
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
		"import-slice": `import slices

func main() needs Console {
	let xs = slices.WithCapacity[int](2)
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

// judgeOutside checks src as judgeWant does, except that every diagnostic
// is an S109 whose message holds each of texts.
func judgeOutside(t *testing.T, src string, texts ...string) {
	t.Helper()
	_, diags := check.CheckSource(src)
	want, have := expected(src), got(diags)
	if strings.Join(want, " ") != strings.Join(have, " ") {
		t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
	}
	for _, d := range diags {
		for _, text := range texts {
			if d.Code != "S109" || !strings.Contains(d.Message, text) {
				t.Errorf("%s: want an S109 naming %q", d, text)
			}
		}
	}
}

// queueMine is a program record whose functions satisfy queue.Queue[int]
// (196m): the probe convstd's declarations.
const queueMine = `import queue

type Mine {}

func Push(_ Mine, v int) fails queue.Closed {}

func Pop(_ Mine) int fails queue.Closed {
	0
}

func Close(_ Mine) {}

func Values(_ Mine) Seq[int] {
	queue.New[int](0).Values()
}

func Poll(_ Mine) Option[int] {
	None
}

func Len(_ Mine) int {
	0
}
`

// TestStdProtocolOpen is the sealed rule of G2 part 2 (stdlib.md, Types 3;
// check.SealedStd): os.Process, which no std extern's result names, takes
// a program provider at each site that converts by assignability, while
// queue.Queue, which queue.New returns, still refuses the same program
// value at the same sites with S109.
func TestStdProtocolOpen(t *testing.T) {
	const fake = `import os

type FakeEnv { Argv []string }

func Args(e FakeEnv) []string { e.Argv }

func Getenv(_ FakeEnv, key string) Option[string] { None }
`
	cases := map[string]string{
		"process-context": fake + `
func run() needs os.Process { _ = os.Process.Args() }

func main() {
	with context { os.Process: FakeEnv{Argv: []string{}} } { run() }
}
`,
		"process-host-context": `import os

func run() needs os.Process { _ = os.Process.Args() }

func main() needs Ambient {
	let live = context { os.Process: os.NewHost() }
	with live { run() }
}
`,
		"process-param": fake + `
func use(p os.Process) { _ = p.Args() }

func main() needs Ambient {
	use(FakeEnv{Argv: []string{}})
	let p os.Process = os.NewHost()
	use(p)
}
`,
		"queue-param": queueMine + `
func use(q queue.Queue[int]) { q.Close() }

func main() {
	use(Mine) // want S109
	let q queue.Queue[int] = Mine // want S109
	q.Close()
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeOutside(t, src, "sealed std protocol", "#206") })
	}
}

// TestCloseRefusals is C29 (check-v1 stage E4 A6; lower-v1 stage 4): every
// checked Close of an owning with's provider releases, an operation of its
// type, a generic Close and a Close on a pointer while the provider is a
// value included, so each checks clean (lower/stage4_close_test.go runs
// them). One form stays refused, once, at the provider: a generic Close
// whose type parameter is constrained by operations, whose release would
// pass witnesses no fact records (S109 naming #197, closeWitnessed). A std
// or extern Close is unreachable: no std or prelude Close is an extern, and
// a user extern Close takes a named type, outside the C26 domain.
func TestCloseRefusals(t *testing.T) {
	cases := map[string]string{
		"operation": `type A { Use() }
type Releaser { Use(), Close() }
type R {}
func Use(_ R) {}
func Close(_ R) {}
func make() Releaser { R }
let W = context { A: make() }
func main() { with W { A.Use() } }
`,
		"generic": `type A { Use() }
type G[T] { Value T }
func Use[T](_ G[T]) {}
func Close[T](_ G[T]) {}
let W = context { A: G[int]{Value: 1} }
func main() { with W { A.Use() } }
`,
		"pointer-close-value-provider": `type A { Use() }
type P {}
func Use(_ P) {}
func Close(_ *P) {}
let W = context { A: P }
func main() { with W { A.Use() } }
`,
		"value-close": `type A { Use() }
type P {}
func Use(_ P) {}
func Close(_ P) {}
let W = context { A: P }
func main() { with W { A.Use() } }
`,
		"generic-witnessed": `type A { Use() }
type Named { Name() string }
type N {}
func Name(_ N) string { "n" }
type G[T] { Value T }
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) {}
let W = context { A: G[N]{Value: N} } // want S109
func main() { with W { A.Use() } }
`,
		// Once per provider, though two withs release it.
		"generic-witnessed-once": `type A { Use() }
type Named { Name() string }
type N {}
func Name(_ N) string { "n" }
type G[T] { Value T }
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) {}
let W = context { A: G[N]{Value: N} } // want S109
func main() {
	with W { A.Use() }
	with W { A.Use() }
}
`,
		// The refusal ends the provider's judgment: no release charge
		// follows it, though the Close needs what main lacks (ledger
		// mutant own-check-15: the continue after the refusal dropped).
		"generic-witnessed-no-charge": `type A { Use() }
type Named { Name() string }
type N {}
func Name(_ N) string { "n" }
type G[T] { Value T }
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) needs Console { Console.Println("closed") }
let W = context { A: G[N]{Value: N} } // want S109
func main() { with W { A.Use() } }
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeOutside(t, src, "#197") })
	}
	t.Run("user-extern-close", func(t *testing.T) {
		judgeOutside(t, "type R {}\nextern go strings.F func Close(r R) // want S109\n", "#202")
	})
	t.Run("no-std-extern-close", func(t *testing.T) {
		prog, ds := check.CheckSource("func f(c Console) { c.Println(\"x\") }\n")
		if len(ds) != 0 {
			t.Fatal(describe(ds))
		}
		var pkgs []*check.Package
		for id, obj := range prog.Info.Uses {
			if tn, ok := obj.(*check.TypeName); ok && id.Name == "Console" {
				pkgs = append(pkgs, tn.Pkg)
			}
		}
		if len(pkgs) != 1 || !pkgs[0].Std() {
			t.Fatalf("the prelude package: %v", pkgs)
		}
		for _, path := range std.Packages() {
			pkg, err := check.Std.Import(path)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			pkgs = append(pkgs, pkg)
		}
		// The walk sees std's externs (queue.New, Sleep, …), so it would
		// see an extern Close.
		closes, externs := 0, 0
		for _, pkg := range pkgs {
			for _, f := range pkg.Funcs {
				if f.Decl != nil && f.Decl.Extern != nil {
					externs++
				}
				if f.Name != "Close" {
					continue
				}
				closes++
				if f.Decl == nil || f.Decl.Extern != nil {
					t.Errorf("%s: a Close function that is an extern: %s", pkg.Path, fmt.Sprint(f.Decl))
				}
			}
		}
		if externs == 0 {
			t.Fatal("the walk saw no std extern")
		}
		t.Logf("std and prelude: %d externs, %d Close functions", externs, closes)
	})
}
