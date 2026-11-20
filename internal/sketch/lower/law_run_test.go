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
// provider's write to its own Map is not seen by the next call, and at b13
// a provider whose state is a sync.Ref had no generator, so a write law had
// no generated provider to run against (batch-12 integration, V1 x value
// rule; fu7a builds a sync.Ref with sync.NewRef).
const lawRunB13 = `type Store {
	Count() int,
	Doubled() int,
	law PutCountsOne(n int) {
		twice() + n == Doubled() + n
	},
}

func twice() int needs Store { Store.Count() * 2 }

type Tally { counts Map[string, int] } implements Store

func Count(t Tally) int { len(t.counts) }

func Doubled(t Tally) int { 2 * len(t.counts) }

type Stuck { at int } implements Store

func Count(s Stuck) int { s.at }

func Doubled(s Stuck) int { 2*s.at + 1 }

func main() needs Console {
	Console.Println("x")
}
`

// TestLawRunB13: the b13 law checks and runs against both providers: it
// holds for Tally and fails for Stuck, whose report names Stuck, the seed
// and the case's provider value.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestLawRunB13(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "lawrun_b13")
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_b13"], "--seed", "7")
	got := verdicts(stdout)
	if code != 1 || len(got) != 3 {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	if got[0] != "ok law Store.PutCountsOne for Tally (100 cases)" {
		t.Errorf("line 1 is %q", got[0])
	}
	if !strings.HasPrefix(got[1], "FAIL law Store.PutCountsOne for Stuck (case 1 of 100, seed 7, size 0: provider = Stuck{at: ") ||
		!strings.HasSuffix(got[1], "law refuted at main.ergo:5:3: the law is false") {
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

type Tally { total sync.Ref[int] } implements Counter

func NewTally() Tally { Tally{total: sync.NewRef(0)} }

func Add(t Tally, n int) { t.total.Update(func(v int) int { v + n }) }

func Total(t Tally) int { t.total.Get() }

type Lossy { total sync.Ref[int] } implements Counter

func NewLossy(start int) Lossy { Lossy{total: sync.NewRef(start)} }

func Add(l Lossy, n int) { l.total.Set(n) }

func Total(l Lossy) int { l.total.Get() }

type Plain { total sync.Ref[int], hook func(int) int } implements Counter

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
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_state", lawRunState)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_state"], "--seed", "7")
	got := verdicts(stdout)
	if code != 1 || len(got) != 4 {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	if got[1] != "ok law Counter.AddAdds for Tally (100 cases)" {
		t.Errorf("line 2 is %q", got[1])
	}
	if !strings.HasPrefix(got[2], "FAIL law Counter.AddAdds for Lossy (case ") || !strings.Contains(got[2], "provider = NewLossy(") ||
		!strings.HasSuffix(got[2], "law refuted at main.ergo:10:3: the law is false") {
		t.Errorf("line 3 is %q", got[2])
	}
	if want := "skip law Counter.AddAdds for Plain: its values have no generator: func(int) int is a function type; bind one in a named context of a test file, let Name = context { Counter: … }, and the law runs under it"; got[0] != want {
		t.Errorf("line 1 is %q, want %q", got[0], want)
	}
	if got[3] != "FAIL (1 of 2 tests, 1 skipped)" {
		t.Errorf("the summary is %q", got[3])
	}
}

// lawRunStore and lawRunProviders are G-exup-1 and G-exup-3 in the layout
// AGENTS.md prescribes: the domain package lawkv (not kv, which std
// owns) declares the operation type
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

const lawRunProviders = `import lawkv
import sync

type Last { v sync.Ref[int] } implements lawkv.Store

func NewLast() Last { Last{v: sync.NewRef(0)} }

func Put(l Last, k int, v int) { l.v.Set(v) }

func Get(l Last, k int) int { l.v.Get() }

type Zero { n int } implements lawkv.Store

func Put(z Zero, k int, v int) {}

func Get(z Zero, k int) int { z.n }

type Plain { v sync.Ref[int], hook func(int) int } implements lawkv.Store

func Put(p Plain, k int, v int) { p.v.Set(v) }

func Get(p Plain, k int) int { p.v.Get() }

func main() needs Console {
	let s lawkv.Store = NewLast()
	with context { lawkv.Store: s } { Console.Println("\(lawkv.Store.Get(1))") }
}
`

