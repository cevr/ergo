package lower

import (
	"fmt"
	"strings"
	"testing"
)

// Round 1 verify of 196b U1 (pkg-lower): keys and locations in a program
// of several files and packages. A syntax position is an offset into one
// file, and a display string names a package by its last path element, so
// neither is an identity once a program has two files or two packages
// whose paths end alike. The cases below are the verifier's probes and the
// properties that generalise them.

// buildAndRun builds the programs and checks each one's stdout and exit 0.
func buildAndRun(t *testing.T, programs []*lowered, want map[string]string) {
	t.Helper()
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the lowered programs do not build")
	}
	for _, p := range programs {
		out, stderr, code := runExe(t, exes[p.name])
		if out != want[p.name] || code != 0 {
			t.Errorf("%s: exit %d, stdout %q, want %q; stderr:\n%s", p.name, code, out, want[p.name], stderr)
		}
	}
}

func TestVerifyR1GenericNames(t *testing.T) {
	lib := libraryUnit(t)
	root := checkPackage(t, "main", []pkgSource{
		{"/probe/names/a.ergo", `import lib
func main() needs Console { Console.Println("\(First([]string{}, "root")) \(lib.First([]string{}, "lib")) \(Make(2).N) \(lib.Make(2).N) \(Limit)") }
`},
		{"/probe/names/b.ergo", "type Q { N int }\nfunc Make(n int) Q { Q{N: n} }\nfunc First[T](xs []T, d T) T { d }\n"},
		{"/probe/names/c.ergo", "let Limit = 9\n"},
	}, pkgImporter{"lib": lib.Package})
	root.Imports = []Unit{lib}
	p := mustLowerUnit(t, "genericnames", root, false)
	buildAndRun(t, []*lowered{p}, map[string]string{"genericnames": "root lib 2 8 9\n"})
}

