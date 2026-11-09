package lower

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// law-run-1 (#162; types.md, Laws 10): ergo test runs every top-level law
// over generated values of its parameters, and every member law against
// each provider of its operation type that the package declares, with a
// generated value of the provider. Inside a member law the operation type is
// in the law's row and bound to the provider under test, so a helper that
// needs it runs (G-ex-b-13). A false law fails the run, naming the provider
// and the seed.

// lawRunB13 is the b13 repro of the examples wave with a provider: the law
// calls a helper that needs its own operation type. The law relates two
// reads, not a write and a read: under the value rule (owner q26) a
// provider's write to its own Map is not seen by the next call, and a
// provider whose state is a sync.Ref has no generator, so a write law has
// no generated provider to run against (batch-12 integration, V1 x value
// rule).
const lawRunB13 = `type Store {
	Count() int,
	Doubled() int,
	law PutCountsOne(n int) {
		twice() + n == Doubled() + n
	},
}

func twice() int needs Store { Store.Count() * 2 }

type Tally { counts Map[string, int] }

func Count(t Tally) int { len(t.counts) }

func Doubled(t Tally) int { 2 * len(t.counts) }

type Stuck { at int }

func Count(s Stuck) int { s.at }

func Doubled(s Stuck) int { 2*s.at + 1 }

func main() needs Console {
	Console.Println("x")
}
`

// TestLawRunB13: the b13 law checks and runs against both providers: it
// holds for Tally and fails for Stuck, whose report names Stuck, the seed
// and the case's provider value.
func TestLawRunB13(t *testing.T) {
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_b13", lawRunB13)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_b13"], "--seed", "7")
	got := lines(stdout)
	if code != 1 || len(got) != 3 {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	if got[0] != "ok law Store.PutCountsOne for Tally (100 cases)" {
		t.Errorf("line 1 is %q", got[0])
	}
	if !strings.HasPrefix(got[1], "FAIL law Store.PutCountsOne for Stuck (case 1 of 100, seed 7, size 0: provider = Stuck{at: ") ||
		!strings.HasSuffix(got[1], "law refuted at main.ergo:4:2: the law is false") {
		t.Errorf("line 2 is %q", got[1])
	}
	if got[2] != "FAIL (1 of 2 tests)" {
		t.Errorf("the summary is %q", got[2:])
	}
}

// lawRunState is integration finding F3's shape (fs4): a member law whose
// operations change the provider's state. Under the value rule the state
// lives in a sync.Ref, a std type that has no generator, so the providers
// are built by their constructors, one for each case: Tally keeps the law,
// Lossy breaks it, and Plain, with no constructor, is a skip that the
// summary counts.
const lawRunState = `import sync

type Counter {
	Add(n int),
	Total() int,
	law AddAdds(n int) {
		let before = Total()
		Add(n)
		Add(n)
		Total() == before + 2 * n
	},
}

type Tally { total sync.Ref[int] }

func NewTally() Tally { Tally{total: sync.NewRef(0)} }

func Add(t Tally, n int) { t.total.Update(func(v int) int { v + n }) }

func Total(t Tally) int { t.total.Get() }

type Lossy { total sync.Ref[int] }

func NewLossy(start int) Lossy { Lossy{total: sync.NewRef(start)} }

func Add(l Lossy, n int) { l.total.Set(n) }

func Total(l Lossy) int { l.total.Get() }

type Plain { total sync.Ref[int] }

func Add(p Plain, n int) { p.total.Set(p.total.Get() + n) }

func Total(p Plain) int { p.total.Get() }

func main() needs Console {
	Console.Println("\(NewTally().Total() + NewLossy(0).Total())")
}
`

