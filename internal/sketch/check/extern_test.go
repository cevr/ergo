package check_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
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
`

// sealedPrograms holds, for each operation type of std and the prelude, a
// program whose value of type Mine satisfies it and is passed to a
// parameter of that type at the one line that starts "\tuse(". A std
// operation type with no row here fails TestStdProtocolSealed, and so does
// a row whose type std no longer declares.
var sealedPrograms = map[string]string{
	"queue.Queue": queueMine + `
func use(q queue.Queue[int]) { q.Close() }

func main() {
	use(Mine)
}
`,
	"prelude.Task": `type Fault {}
type Mine {}

func Wait(_ Mine) int fails Fault { 0 }
func Stop(_ Mine) {}

func use(t Task[int, Fault]) { t.Stop() }

func main() {
	use(Mine)
}
`,
	"prelude.Console": `type Mine {}

func Println(_ Mine, line string) {}

func use(c Console) { c.Println("x") }

func main() {
	use(Mine)
}
`,
	// Sleep is a prelude name no program redeclares, so the program value
	// is one of a program operation type.
	"prelude.Scheduler": `type Mine { Sleep(d duration) }

func use(s Scheduler) { s.Sleep(1ms) }

func pass(m Mine) {
	use(m)
}
`,
	"prelude.Clock": `type Mine {}

func Now(_ Mine) int64 { 0 }

func use(c Clock) { _ = c.Now() }

func main() {
	use(Mine)
}
`,
	"prelude.Random": `type Mine {}

func Int63(_ Mine) int64 { 0 }

func use(r Random) { _ = r.Int63() }

func main() {
	use(Mine)
}
`,
	// std os (G2): open, as no std extern's result names it (stdlib.md,
	// os 5).
	"os.Process": `import os

type Mine {}

func Args(_ Mine) []string { []string{} }
func Getenv(_ Mine, key string) Option[string] { None }

func use(p os.Process) { _ = p.Args() }

func main() {
	use(Mine)
}
`,
	// std fs (G4): open, as no std extern's result names it (stdlib.md,
	// fs 5).
	"fs.Files": `import fs

type Mine {}