// All 64 forward-edge DAGs on four ranks, with path order opposite rank.
// Each package holds a machine with a value initial, and a let unless the
// mask leaves it out (mask%5 names one package, and 4 leaves out every
// let). The root's main names the machines in path order, the order Go's
// initialization does not follow, and names no let when there is none; the
// root has a machine of its own at an even mask, which the lowering meets
// first.
// Each emitted initializer appears once, in the Go-spec scheduler's order:
// the lets first, then the machines', each package's in that order, the
// root's last, and
// the entry calls ergoInit before main's first statement (F13: a library's
// machine was lowered after the entry, which then did not call ergoInit).
func TestVerifyR1InitDAGs(t *testing.T) {
	for mask := 0; mask < 64; mask++ {
		imp := pkgImporter{}
		var units []Unit
		deps := [4][]int{}
		edge := 0
		reads := []string{}
		for i := 0; i < 4; i++ {
			path := fmt.Sprintf("p%d", 3-i)
			imports, expr := "", "1"
			for j := 0; j < i; j++ {
				if mask&(1<<edge) != 0 {
					deps[i] = append(deps[i], j)
					imports += fmt.Sprintf("import p%d\n", 3-j)
					expr += fmt.Sprintf(" + p%d.Read()", 3-j)
				}
				edge++
			}
			src := imports + fmt.Sprintf("func start() int { %s }\n", expr)
			if mask%5 == 4 || mask%5 == i {
				src += "func Read() int { start() }\n"
			} else {
				src += fmt.Sprintf("let V%d = start()\nfunc Read() int { V%d }\n", i, i)
				reads = append(reads, fmt.Sprintf("%s.V%d", path, i))
			}
			src += fmt.Sprintf("type S = A{N int}\ntype Sig = Go{}\nfunc open() S { A{N: Read()} }\nfunc step(s S, e Sig) Decision[S, int] { Enter[S]{Next: s} }\ntype M%d machine[S, Sig] { initial: open(), step: step }\n", i)
			u := checkPackage(t, path, []pkgSource{{"/dag/" + path + ".ergo", src}}, imp)
			imp[path] = u.Package
			units = append(units, u)
		}
		sum := "0"
		if len(reads) > 0 {
			sum = strings.Join(reads, " + ")
		}
		main := "import p0\nimport p1\nimport p2\nimport p3\nfunc main() needs Console {\n\tlet _ = p0.M3{}\n\tlet _ = p1.M2{}\n\tlet _ = p2.M1{}\n\tlet _ = p3.M0{}\n\tConsole.Println(\"\\(" + sum + ")\")\n}\n"
		if mask%2 == 0 {
			main += "type R = Root{N int}\nfunc stepR(s R, e p0.Sig) Decision[R, int] { Enter[R]{Next: s} }\ntype MR machine[R, p0.Sig] { initial: Root{N: p3.Read()}, step: stepR }\n"
		}
		root := checkPackage(t, "main", []pkgSource{{"/dag/main.ergo", main}}, imp)
		root.Imports = units
		code := mustLowerUnit(t, "dag", root, false).mainGo()
		entry := code[strings.Index(code, "func main() {"):]
		if call := strings.Index(entry, "ergoInit("); call < 0 || call > strings.Index(entry, "\n}\n") {
			t.Fatalf("mask %d: the entry does not call ergoInit:\n%s", mask, code)
		}
		var order []int
		done := [4]bool{}
		last := -1
		for step := 0; step < 4; step++ {
			next := -1
			for i := 3; i >= 0; i-- {
				if done[i] {
					continue
				}
				ready := true
				for _, d := range deps[i] {
					if !done[d] {
						ready = false
					}
				}
				if ready {
					next = i
					break
				}
			}
			done[next] = true
			order = append(order, next)
		}
		var tokens []string
		for _, i := range order {
			if mask%5 != 4 && mask%5 != i {
				tokens = append(tokens, fmt.Sprintf("V%d = ", i))
			}
		}
		for _, i := range order {
			tokens = append(tokens, fmt.Sprintf("ergoInitialM%d = ", i))
		}
		if mask%2 == 0 {
			tokens = append(tokens, "ergoInitialMR = ")
		}
		for step, token := range tokens {
			pos := strings.Index(code, token)
			if pos < last || pos < 0 || strings.Count(code, token) != 1 {
				t.Fatalf("mask %d step %d: %q at %d after %d; count %d\n%s", mask, step, token, pos, last, strings.Count(code, token), code)
			}
			last = pos
		}
	}
}

func TestVerifyR1Diamond(t *testing.T) {
	leaf := checkPackage(t, "zleaf", []pkgSource{{"/probe/leaf.ergo", "func start() int { 3 }\nlet N = start()\nfunc Read() int { N }\n"}}, nil)
	imp := pkgImporter{"zleaf": leaf.Package}
	left := checkPackage(t, "aleft", []pkgSource{{"/probe/left.ergo", "import zleaf\nfunc start() int { zleaf.Read() + 1 }\nlet N = start()\n"}}, imp)
	right := checkPackage(t, "bright", []pkgSource{{"/probe/right.ergo", "import zleaf\nfunc start() int { zleaf.Read() + 2 }\nlet N = start()\n"}}, imp)
	imp["aleft"], imp["bright"] = left.Package, right.Package
	root := checkPackage(t, "main", []pkgSource{
		{"/probe/a.ergo", `import aleft
import bright
import zleaf
let N = aleft.N + bright.N
func main() needs Console { Console.Println("\(N) \(fromB()) \(zleaf.N)") }
func fromA() int { 7 }
`},
		{"/probe/b.ergo", "func fromB() int { fromC() + fromA() }\n"},
		{"/probe/c.ergo", "func fromC() int { fromA() + 1 }\n"},
	}, imp)
	root.Imports = []Unit{right, leaf, left}
	p := mustLowerUnit(t, "diamond", root, false)
	buildAndRun(t, []*lowered{p}, map[string]string{"diamond": "9 15 3\n"})
}