// TestLawRunOtherPackage: the laws of an operation type run against each
// provider of it, in the package that declares the provider: the member
// law with the provider bound, the top-level law with the provider as its
// argument. A false law fails with its seed and the provider's build. The
// domain package's own test build names the laws it leaves to the
// packages that provide Store, so none is silent there.
func TestLawRunOtherPackage(t *testing.T) {
	needsBuild(t)
	kv := cxPackage(t, "lawkv", lawRunStore)
	imports := cxImporter{"lawkv": kv}
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
	got := verdicts(stdout)
	if code != 1 || len(got) != 7 {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	// A member law's fix is the context form; a law whose slot is a
	// parameter runs under no context, so its fix is the constructor
	// (mutant lawfix-one-text).
	const plain = ": its values have no generator: func(int) int is a function type; "
	want := []struct{ line, prefix, suffix string }{
		{line: "skip law lawkv.Store.GetAfterPut for Plain" + plain + "bind one in a named context of a test file, let Name = context { lawkv.Store: … }, and the law runs under it"},
		{line: "ok law lawkv.Store.GetAfterPut for Last (100 cases)"},
		{prefix: "FAIL law lawkv.Store.GetAfterPut for Zero (case ", suffix: "law refuted at lib.ergo:6:3: the law is false"},
		{line: "skip law lawkv.PutKeepsOthers with s = Plain" + plain + "a func NewPlain() Plain with no rows would build one for each case"},
		{prefix: "FAIL law lawkv.PutKeepsOthers with s = Last (case ", suffix: "law refuted at lib.ergo:14:3: the law is false"},
		{prefix: "FAIL law lawkv.PutKeepsOthers with s = Zero (case ", suffix: "law refuted at lib.ergo:14:3: the law is false"},
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
	if code != 0 || !slices.Equal(verdicts(stdout), wantKV) {
		t.Errorf("kv: exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(wantKV, "\n"), firstLines(stderr, 8))
	}
}

// lawRunMem provides lawkv.Store in a package of its own, which imports lawkv to
// declare it.
const lawRunMem = `import lawkv
import sync

type Last { v sync.Ref[int] } implements lawkv.Store

func NewLast() Last { Last{v: sync.NewRef(0)} }

func Put(l Last, k int, v int) { l.v.Set(v) }

func Get(l Last, k int) int { l.v.Get() }

type Zero { n int } implements lawkv.Store

func Put(z Zero, k int, v int) {}

func Get(z Zero, k int) int { z.n }
`

const lawRunBinder = `import lawkv
import mem

func main() needs Console {
	let s lawkv.Store = mem.NewLast()
	with context { lawkv.Store: s } { Console.Println("\(lawkv.Store.Get(1))") }
}
`

// TestLawRunLeastPackage: a law runs in the least package that sees its
// operation type and a provider. A provider declares the operation type it
// implements (EXC1), so its package imports the operation type's and is
// that least package: mem runs lawkv's laws against its own providers, and
// a binder that imports both runs none again.
func TestLawRunLeastPackage(t *testing.T) {
	needsBuild(t)
	kv := cxPackage(t, "lawkv", lawRunStore)
	mf, err := syntax.Parse(lawRunMem)
	if err != nil {
		t.Fatal(err)
	}
	memImports := cxImporter{"lawkv": kv}
	memPkg, ds := check.Check("mem", []*syntax.File{mf}, memImports)
	if len(ds) != 0 {
		t.Fatalf("mem: %v", ds)
	}
	mem := Unit{Package: memPkg, Files: []*syntax.File{mf}, Sources: []Source{{Path: "/ergo-test/mem/lib.ergo", Text: lawRunMem}}}
	imports := cxImporter{"lawkv": kv, "mem": mem}
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
	memRoot := mem
	memRoot.Imports = memImports.units()
	memFiles, err := LowerTests(memRoot)
	if err != nil {
		t.Fatal(err)
	}
	exes := buildAll(t, []*lowered{{name: "lawrun_least", files: files}, {name: "lawrun_mem", files: memFiles}}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_mem"], "--seed", "7")
	got := verdicts(stdout)
	want := []string{
		"ok law lawkv.Store.GetAfterPut for Last (100 cases)",
		"FAIL law lawkv.Store.GetAfterPut for Zero (case ",
		"FAIL law lawkv.PutKeepsOthers with s = Last (case ",
		"FAIL law lawkv.PutKeepsOthers with s = Zero (case ",
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
	stdout, _, code = runExe(t, exes["lawrun_least"])
	if code != 0 || !slices.Equal(verdicts(stdout), []string{"PASS (0 tests)"}) {
		t.Errorf("binder: exit %d, stdout:\n%s", code, stdout)
	}
}

// TestLawUnderAContextTakesTheOneRule: a run under a context takes its
// cases by the one rule, its values being the law's parameters alone (the
// context builds the provider): a law with none runs once and is proved,
// one whose parameters can be listed takes every value, and one with an
// int still draws 100 (types.md, Laws 10; mutants lawctx-noparams-draws,
// lawctx-listed-draws). Before, each ran 100 cases, so a host law under a
// test's ambient started its processes 100 times (WG1 r3).
// It builds Go because the case count is the built test binary's report.
func TestLawUnderAContextTakesTheOneRule(t *testing.T) {
	needsBuild(t)
	src := `type Shape {
	Area() int,
	law Positive() { Area() >= 0 },
	law Signs(b bool) { if b { Area() >= 0 } else { -Area() <= 0 } },
	law Scales(n int) { Area() * n == n * Area() },
}

type Sq { n int } implements Shape

func Area(s Sq) int { s.n % 2 + 1 }

let Live = context { Shape: Sq{n: 2} }

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawctx_onerule", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawctx_onerule"])
	want := []string{
		"ok law Shape.Positive for Sq (100 cases)",
		"ok law Shape.Positive under Live (proved: 1 case)",
		"ok law Shape.Signs for Sq (100 cases)",
		"ok law Shape.Signs under Live (proved: all 2 cases)",
		"ok law Shape.Scales for Sq (100 cases)",
		"ok law Shape.Scales under Live (100 cases)",
		"PASS (6 tests)",
	}
	if code != 0 || !slices.Equal(verdicts(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}

// TestLawRunSkips: a law that cannot run says why, and the summary counts
// it as skipped, never as a test (fs4). A run under a context whose build
// acquires a provider with a Close releases it at the end of each case,
// after the law's last operation: Gn's Area is negative once its Close
// ran (mutant lawrel-before-last-op) or while an earlier case's Gn is
// unreleased (mutant lawrel-skipped); so does a provider with no Close
// whose field holds one (Holder under Locked: its generated release
// closes the Lock, mutant lawrel-field-unreleased), and a with of that
// context compiles (locked: the checker charged a nil Close's needs, and a
// named builder's generated release needed a frame scope; mutants
// relneeds-drop-as-close, field-release-on-frame). A part's Close that
// needs a capability makes its context a skip (Gate under Latched, mutant
// relneeds-parts-ignored). A context whose release a case
// cannot run is a skip: a Close that needs a capability (Needy, mutant
// lawrel-needs-ignored), a provider that names a package let (Pinned,
// mutant lawrel-package-let) or a generic Close with witnesses
// (Witnessed, mutant lawrel-witnessed). A provider with no generator that a
// context which runs builds (Fn under Wired, Gn under Kept) runs there
// and is no skip; one whose context does not run (Nd under Needy) stays
// a skip; a provider that runs (Sq) runs beside its context (mutants
// lawctx-covers-none, lawctx-covers-any-type,
// lawctx-covers-unrun-context, lawctx-covers-runnable).
func TestLawRunSkips(t *testing.T) {
	needsBuild(t)
	src := `import sync

type Shape {
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

type Fn { f func(int) int } implements Shape

func Area(_ Fn) int { 1 }

type Fns {} implements Box[func(int) int]

func Put(_ Fns, v own func(int) int) {}

func Apply(f func(int) int) int { f(1) }

law Calls(f func(int) int) { Apply(f) == Apply(f) }

type Res { n int } implements Shape, Closer

func Area(_ Res) int { 1 }

func Close(_ Res) {}

func openRes() Res { Res{n: 1} }

let Held = context { Shape: openRes() }

let pinned = Res{n: 3}

let Pinned = context { Shape: pinned }

let live = sync.NewRef(0)

type Gn { g func(int) int, closed sync.Ref[bool] } implements Shape, Closer

func Area(g Gn) int {
	if g.closed.Get() || live.Get() != 1 {
		return -1
	}
	1
}

func Close(g Gn) {
	g.closed.Set(true)
	live.Set(live.Get() - 1)
}

func openGn() Gn {
	live.Set(live.Get() + 1)
	Gn{g: func(n int) int { n }, closed: sync.NewRef(false)}
}

let Kept = context { Shape: openGn() }

type Lock { g func(int) int, closed sync.Ref[bool] } implements Closer

func Close(k Lock) {
	k.closed.Set(true)
	live.Set(live.Get() - 1)
}

type Holder { lock Lock } implements Shape

func Area(h Holder) int {
	if h.lock.closed.Get() || live.Get() != 1 {
		return -1
	}
	1
}

func openHolder() Holder {
	live.Set(live.Get() + 1)
	Holder{lock: Lock{g: func(n int) int { n }, closed: sync.NewRef(false)}}
}

let Locked = context { Shape: openHolder() }

func locked() int {
	with Locked {
		Shape.Area()
	}
}

type Latch { n int } implements Closer

func Close(_ Latch) needs Console {}

type Gate { latch Latch } implements Shape

func Area(_ Gate) int { 1 }

let Latched = context { Shape: Gate{latch: Latch{n: 1}} }

type Nd { g func(int) int } implements Shape, Closer

func Area(_ Nd) int { 1 }

func Close(_ Nd) needs Console {}

let Needy = context { Shape: Nd{g: func(n int) int { n }} }

type Nm {} implements Named

func Name(_ Nm) string { "n" }

type Wt[T] { Value T } implements Shape, Closer

func Area[T Named](_ Wt[T]) int { 1 }

func Close[T Named](_ Wt[T]) {}

let Witnessed = context { Shape: Wt[Nm]{Value: Nm} }

type Sq { n int } implements Shape

func Area(_ Sq) int { 1 }

let Sized = context { Shape: Sq{n: 2} }

let Wired = context { Shape: Fn{f: func(n int) int { n }} }

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_skips", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_skips"])
	want := []string{
		"skip law Shape.Positive for Nd: its values have no generator: func(int) int is a function type; bind one in a named context of a test file, let Name = context { Shape: … }, and the law runs under it",
		"ok law Shape.Positive for Res (100 cases)",
		"ok law Shape.Positive for Gate (100 cases)",
		"ok law Shape.Positive for Sq (100 cases)",
		"ok law Shape.Positive under Held (proved: 1 case)",
		"ok law Shape.Positive under Kept (proved: 1 case)",
		"skip law Shape.Positive under Latched: its provider's release needs Console; a law case has no capability to release it with",
		"ok law Shape.Positive under Locked (proved: 1 case)",
		"skip law Shape.Positive under Needy: its provider's release needs Console; a law case has no capability to release it with",
		"skip law Shape.Positive under Pinned: its provider names the package let pinned, which a case's release would close for every later case",
		"ok law Shape.Positive under Sized (proved: 1 case)",
		"ok law Shape.Positive under Wired (proved: 1 case)",
		"skip law Shape.Positive under Witnessed: its provider's generic Close takes witnesses for a type parameter constrained by operations, and a case has no instance to pass (#197)",
		"skip law Box.Keeps for Fns: its parameter v has no generator at Box[func(int) int]: func(int) int is a function type",
		"skip law Runner.Runs: its operation type Runner has the row parameter E, and a run binds only type parameters",
		"skip law Ranked.Same: its operation type Ranked constrains T by operations, and a run passes no witness",
		"skip law Calls: its parameter f has no generator: func(int) int is a function type",
		"PASS (8 tests, 9 skipped)",
	}
	if code != 0 || !slices.Equal(verdicts(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}

// lawRunCapabilities has providers that hold capabilities and no
// constructor (fu7a, check/builders.go). Book holds an fs.Files and an
// fs.Writes: AddReads holds only when both are one fresh disk per case.
// Echo writes an io.Stdout and counts in a sync.Ref. Lossy forgets, so its
// run fails with its seed. Stuck holds a queue.Queue, which has no
// builder, so its run is a skip line with the reason.
const lawRunCapabilities = `import fs
import fstest
import io
import queue
import sync

type Notes {
	Add(line string),
	All() string,
	law AddReads(line string) {
		Add(line)
		All() == line + "\n"
	},
}

type Book { files fs.Files, writes fs.Writes } implements Notes

func Add(b Book, line string) {
	b.writes.AppendFile("notes", line + "\n") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
	}
}

func All(b Book) string {
	b.files.ReadFile("notes") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		""
	}
}

type Lossy { files fs.Files, writes fs.Writes } implements Notes

func Add(l Lossy, line string) {}

func All(l Lossy) string { "" }

type Stuck { q queue.Queue[int] } implements Notes

func Add(s Stuck, line string) {}

func All(s Stuck) string { "" }

type Loud {
	Shout(s string) int,
	law CountsUp(s string) {
		let a = Shout(s)
		Shout(s) == a + 1
	},
}

type Echo { out io.Stdout, n sync.Ref[int] } implements Loud

func Shout(e Echo, s string) int {
	e.out.Println("echo:" + s)
	e.n.Update(func(v int) int { v + 1 })
	e.n.Get()
}

// The builder of fs.Files is in fstest, so the package sees fstest.
test BookStartsEmpty {
	let m = fstest.NewMemory()
	assert (Book{files: m, writes: m}).All() == ""
}

func main() {}
`

// TestLawRunCapabilities: a provider that holds capabilities runs its laws
// with no constructor, each case on its own world: a fresh fstest.Memory
// that the provider's fs.Files and fs.Writes share, an io.Recorder for its
// io.Stdout (nothing reaches the binary's output) and a new sync.Ref. A
// false law fails with its seed and the provider's build.
// It builds Go because the law runner is that binary; no interpreter runs
// a law. Mutants it kills: one world for every case (Book fails at its
// second case), a builder per field (Book's reads miss its writes), a host
// provider as a builder (fs.NewHost and fstest.NewMemory are two builders,
// so Book skips; or "echo:" reaches stdout), and a capability with no
// builder that runs instead of its skip line.
func TestLawRunCapabilities(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_caps", lawRunCapabilities)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_caps"], "--seed", "7")
	got := verdicts(stdout)
	want := []struct{ line, prefix, suffix string }{
		{line: "ok BookStartsEmpty"},
		{line: "skip law Notes.AddReads for Stuck: its values have no generator: queue.Queue[int] is an operation type with no builder: neither queue nor queuetest, as this package sees them, declares a New<X> with no row whose X provides it; bind one in a named context of a test file, let Name = context { Notes: … }, and the law runs under it"},
		{line: "ok law Notes.AddReads for Book (100 cases)"},
		{prefix: "FAIL law Notes.AddReads for Lossy (case 1 of 100, seed 7, size 0: provider = Lossy{files: fstest.NewMemory(), writes: fstest.NewMemory()}, line = ", suffix: "law refuted at main.ergo:12:3: the law is false"},
		{line: "ok law Loud.CountsUp for Echo (100 cases)"},
		{line: "FAIL (1 of 4 tests, 1 skipped)"},
	}
	if code != 1 || len(got) != len(want) {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	for i, w := range want {
		switch {
		case w.line != "" && got[i] != w.line:
			t.Errorf("line %d is %q, want %q", i+1, got[i], w.line)
		case w.line == "" && (!strings.HasPrefix(got[i], w.prefix) || !strings.HasSuffix(got[i], w.suffix)):
			t.Errorf("line %d is %q, want %q … %q", i+1, got[i], w.prefix, w.suffix)
		}
	}
}

// TestLawRunDefinedBool: a law's result may be of a type over bool, at the
// tail and at a return (types.md, Laws 8); its run converts it.
func TestLawRunDefinedBool(t *testing.T) {
	needsBuild(t)
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
	if code != 0 || !slices.Equal(verdicts(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
}

// TestLawRunGenericListed is the repro of a batch-14 merge bug: a law of a
// generic operation type (L4) whose values can be listed at the provider's
// instance runs by the one rule (Laws 10, MU6b), proved or refuted at its
// first case, and its listed case passes the provider and the instance's
// type arguments as the drawn run does. It builds Go because the subject
// is the run's report. Mutant: listedRuns reads the law's own parameter
// types (T), so the run draws 100 cases and is never proved.
func TestLawRunGenericListed(t *testing.T) {
	needsBuild(t)
	src := `type Flip[T comparable] {
	Not(v T) T,
	law Moves(v T) { Not(v) != v },
}

type Turn {} implements Flip[bool]

func Not(_ Turn, v bool) bool { !v }

type Stuck {} implements Flip[bool]

func Not(_ Stuck, v bool) bool { v }

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_generic_listed", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_generic_listed"])
	got := verdicts(stdout)
	if code != 1 || len(got) != 3 || got[0] != "ok law Flip.Moves for Turn (proved: all 2 cases)" ||
		!strings.HasPrefix(got[1], "FAIL law Flip.Moves for Stuck (case 1 of 2, every value: provider = Stuck, v = false)") {
		t.Errorf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
}

// TestLawRunNoParams: a top-level law with no parameters draws nothing and
// runs its one case, so a run that holds is proved (Laws 10); a false one
// fails the run.
func TestLawRunNoParams(t *testing.T) {
	needsBuild(t)
	src := `law Holds() { Two() == 2 }

law Breaks() { Two() == 3 }

func main() {}

func Two() int { 1 + 1 }
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_noparams", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_noparams"])
	want := []string{"ok law Holds (proved: 1 case)", "FAIL law Breaks: law refuted at main.ergo:3:16: the law is false", "FAIL (1 of 2 tests)"}
	if code != 1 || !slices.Equal(verdicts(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}

// lawRunProgram is a generated program of the property below: one
// operation type with a member law and 1-3 providers, of which at most one
// is broken: it breaks the law for n above a threshold, so it fails at some
// case. Half the laws reach the operation through a helper that needs the
// type (the b13 binding); each program also has a top-level law, true or
// false. Half the providers are also bound by a named context, under
// which the law runs too (L5), and fails exactly when the provider is the
// broken one. Half the non-generic Ops reached without the helper declare
// F mut, and half of their providers are borrow providers, whose F takes
// mut P (Laws 10; a function on mut P implements only a mut operation,
// MODES-4), and half the programs add Gate, whose one
// borrow provider G is listed: proved, or refuted by a broken Set. want
// are the lines the test binary prints, in order, and exit its status.
type lawRunProgram struct {
	name, src string
	generic   bool   // Op[T], run at each provider's instance
	mutOp     bool   // a non-generic Op that declares mut F
	borrowed  bool   // a borrow provider of Op is the broken one
	gate      string // "ok" or "FAIL" for Gate's listed borrow provider, "" for none
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
	// A non-generic Op reached without the helper declares F mut half the
	// time (MODES-4): only then may a provider be a borrow provider, as a
	// function on mut P implements only a mut operation (S106), while a
	// function on P implements either kind.
	opMut := !generic && !helper && r.Intn(2) == 0
	mode := ""
	if opMut {
		mode = "mut "
	}
	if generic {
		b.WriteString("type Op[T number] {\n\tF(n T) T,\n\tlaw Linear(n T) { F(n + n) == F(n) + F(n) - F(n - n) },\n}\n\n")
	} else {
		fmt.Fprintf(&b, "type Op {\n\t%sF(n int) int,\n\tlaw Linear(n int) { %s == n * %d + %d },\n}\n\n", mode, call, k, c)
	}
	if helper {
		b.WriteString("func viaHelper(n int) int needs Op { Op.F(n) }\n\n")
	}
	providers := r.Intn(3) + 1
	broken := r.Intn(providers + 1) // == providers: none is broken
	var want []string
	var ctxs strings.Builder
	var under []string
	failed := 0
	borrowed, gate := false, ""
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
		// A borrow provider (Laws 10): F takes *P and writes it, so only
		// *P implements Op; the run binds the case's P by its address.
		impl := "Op"
		if generic {
			impl = "Op[" + elem + "]"
		}
		borrow := opMut && r.Intn(2) == 0
		if borrow {
			fmt.Fprintf(&b, "type %s { tag int } implements %s\n\nfunc F(p mut %s, n int) int {\n\tp.tag = p.tag + 1\n\t%s\n}\n\n", name, impl, name, body)
		} else {
			fmt.Fprintf(&b, "type %s { tag int } implements %s\n\nfunc F(_ %s, n %s) %s { %s }\n\n", name, impl, name, elem, elem, body)
		}
		line := "ok law Op.Linear for " + name + " (100 cases)"
		if p == broken {
			line = "FAIL law Op.Linear for " + name + " (case "
			borrowed = borrow
			failed++
		}
		want = append(want, line)
		if !opMut && r.Intn(2) == 0 {
			// A named context that binds the provider: the law also runs
			// under it (L5), after the provider runs. A mut Op's value
			// needs a place (Borrows 6), which a context entry is not.
			key := "Op"
			if generic {
				key = "Op[" + elem + "]"
			}
			fmt.Fprintf(&ctxs, "let C%d = context { %s: %s{tag: 1} }\n\n", p, key, name)
			line := "ok law Op.Linear under C" + fmt.Sprint(p) + " (100 cases)"
			if p == broken {
				line = "FAIL law Op.Linear under C" + fmt.Sprint(p) + " (case "
				failed++
			}
			under = append(under, line)
		}
	}
	b.WriteString(ctxs.String())
	want = append(want, under...)
	providers += len(under)
	if r.Intn(2) == 0 {
		// A borrow provider whose values can be listed: Gate's law over
		// a bool runs every value of G and of on, and is proved, or fails
		// at its first case when G's Set is broken.
		set := "g.on = on"
		if r.Intn(2) == 0 {
			set = "g.on = true"
			want = append(want, "FAIL law Gate.Keeps for G (case ")
			gate = "FAIL"
			failed++
		} else {
			want = append(want, "ok law Gate.Keeps for G (proved: all 4 cases)")
			gate = "ok"
		}
		fmt.Fprintf(&b, "type Gate {\n\tmut Set(on bool),\n\tmut On() bool,\n\tlaw Keeps(on bool) {\n\t\tSet(on)\n\t\tOn() == on\n\t},\n}\n\ntype G { on bool } implements Gate\n\nfunc Set(g mut G, on bool) { %s }\n\nfunc On(g mut G) bool { g.on }\n\n", set)
		providers++
	}
	top := r.Intn(2) == 0
	if top {
		b.WriteString("law Top(a int, b int) { Sum(a, b) == Sum(b, a) }\n\n")
		want = append(want, "ok law Top (100 cases)")
	} else {
		b.WriteString("law Top(a int, b int) { Sum(a, -b) == Sum(b, -a) }\n\n")
		want = append(want, "FAIL law Top (case ")
		failed++
	}
	b.WriteString("func Sum(a int, b int) int { a + b }\n\n")
	b.WriteString("func main() {}\n")
	total := providers + 1
	exit := 0
	if failed > 0 {
		exit = 1
		want = append(want, fmt.Sprintf("FAIL (%d of %d tests)", failed, total))
	} else {
		want = append(want, fmt.Sprintf("PASS (%d tests)", total))
	}
	return lawRunProgram{name: fmt.Sprintf("lawrun_gen_%02d", i), src: b.String(), want: want, exit: exit, generic: generic, mutOp: opMut, borrowed: borrowed, gate: gate}
}

// TestLawRunFindsTheBrokenProvider is the property: over generated
// operation types with 1-3 providers, one of them broken at random (or
// none), exactly the broken provider fails, with its name and the seed, and
// the top-level law runs as well. A failure prints the program's source.
// ERGO_LAWRUN_SEED replays another seed.
func TestLawRunFindsTheBrokenProvider(t *testing.T) {
	needsBuild(t)
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
	genericBroken, borrowBroken := false, false
	modes := map[bool]bool{} // a non-generic Op's F: mut, or not
	gates := map[string]bool{}
	for i := 0; i < 32; i++ {
		p := genLawRunProgram(r, i)
		progs = append(progs, p)
		borrowBroken = borrowBroken || p.borrowed
		if !p.generic {
			modes[p.mutOp] = true
		}
		gates[p.gate] = true
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
	// It reaches a broken borrow provider of Op, and Gate's listed borrow
	// provider both proved and refuted (Laws 10).
	if !modes[true] || !modes[false] {
		t.Fatalf("seed %d: the generator draws one mode of a non-generic Op's F only (%v)", seed, modes)
	}
	if !borrowBroken || !gates["ok"] || !gates["FAIL"] {
		t.Fatalf("seed %d: the generator reaches no broken borrow provider (%v) or no proved and refuted Gate (%v)", seed, borrowBroken, gates)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		stdout, stderr, code := runExe(t, exes[p.name])
		got := verdicts(stdout)
		ok := code == p.exit && len(got) == len(p.want)
		for k := 0; ok && k < len(got); k++ {
			w := p.want[k]
			switch {
			case strings.HasPrefix(w, "FAIL law Gate."):
				ok = strings.HasPrefix(got[k], w) && strings.Contains(got[k], ", every value: provider = G{") && strings.HasSuffix(got[k], ": the law is false")
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

// lawRunFiles provides fs.Files six ways. Drifty's reads of a path both
// fail NotFound, the second naming another path; DriftyDenied's both fail
// Denied, the second naming another path; DriftyFailed's both fail Failed
// with another Reason; Flaky's alternate NotFound and "x"; Shifty's
// alternate Denied and NotFound; so none keeps fs.Files.ReadIsRepeatable.
// Steady fails the empty path with NotFound every time and reads "x"
// otherwise, so it keeps it, its failures included (batch-12 M2, M2a;
// batch-13 fu8c: a same-kind arm of each kind compares the fields).
const lawRunFiles = `import fs
import sync

type Drifty { reads sync.Ref[int] } implements fs.Files

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

type DriftyDenied { reads sync.Ref[int] } implements fs.Files

func NewDriftyDenied() DriftyDenied { DriftyDenied{reads: sync.NewRef[int](0)} }

func ReadFile(f DriftyDenied, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	let n = f.reads.Get()
	f.reads.Set(n + 1)
	if n % 2 == 0 {
		return fs.Denied{Path: path}
	}
	fs.Denied{Path: path + "/other"}
}

func ReadDir(f DriftyDenied, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

func Stat(f DriftyDenied, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

type DriftyFailed { reads sync.Ref[int] } implements fs.Files

func NewDriftyFailed() DriftyFailed { DriftyFailed{reads: sync.NewRef[int](0)} }

func ReadFile(f DriftyFailed, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	let n = f.reads.Get()
	f.reads.Set(n + 1)
	if n % 2 == 0 {
		return fs.Failed{Path: path, Reason: "busy"}
	}
	fs.Failed{Path: path, Reason: "gone"}
}

func ReadDir(f DriftyFailed, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

func Stat(f DriftyFailed, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }

type Flaky { reads sync.Ref[int] } implements fs.Files

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

type Shifty { reads sync.Ref[int] } implements fs.Files

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

type Steady {} implements fs.Files

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
// one kind with other fields (Drifty, DriftyDenied, DriftyFailed: one per
// kind), and holds for a steady one.
// fstest.Memory and the disk keep it in TestFSMemoryAgreesWithHost and
// TestStdHostLawsHold. Mutants it kills: a first failed read that holds
// the law vacuously (Flaky passes), a second failure that is not compared
// with the first (Shifty passes), and a same-kind arm that compares no
// field, in any of the three arms (Drifty, DriftyDenied or DriftyFailed
// passes). It builds Go because the law runner, its provider builds and its seeded
// cases exist only in the lowered test binary, and Flaky's state is a
// sync.Ref that only the runtime runs; no in-process interpreter runs a
// member law against a provider.
func TestLawRunReadIsRepeatable(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_files", lawRunFiles)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_files"], "--seed", "7")
	got := verdicts(stdout)
	want := []string{
		"FAIL law fs.Files.ReadIsRepeatable for Drifty (case ",
		"FAIL law fs.Files.ReadIsRepeatable for DriftyDenied (case ",
		"FAIL law fs.Files.ReadIsRepeatable for DriftyFailed (case ",
		"FAIL law fs.Files.ReadIsRepeatable for Flaky (case ",
		"FAIL law fs.Files.ReadIsRepeatable for Shifty (case ",
		"ok law fs.Files.ReadIsRepeatable for Steady (",
		"FAIL (5 of 6 tests)",
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