func ReadFile(_ Mine, path string) string fails fs.NotFound | fs.Denied | fs.Failed { "" }
func ReadDir(_ Mine, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { []string{} }
func Stat(_ Mine, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Info{Name: path, Size: 0, Dir: false}
}

func use(f fs.Files) { _ = f.ReadFile("a") catch { case fs.NotFound, fs.Denied, fs.Failed: "" } }

func main() {
	use(Mine)
}
`,
	// The prelude's Ambient (host-authority H1): its one operation is
	// unexported, so no program type implements it (sealedBy), though no
	// std extern names it (unexportedStd).
	"prelude.Ambient": `type Mine {}

func ambient(_ Mine) {}

func use(_ Ambient) {}

func main() {
	use(Mine)
}
`,
}

// unexportedStd is the std operation types sealed by an unexported
// operation: a program value is refused at the use line by the sealedBy
// text, not by SealedStd's S109.
var unexportedStd = map[string]string{
	"prelude.Ambient": "ambient is an unexported operation of prelude.Ambient, so only a type of package prelude implements it",
}

// openStd is the std operation types a program provides: no std extern's
// result names them (check.SealedStd; stdlib.md, Types 3). Every other
// std operation type is sealed.
var openStd = map[string]bool{
	"prelude.Console":   true,
	"prelude.Scheduler": true,
	"prelude.Clock":     true,
	"prelude.Random":    true,
	"os.Process":        true,
	"fs.Files":          true,
}

// stdOperationTypes lists, as path.Name, every exported operation type
// (a braced type of operations, not a union) the prelude and each std
// package declare, read from their source with the parser: independent of
// the checker.
func stdOperationTypes(t *testing.T) []string {
	t.Helper()
	sources := map[string]map[string]string{std.PreludePath: std.Prelude()}
	for _, path := range std.Packages() {
		files, ok := std.Package(path)
		if !ok {
			t.Fatalf("std lists %s but does not serve it", path)
		}
		sources[path] = files
	}
	var out []string
	for path, files := range sources {
		for name, src := range files {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("%s/%s: %v", path, name, err)
			}
			for _, d := range f.Decls {
				td, ok := d.(*syntax.TypeDecl)
				if !ok || !unicode.IsUpper([]rune(td.Name.Name)[0]) {
					continue
				}
				if it, ok := td.Spec.(*syntax.InterfaceType); ok && len(it.Union) == 0 {
					out = append(out, path+"."+td.Name.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestStdProtocolSealed is C28 of check-v1 stage E4 (A6): a value converted
// to a sealed std protocol (the prelude's Task, or an operation type of a
// std package other than the prelude, the types lower/std.go runtimeType
// represents by a runtime/v1 type) is S109 naming #206, at every site that
// converts by assignability. The prelude's capabilities stay open: a
// program provides them (the over-seal control).
func TestStdProtocolSealed(t *testing.T) {
	sealed := []string{"sealed std protocol", "#206"}
	cases := map[string]string{
		// probe convstd, verbatim.
		"convstd": queueMine + `
func use(q queue.Queue[int]) {
	q.Close()
}

func main() needs Console {
	use(Mine) // want S109
	Console.Println("converted")
}
`,
		"written-conversion": queueMine + `
func main() {
	let q = queue.Queue[int](Mine) // want S109
	q.Close()
}
`,
		"let-typed": queueMine + `
func main() {
	let q queue.Queue[int] = Mine // want S109
	q.Close()
}
`,
		"tail-value": queueMine + `
func make() queue.Queue[int] {
	Mine // want S109
}
`,
		"let-elements": queueMine + `
func pair() (Mine, Mine) { (Mine, Mine) }

func main() {
	let a, b queue.Queue[int] = pair() // want S109
	a.Close()
	b.Close()
}
`,
		"compare": queueMine + `
func same(q queue.Queue[int]) bool {
	q == Mine // want S109
}
`,
		// A program operation type's value is a program value too.
		"program-operation-type": `import queue

type Mine {
	Push(v int) fails queue.Closed,
	Pop() int fails queue.Closed,
	Close(),
	Values() Seq[int],
}

func use(q queue.Queue[int]) { q.Close() }

func pass(m Mine) {
	use(m) // want S109
}
`,
		"task": `type Fault {}
type Mine {}

func Wait(_ Mine) int fails Fault { 0 }
func Stop(_ Mine) {}

func use(t Task[int, Fault]) { t.Stop() }

func main() {
	use(Mine) // want S109
}
`,
		// Controls: a value of the std type itself converts nothing, and
		// the prelude's Console takes a program provider.
		"queue-identity": `import queue

func use(q queue.Queue[int]) { q.Close() }

func main() {
	use(queue.New[int](1))
}
`,
		"console-param": `type Mine {}

func Println(_ Mine, line string) {}

func use(c Console) { c.Println("x") }

func main() {
	use(Mine)
}
`,
		"console-provider": `type Quiet {}

func Println(_ Quiet, line string) {}

let W = context { Console: Quiet }

func main() {
	with W { Console.Println("x") }
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeOutside(t, src, sealed...) })
	}
	types := stdOperationTypes(t)
	for _, key := range types {
		t.Run("enumerate/"+key, func(t *testing.T) {
			src, ok := sealedPrograms[key]
			if !ok {
				t.Fatalf("the std operation type %s has no row in sealedPrograms", key)
			}
			// The oracle, written by hand: the operation types a std
			// extern's result names (task.Go, queue.New) are sealed.
			isSealed := !openStd[key]
			at, uses := 0, 0
			for i, line := range strings.Split(src, "\n") {
				if strings.HasPrefix(line, "\tuse(") {
					at, uses = i+1, uses+1
				}
			}
			if uses != 1 {
				t.Fatalf("the row has %d use( lines, want 1", uses)
			}
			_, diags := check.CheckSource(src)
			if text, ok := unexportedStd[key]; ok {
				if len(diags) != 1 || diags[0].Line != at || !strings.Contains(diags[0].Message, text) {
					t.Fatalf("want one diagnostic %q at line %d, got\n\t%s", text, at, describe(diags))
				}
				return
			}
			if !isSealed {
				if len(diags) != 0 {
					t.Fatalf("want clean (a program provides %s), got\n\t%s", key, describe(diags))
				}
				return
			}
			if len(diags) != 1 || diags[0].Code != "S109" || diags[0].Line != at || !strings.Contains(diags[0].Message, "#206") {
				t.Fatalf("want one S109 naming #206 at line %d, got\n\t%s", at, describe(diags))
			}
		})
	}
	for key := range sealedPrograms {
		if !sortedHas(types, key) {
			t.Errorf("sealedPrograms row %s: std declares no such operation type", key)
		}
	}
}

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

func sortedHas(xs []string, x string) bool {
	i := sort.SearchStrings(xs, x)
	return i < len(xs) && xs[i] == x
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