func TestVerifyR1ImportedRefusal(t *testing.T) {
	lib := checkPackage(t, "lib", []pkgSource{{"/probe/lib/a.ergo", "func X() []int { []int{2: 5} }\n"}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/probe/main.ergo", "import lib\nfunc main() { let _ = lib.X() }\n"}}, pkgImporter{"lib": lib.Package})
	root.Imports = []Unit{lib}
	o := lowerUnit("ref", root, false)
	if o.err != nil || o.panicked != nil || len(o.refused) != 1 || o.refused[0].Path != "/probe/lib/a.ergo" || o.refused[0].Source != 1 {
		t.Fatalf("err %v panic %v refusals %+v", o.err, o.panicked, o.refused)
	}
}

// M1: a test's failure report names the file of the test, not the first
// file. Each test, and each assert, sits in its own file, at the same line
// and column in each, so only the file can tell them apart.
func TestVerifyR1TestFailureFile(t *testing.T) {
	root := checkPackage(t, "main", []pkgSource{
		{"/probe/a.ergo", "func main() {}\n"},
		{"/probe/b.ergo", "type Bad {}\nfunc bad() fails Bad { Bad }\ntest Later { try bad() }\n"},
	}, nil)
	p := mustLowerUnit(t, "testfile", root, true)
	exes := buildAll(t, []*lowered{p}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	out, stderr, _ := runExe(t, exes[p.name])
	if !strings.Contains(out+stderr, "b.ergo:3:") || strings.Contains(out+stderr, "a.ergo:") {
		t.Fatalf("wrong test failure location: %s%s", out, stderr)
	}
}

func TestTestReportsNameTheirFiles(t *testing.T) {
	files := []pkgSource{{"/probe/reports/main.ergo", "func main() {}\n"}}
	var want []string
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("f%d", i)
		files = append(files, pkgSource{"/probe/reports/" + name + ".ergo", fmt.Sprintf(
			"type Bad%d {}\nfunc bad%d() fails Bad%d { Bad%d }\ntest Escape%d { try bad%d() }\ntest Assert%d { assert %d == 9 }\n", i, i, i, i, i, i, i, i)})
		want = append(want,
			fmt.Sprintf("FAIL Escape%d: a failure escaped the test at %s.ergo:3:1", i, name),
			fmt.Sprintf("FAIL Assert%d: assert failed at %s.ergo:4:", i, name))
	}
	// Each permutation of the files' order, the main file first.
	for _, order := range [][]int{{1, 2, 3}, {3, 1, 2}, {2, 3, 1}} {
		srcs := []pkgSource{files[0]}
		for _, i := range order {
			srcs = append(srcs, files[i])
		}
		p := mustLowerUnit(t, "reports", checkPackage(t, "main", srcs, nil), true)
		exes := buildAll(t, []*lowered{p}, true)
		if exes == nil {
			t.Fatal("the lowered program does not build")
		}
		out, _, _ := runExe(t, exes[p.name])
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("order %v: the report lacks %q:\n%s", order, w, out)
			}
		}
	}
}

// opsLib is a user package that exports the operation type Read, the
// record Q with the Get operation of Read, and Make, which converts a Q
// into a Read.
func opsLib(n string) string {
	return "type Read { Get() int }\ntype Q { N int }\nfunc Get(q Q) int { q.N }\nfunc Make() Read { Q{N: " + n + "} }\n"
}