// TestLawRunState: F3's law runs against each provider its constructor
// builds, a provider with no constructor and no generator is a skip line
// with the reason, and the summary counts the skip.
func TestLawRunState(t *testing.T) {
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_state", lawRunState)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_state"], "--seed", "7")
	got := lines(stdout)
	if code != 1 || len(got) != 4 {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	if got[1] != "ok law Counter.AddAdds for Tally (100 cases)" {
		t.Errorf("line 2 is %q", got[1])
	}
	if !strings.HasPrefix(got[2], "FAIL law Counter.AddAdds for Lossy (case ") || !strings.Contains(got[2], "provider = NewLossy(") ||
		!strings.HasSuffix(got[2], "law refuted at main.ergo:6:2: the law is false") {
		t.Errorf("line 3 is %q", got[2])
	}
	if want := "skip law Counter.AddAdds for Plain: its values have no generator: sync.Ref[int] is a standard library type; a func NewPlain() Plain with no rows would build one for each case"; got[0] != want {
		t.Errorf("line 1 is %q, want %q", got[0], want)
	}
	if got[3] != "FAIL (1 of 2 tests, 1 skipped)" {
		t.Errorf("the summary is %q", got[3])
	}
}

// lawRunStore and lawRunProviders are G-exup-1 and G-exup-3 in the layout
// AGENTS.md prescribes: the domain package kv declares the operation type
// Store with a member law and a top-level law that takes a Store, and the
// package that imports it provides Store. Last keeps GetAfterPut and
// breaks PutKeepsOthers, Zero breaks both, and Plain, with no generator
// and no constructor, is a skip.
const lawRunStore = `type Store {
	Put(k int, v int),
	Get(k int) int,
	law GetAfterPut(k int, v int) {
		Put(k, v)
		[]int{Get(k)} == []int{v}
	},
}

law PutKeepsOthers(s Store, k int, v int) {
	with context { Store: s } {
		Store.Put(k, 1)
		Store.Put(k + 1, v)
		Store.Get(k) == 1
	}
}
`

const lawRunProviders = `import kv
import sync

type Last { v sync.Ref[int] }

func NewLast() Last { Last{v: sync.NewRef(0)} }

func Put(l Last, k int, v int) { l.v.Set(v) }

func Get(l Last, k int) int { l.v.Get() }

type Zero { n int }

func Put(z Zero, k int, v int) {}

func Get(z Zero, k int) int { z.n }

type Plain { v sync.Ref[int] }

func Put(p Plain, k int, v int) { p.v.Set(v) }

func Get(p Plain, k int) int { p.v.Get() }

func main() needs Console {
	let s kv.Store = NewLast()
	with context { kv.Store: s } { Console.Println("\(kv.Store.Get(1))") }
}
`