// M2 and M3: two packages whose paths end alike (x/lib, y/lib), each with
// the same declarations; the root names a type lib too.
func TestVerifyR1PackageAdapterNames(t *testing.T) {
	a := checkPackage(t, "x/lib", []pkgSource{{"/probe/x/lib.ergo", opsLib("1")}}, nil)
	b := checkPackage(t, "y/lib", []pkgSource{{"/probe/y/lib.ergo", opsLib("2")}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/probe/adapters.ergo", `import a x/lib
import b y/lib
type lib {}
func main() needs Console { Console.Println("\(a.Make().Get()) \(b.Make().Get())") }
`}}, pkgImporter{"x/lib": a.Package, "y/lib": b.Package})
	root.Imports = []Unit{a, b}
	p := mustLowerUnit(t, "adapters", root, false)
	buildAndRun(t, []*lowered{p}, map[string]string{"adapters": "1 2\n"})
}

func TestVerifyR1AdapterCollision(t *testing.T) {
	a := checkPackage(t, "x/lib", []pkgSource{{"/probe/x/lib.ergo", opsLib("1")}}, nil)
	b := checkPackage(t, "y/lib", []pkgSource{{"/probe/y/lib.ergo", opsLib("2")}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/probe/adapters.ergo", `import a x/lib
import b y/lib
type lib {}
func main() needs Console { let _ = a.Make(); let _ = b.Make(); Console.Println("ok") }
`}}, pkgImporter{"x/lib": a.Package, "y/lib": b.Package})
	root.Imports = []Unit{a, b}
	p := mustLowerUnit(t, "adaptercollision", root, false)
	buildAndRun(t, []*lowered{p}, map[string]string{"adaptercollision": "ok\n"})
}

// The text of an error type's instances over two packages' Q, whose
// displays are both Oops[lib.Q]: each instance has its own text function.
func TestErrorTextInstancesAcrossPackages(t *testing.T) {
	a := checkPackage(t, "x/lib", []pkgSource{{"/probe/x/lib.ergo", opsLib("1")}}, nil)
	b := checkPackage(t, "y/lib", []pkgSource{{"/probe/y/lib.ergo", "type Q { M string }\n"}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/probe/text.ergo", `import a x/lib
import b y/lib
type lib {}
type Oops[T] { v T }
func f() fails Oops[a.Q] { Oops[a.Q]{v: a.Q{N: 1}} }
func g() fails Oops[b.Q] { Oops[b.Q]{v: b.Q{M: "two"}} }
func main() {}
test F { try f() }
test G { try g() }
`}}, pkgImporter{"x/lib": a.Package, "y/lib": b.Package})
	root.Imports = []Unit{a, b}
	p := mustLowerUnit(t, "errtext", root, true)
	exes := buildAll(t, []*lowered{p}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	out, stderr, _ := runExe(t, exes[p.name])
	if !strings.Contains(out, "Oops{v:{N:1}}") || !strings.Contains(out, "Oops{v:{M:two}}") {
		t.Fatalf("the reports do not show both instances:\n%s%s", out, stderr)
	}
}

// Imported operation types reached every way the root can reach them: the
// value an imported function returns, a parameter of the imported type, a
// conversion the root writes from an imported record, and a pointer to an
// imported record, from packages whose paths share their last element
// (1 to 3 of them), with the same type, function and parameter names in
// each. Each package k provides k.
func TestImportedOperationTypes(t *testing.T) {
	paths := []string{"x/lib", "y/lib", "z/lib"}
	aliases := []string{"a", "b", "c"}
	src := func(k int) string {
		return opsLib(fmt.Sprint(k)) + `func Show(r Read) int { r.Get() }
func First[T](xs []T, d T) T { d }
type Bump { Inc() int }
func Inc(q *Q) int { q.N + 10 }
`
	}
	var programs []*lowered
	want := map[string]string{}
	for n := 1; n <= len(paths); n++ {
		imp := pkgImporter{}
		var units []Unit
		imports, header, body, expect := "", "", "", ""
		for k := 0; k < n; k++ {
			u := checkPackage(t, paths[k], []pkgSource{{"/probe/" + paths[k] + ".ergo", src(k + 1)}}, nil)
			imp[paths[k]] = u.Package
			units = append(units, u)
			x := aliases[k]
			imports += "import " + x + " " + paths[k] + "\n"
			body += fmt.Sprintf("\tlet mut q%[1]s = %[1]s.Q{N: %[2]d}\n", x, k+1)
			body += fmt.Sprintf("\tConsole.Println(\"\\(%[1]s.Make().Get()) \\(use%[1]s(%[1]s.Make())) \\(%[1]s.Show(%[1]s.Q{N: %[2]d})) \\(%[1]s.First([]int{}, %[2]d)) \\(bump%[1]s(&q%[1]s))\")\n", x, k+1)
			header += fmt.Sprintf("func use%[1]s(r %[1]s.Read) int { r.Get() }\nfunc bump%[1]s(b %[1]s.Bump) int { b.Inc() }\n", x)
			expect += strings.Repeat(fmt.Sprint(k+1)+" ", 4) + fmt.Sprint(k+11) + "\n"
		}
		root := checkPackage(t, "main", []pkgSource{{"/probe/ops.ergo", imports + header + "type lib {}\nfunc main() needs Console {\n" + body + "}\n"}}, imp)
		root.Imports = units
		name := fmt.Sprintf("ops%d", n)
		programs = append(programs, mustLowerUnit(t, name, root, false))
		want[name] = expect
	}
	buildAndRun(t, programs, want)
}

// B1: a with's builder is keyed by its acquisitions' identity, not their
// positions. Each provider file declares its capability, provider and
// context at the same offsets; W embeds them all, and each with acquires
// another subset of them. Every order of the files is lowered.
func TestContextAcquisitionsAcrossFiles(t *testing.T) {
	provider := func(i int) pkgSource {
		return pkgSource{fmt.Sprintf("/probe/ctx/p%d.ergo", i), fmt.Sprintf(
			"type C%[1]d { Get() int }\ntype P%[1]d {}\nfunc Get(x P%[1]d) int { %[2]d }\nlet X%[1]d = context { C%[1]d: P%[1]d }\nfunc use%[1]d() int needs C%[1]d { C%[1]d.Get() }\n", i, i+1)}
	}
	subsets := [][]int{{1}, {0}, {2}, {0, 2}, {1, 2}, {0, 1, 2}, {2, 0}}
	var body, expect strings.Builder
	for k, s := range subsets {
		var calls []string
		sum := 0
		for _, i := range s {
			calls = append(calls, fmt.Sprintf("use%d()", i))
			sum += i + 1
		}
		fmt.Fprintf(&body, "\tlet s%d = with W { %s }\n", k, strings.Join(calls, " + "))
		fmt.Fprintf(&expect, "%d ", sum)
	}
	var shown []string
	for k := range subsets {
		shown = append(shown, fmt.Sprintf("\\(s%d)", k))
	}
	main := pkgSource{"/probe/ctx/main.ergo", "let W = context { X0, X1, X2 }\nfunc main() needs Console {\n" + body.String() + "\tConsole.Println(\"" + strings.Join(shown, " ") + "\")\n}\n"}
	var programs []*lowered
	want := map[string]string{}
	for k, order := range [][]int{{0, 1, 2}, {2, 0, 1}, {1, 2, 0}} {
		srcs := []pkgSource{main}
		for _, i := range order {
			srcs = append(srcs, provider(i))
		}
		name := fmt.Sprintf("ctx%d", k)
		programs = append(programs, mustLowerUnit(t, name, checkPackage(t, "main", srcs, nil), false))
		want[name] = strings.TrimSuffix(expect.String(), " ") + "\n"
	}
	buildAndRun(t, programs, want)
}

func TestVerifyR1ContextPositions(t *testing.T) {
	root := checkPackage(t, "main", []pkgSource{
		{"/probe/a.ergo", "type A { Get() int }\ntype One {}\nfunc Get(x One) int { 1 }\nlet X = context { A: One }\nfunc useA() int needs A { A.Get() }\n"},
		{"/probe/b.ergo", "type B { Get() int }\ntype Two {}\nfunc Get(x Two) int { 2 }\nlet Y = context { B: Two }\nfunc useB() int needs B { B.Get() }\n"},
		{"/probe/c.ergo", `let W = context { X, Y }
func main() needs Console {
	let a = with W { useA() }
	let b = with W { useB() }
	Console.Println("\(a) \(b)")
}
`}}, nil)
	p := mustLowerUnit(t, "contexts", root, false)
	buildAndRun(t, []*lowered{p}, map[string]string{"contexts": "1 2\n"})
}