// TestLawRunOtherPackage: the laws of an operation type run against each
// provider of it, in the package that declares the provider: the member
// law with the provider bound, the top-level law with the provider as its
// argument. A false law fails with its seed and the provider's build. The
// domain package's own test build names the laws it leaves to the
// packages that provide Store, so none is silent there.
func TestLawRunOtherPackage(t *testing.T) {
	kv := cxPackage(t, "kv", lawRunStore)
	imports := cxImporter{"kv": kv}
	f, err := syntax.Parse(lawRunProviders)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, imports)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	files, err := LowerTests(Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: lawRunProviders}}, Imports: imports.units()})
	if err != nil {
		t.Fatal(err)
	}
	kvFiles, err := LowerTests(kv)
	if err != nil {
		t.Fatal(err)
	}
	exes := buildAll(t, []*lowered{{name: "lawrun_xpkg", files: files}, {name: "lawrun_kv", files: kvFiles}}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_xpkg"], "--seed", "7")
	got := lines(stdout)
	if code != 1 || len(got) != 7 {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	const plain = ": its values have no generator: sync.Ref[int] is a standard library type; a func NewPlain() Plain with no rows would build one for each case"
	want := []struct{ line, prefix, suffix string }{
		{line: "skip law kv.Store.GetAfterPut for Plain" + plain},
		{line: "ok law kv.Store.GetAfterPut for Last (100 cases)"},
		{prefix: "FAIL law kv.Store.GetAfterPut for Zero (case ", suffix: "law refuted at lib.ergo:4:2: the law is false"},
		{line: "skip law kv.PutKeepsOthers with s = Plain" + plain},
		{prefix: "FAIL law kv.PutKeepsOthers with s = Last (case ", suffix: "law refuted at lib.ergo:10:1: the law is false"},
		{prefix: "FAIL law kv.PutKeepsOthers with s = Zero (case ", suffix: "law refuted at lib.ergo:10:1: the law is false"},
		{line: "FAIL (3 of 4 tests, 2 skipped)"},
	}
	for i, w := range want {
		switch {
		case w.line != "" && got[i] != w.line:
			t.Errorf("line %d is %q, want %q", i+1, got[i], w.line)
		case w.line == "" && (!strings.HasPrefix(got[i], w.prefix) || !strings.HasSuffix(got[i], w.suffix) || !strings.Contains(got[i], "seed 7")):
			t.Errorf("line %d is %q, want %q … seed 7 … %q", i+1, got[i], w.prefix, w.suffix)
		}
	}
	if !strings.Contains(got[4], "s = NewLast()") || !strings.Contains(got[2], "provider = Zero{n: ") {
		t.Errorf("a failure does not show the provider's build:\n%s\n%s", got[2], got[4])
	}
	stdout, stderr, code = runExe(t, exes["lawrun_kv"])
	wantKV := []string{
		"law Store.GetAfterPut runs where Store has a provider: none is in this package or its imports",
		"law PutKeepsOthers runs where Store has a provider: none is in this package or its imports",
		"PASS (0 tests)",
	}
	if code != 0 || !slices.Equal(lines(stdout), wantKV) {
		t.Errorf("kv: exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(wantKV, "\n"), firstLines(stderr, 8))
	}
}

// lawRunMem provides kv.Store without importing kv: its types implement
// Store by their operations alone, so only a package that imports both
// sees the law and its providers.
const lawRunMem = `import sync

type Last { v sync.Ref[int] }

func NewLast() Last { Last{v: sync.NewRef(0)} }

func Put(l Last, k int, v int) { l.v.Set(v) }

func Get(l Last, k int) int { l.v.Get() }

type Zero { n int }

func Put(z Zero, k int, v int) {}

func Get(z Zero, k int) int { z.n }
`

const lawRunBinder = `import kv
import mem

func main() needs Console {
	let s kv.Store = mem.NewLast()
	with context { kv.Store: s } { Console.Println("\(kv.Store.Get(1))") }
}
`

// TestLawRunLeastPackage: a law whose operation type and provider are in
// two packages that do not import each other runs in the package that
// imports both, the least that sees them, against providers it does not
// declare; the provider's own package does not see the law and runs none.
func TestLawRunLeastPackage(t *testing.T) {
	kv, mem := cxPackage(t, "kv", lawRunStore), cxPackage(t, "mem", lawRunMem)
	imports := cxImporter{"kv": kv, "mem": mem}
	f, err := syntax.Parse(lawRunBinder)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, imports)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	files, err := LowerTests(Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: lawRunBinder}}, Imports: imports.units()})
	if err != nil {
		t.Fatal(err)
	}
	memFiles, err := LowerTests(mem)
	if err != nil {
		t.Fatal(err)
	}
	exes := buildAll(t, []*lowered{{name: "lawrun_least", files: files}, {name: "lawrun_mem", files: memFiles}}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_least"], "--seed", "7")
	got := lines(stdout)
	want := []string{
		"ok law kv.Store.GetAfterPut for mem.Last (100 cases)",
		"FAIL law kv.Store.GetAfterPut for mem.Zero (case ",
		"FAIL law kv.PutKeepsOthers with s = mem.Last (case ",
		"FAIL law kv.PutKeepsOthers with s = mem.Zero (case ",
		"FAIL (3 of 4 tests)",
	}
	if code != 1 || len(got) != len(want) {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) || strings.HasPrefix(w, "FAIL law") && !strings.Contains(got[i], "seed 7") {
			t.Errorf("line %d is %q, want %q …", i+1, got[i], w)
		}
	}
	stdout, _, code = runExe(t, exes["lawrun_mem"])
	if code != 0 || !slices.Equal(lines(stdout), []string{"PASS (0 tests)"}) {
		t.Errorf("mem: exit %d, stdout:\n%s", code, stdout)
	}
}

// TestLawRunSkips: a law that cannot run says why, and the summary counts
// it as skipped, never as a test (fs4).
func TestLawRunSkips(t *testing.T) {
	src := `type Shape {
	Area() int,
	law Positive() { Area() >= 0 },
}

type Box[T] {
	Put(v T),
	law Keeps(v T) { true },
}

type Runner[E] {
	Run() fails E,
	law Runs() { true },
}

// law-exempt: a constraint.
type Named {
	Name() string,
}

type Ranked[T Named] {
	Rank(v T) int,
	law Same(v T) { Rank(v) == Rank(v) },
}

type Fn { f func(int) int }

func Area(_ Fn) int { 1 }

type Fns {}

func Put(_ Fns, v func(int) int) {}

law Calls(f func(int) int) { f(1) == f(1) }

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_skips", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_skips"])
	want := []string{
		"skip law Shape.Positive for Fn: its values have no generator: func(int) int is a function type; a func NewFn() Fn with no rows would build one for each case",
		"skip law Box.Keeps for Fns: its parameter v has no generator at Box[func(int) int]: func(int) int is a function type",
		"skip law Runner.Runs: its operation type Runner has the row parameter E, and a run binds only type parameters",
		"skip law Ranked.Same: its operation type Ranked constrains T by operations, and a run passes no witness",
		"skip law Calls: its parameter f has no generator: func(int) int is a function type",
		"PASS (0 tests, 5 skipped)",
	}
	if code != 0 || !slices.Equal(lines(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}

// TestLawRunDefinedBool: a law's result may be of a type over bool, at the
// tail and at a return (types.md, Laws 8); its run converts it.
func TestLawRunDefinedBool(t *testing.T) {
	src := `type Flag bool

func yes() Flag { Flag(true) }

law FlagTail(n int) { yes() }

law FlagReturn(n int) {
	if n > 0 {
		return yes()
	}
	true
}

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_flag", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_flag"])
	want := []string{"ok law FlagTail (100 cases)", "ok law FlagReturn (100 cases)", "PASS (2 tests)"}
	if code != 0 || !slices.Equal(lines(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
}

// TestLawRunNoParams: a top-level law with no parameters draws nothing and
// runs once, as an example test does; a false one fails the run.
func TestLawRunNoParams(t *testing.T) {
	src := `law Holds() { 1 + 1 == 2 }

law Breaks() { 1 + 1 == 3 }

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_noparams", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_noparams"])
	want := []string{"ok law Holds", "FAIL law Breaks: law refuted at main.ergo:3:1: the law is false", "FAIL (1 of 2 tests)"}
	if code != 1 || !slices.Equal(lines(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}

// lawRunProgram is a generated program of the property below: one
// operation type with a member law and 1-3 providers, of which at most one
// is broken: it breaks the law for n above a threshold, so it fails at some
// case. Half the laws reach the operation through a helper that needs the
// type (the b13 binding); each program also has a top-level law, true or
// false. want are the lines the test binary prints, in order, and exit its
// status.
type lawRunProgram struct {
	name, src string
	generic   bool // Op[T], run at each provider's instance
	want      []string
	exit      int
}

func genLawRunProgram(r *rand.Rand, i int) lawRunProgram {
	var b strings.Builder
	k, c := r.Intn(5)+1, r.Intn(7)-3
	helper := r.Intn(2) == 0
	call := "F(n)"
	if helper {
		call = "viaHelper(n)"
	}
	// A generic Op[T] (L4) runs its law at each provider's instance: a
	// provider's F fixes T, to int or int64. Its law calls F bare: a
	// helper would need Op[T], which is no capability. A literal does not
	// take a type parameter's type, so the law states affinity without
	// one: f(2n) = 2f(n) - f(0), which the broken provider's step at its
	// threshold breaks for n past it (threshold >= 0) or below it (< 0).
	generic := !helper && r.Intn(2) == 0
	if generic {
		b.WriteString("type Op[T number] {\n\tF(n T) T,\n\tlaw Linear(n T) { F(n + n) == F(n) + F(n) - F(n - n) },\n}\n\n")
	} else {
		fmt.Fprintf(&b, "type Op {\n\tF(n int) int,\n\tlaw Linear(n int) { %s == n * %d + %d },\n}\n\n", call, k, c)
	}
	if helper {
		b.WriteString("func viaHelper(n int) int needs Op { Op.F(n) }\n\n")
	}
	providers := r.Intn(3) + 1
	broken := r.Intn(providers + 1) // == providers: none is broken
	var want []string
	failed := 0
	for p := 0; p < providers; p++ {
		name := fmt.Sprintf("P%d", p)
		body := fmt.Sprintf("n * %d + %d", k, c)
		if p == broken {
			body = fmt.Sprintf("if n > %d { n * %d + %d } else { n * %d + %d }", r.Intn(20)-10, k, c+1, k, c)
		}
		elem := "int"
		if generic && r.Intn(2) == 0 {
			elem = "int64"
		}
		fmt.Fprintf(&b, "type %s { tag int }\n\nfunc F(_ %s, n %s) %s { %s }\n\n", name, name, elem, elem, body)
		line := "ok law Op.Linear for " + name + " (100 cases)"
		if p == broken {
			line = "FAIL law Op.Linear for " + name + " (case "
			failed++
		}
		want = append(want, line)
	}
	top := r.Intn(2) == 0
	if top {
		b.WriteString("law Top(a int, b int) { a + b == b + a }\n\n")
		want = append(want, "ok law Top (100 cases)")
	} else {
		b.WriteString("law Top(a int, b int) { a - b == b - a }\n\n")
		want = append(want, "FAIL law Top (case ")
		failed++
	}
	b.WriteString("func main() {}\n")
	total := providers + 1
	exit := 0
	if failed > 0 {
		exit = 1
		want = append(want, fmt.Sprintf("FAIL (%d of %d tests)", failed, total))
	} else {
		want = append(want, fmt.Sprintf("PASS (%d tests)", total))
	}
	return lawRunProgram{name: fmt.Sprintf("lawrun_gen_%02d", i), src: b.String(), want: want, exit: exit, generic: generic}
}

// TestLawRunFindsTheBrokenProvider is the property: over generated
// operation types with 1-3 providers, one of them broken at random (or
// none), exactly the broken provider fails, with its name and the seed, and
// the top-level law runs as well. A failure prints the program's source.
// ERGO_LAWRUN_SEED replays another seed.
func TestLawRunFindsTheBrokenProvider(t *testing.T) {
	seed := int64(162)
	if s := os.Getenv("ERGO_LAWRUN_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_LAWRUN_SEED: %v", err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	var progs []lawRunProgram
	var builds []*lowered
	brokenAt := map[int]bool{}
	genericBroken := false
	for i := 0; i < 32; i++ {
		p := genLawRunProgram(r, i)
		progs = append(progs, p)
		builds = append(builds, lowerTestBuild(t, p.name, p.src))
		for k, w := range p.want {
			if strings.HasPrefix(w, "FAIL law Op.") {
				brokenAt[k] = true
				genericBroken = genericBroken || p.generic
			}
		}
	}
	// The generator reaches a broken provider in every position, and a
	// broken provider of a generic Op[T].
	for k := 0; k < 3; k++ {
		if !brokenAt[k] {
			t.Fatalf("seed %d: no program has a broken provider at position %d", seed, k+1)
		}
	}
	if !genericBroken {
		t.Fatalf("seed %d: no program has a broken provider of a generic Op", seed)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		stdout, stderr, code := runExe(t, exes[p.name])
		got := lines(stdout)
		ok := code == p.exit && len(got) == len(p.want)
		for k := 0; ok && k < len(got); k++ {
			w := p.want[k]
			switch {
			case strings.HasSuffix(w, "(case "):
				ok = strings.HasPrefix(got[k], w) && strings.Contains(got[k], ", seed 1, ") && strings.HasSuffix(got[k], ": the law is false")
			default:
				ok = got[k] == w
			}
		}
		if !ok {
			t.Errorf("seed %d, %s: exit %d (want %d)\nstdout:\n%s\nwant:\n%s\nstderr:\n%s\nsource:\n%s", seed, p.name, code, p.exit, stdout, strings.Join(p.want, "\n"), firstLines(stderr, 8), p.src)
		}
	}
}

// lawRunFiles provides fs.Files four ways. Drifty's reads of a path both
// fail NotFound, the second naming another path; Flaky's alternate
// NotFound and "x"; Shifty's alternate Denied and NotFound; so none keeps
// fs.Files.ReadIsRepeatable. Steady fails the empty path with NotFound
// every time and reads "x" otherwise, so it keeps it, its failures
// included (batch-12 M2, M2a).
const lawRunFiles = `import fs
import sync

type Drifty { reads sync.Ref[int] }

func NewDrifty() Drifty { Drifty{reads: sync.NewRef[int](0)} }

func ReadFile(f Drifty, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	let n = f.reads.Get()
	f.reads.Set(n + 1)
	if n % 2 == 0 {
		return fs.NotFound{Path: path}
	}
	fs.NotFound{Path: path + "/other"}
}

func ReadDir(f Drifty, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

func Stat(f Drifty, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

type Flaky { reads sync.Ref[int] }

func NewFlaky() Flaky { Flaky{reads: sync.NewRef[int](0)} }

func ReadFile(f Flaky, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	let n = f.reads.Get()
	f.reads.Set(n + 1)
	if n % 2 == 0 {
		return fs.NotFound{Path: path}
	}
	"x"
}

func ReadDir(f Flaky, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

func Stat(f Flaky, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

type Shifty { reads sync.Ref[int] }

func NewShifty() Shifty { Shifty{reads: sync.NewRef[int](0)} }

func ReadFile(s Shifty, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	let n = s.reads.Get()
	s.reads.Set(n + 1)
	if n % 2 == 0 {
		return fs.Denied{Path: path}
	}
	fs.NotFound{Path: path}
}

func ReadDir(s Shifty, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

func Stat(s Shifty, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

type Steady {}

func ReadFile(s Steady, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	if path == "" {
		return fs.NotFound{Path: path}
	}
	"x"
}

func ReadDir(s Steady, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

func Stat(s Steady, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

func main() {}
`

// TestLawRunReadIsRepeatable: the std law fs.Files.ReadIsRepeatable
// compares both reads' outcomes, so it refutes a provider whose reads
// alternate a failure and a text (Flaky), two failure kinds (Shifty) or
// one kind with other fields (Drifty), and holds for a steady one.
// fstest.Memory and the disk keep it in TestFSMemoryAgreesWithHost and
// TestStdHostLawsHold. Mutants it kills: a first failed read that holds
// the law vacuously (Flaky passes), a second failure that is not compared
// with the first (Shifty passes), and a same-kind arm that compares no
// field (Drifty passes). It
// builds Go because the law runner, its provider builds and its seeded
// cases exist only in the lowered test binary, and Flaky's state is a
// sync.Ref that only the runtime runs; no in-process interpreter runs a
// member law against a provider.
func TestLawRunReadIsRepeatable(t *testing.T) {
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_files", lawRunFiles)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_files"], "--seed", "7")
	got := lines(stdout)
	want := []string{
		"FAIL law fs.Files.ReadIsRepeatable for Drifty (case ",
		"FAIL law fs.Files.ReadIsRepeatable for Flaky (case ",
		"FAIL law fs.Files.ReadIsRepeatable for Shifty (case ",
		"ok law fs.Files.ReadIsRepeatable for Steady (",
		"FAIL (3 of 4 tests)",
	}
	if code != 1 || len(got) != len(want) {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) || strings.HasPrefix(w, "FAIL law") && !strings.Contains(got[i], "seed 7") {
			t.Errorf("line %d is %q, want %q …", i+1, got[i], w)
		}
	}
}
