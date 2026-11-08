package check_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// importedFootprintImporter serves package lib and the standard library.
type importedFootprintImporter struct{ lib *check.Package }

func (i importedFootprintImporter) Import(path string) (*check.Package, error) {
	if path == "lib" {
		return i.lib, nil
	}
	return check.Std.Import(path)
}

// footprintCaps is the pool of capabilities of the generated contexts.
const footprintCaps = 6

// footprintDecls declares, for each capability Ci, its operation type, a
// provider type Mi, and four providers of it: Mi runs nothing, UseI()
// calls C0's operation (C0 is bound by every generated context, and a
// named context is closed: its providers use only what it binds),
// OpaqueI() calls a function value, and SpawnI() starts a task.
func footprintDecls() string {
	var b strings.Builder
	b.WriteString("import task\n\n")
	for i := range footprintCaps {
		fmt.Fprintf(&b, "type C%d { Get%d() int }\n\ntype M%d {}\n\nfunc Get%d(_ M%d) int { %d }\n\n", i, i, i, i, i, i)
		if i > 0 {
			fmt.Fprintf(&b, "func Use%d() M%d needs C0 {\n\tlet _ = C0.Get0()\n\tM%d\n}\n\n", i, i, i)
		}
		fmt.Fprintf(&b, "func Opaque%d() M%d {\n\tlet f = func() M%d { M%d }\n\tf()\n}\n\n", i, i, i, i)
		fmt.Fprintf(&b, "func Spawn%d() M%d {\n\tlet t = task.Go() { %d }\n\tt.Stop()\n\tM%d\n}\n\n", i, i, i, i)
	}
	return b.String()
}

// footprintKinds is the number of provider kinds of footprintProvider.
const footprintKinds = 5

// footprintProvider is a provider of capability i, by kind, with the
// qualifier q of the package that declares it ("" or "lib."): 0 the value
// Mi, 1 UseI(), 2 OpaqueI(), 3 SpawnI(), 4 a function literal called where
// it is written that uses C0. C0 has no Use provider and its literal uses
// nothing: it would use itself.
func footprintProvider(q string, i, kind int) string {
	switch {
	case kind == 0:
		return fmt.Sprintf("%sM%d", q, i)
	case kind == 1 && i > 0:
		return fmt.Sprintf("%sUse%d()", q, i)
	case kind == 3:
		return fmt.Sprintf("%sSpawn%d()", q, i)
	case kind == 4 && i > 0:
		return fmt.Sprintf("func() %sM%d {\n\tlet _ = %sC0.Get0()\n\t%sM%d\n}()", q, i, q, q, i)
	case kind == 4:
		return fmt.Sprintf("func() %sM0 { %sM0 }()", q, q)
	}
	return fmt.Sprintf("%sOpaque%d()", q, i)
}

// footprintCase is a generated chain of named contexts W0..Wn-1, where W0
// binds C0 and each later one embeds the one before it, binds fresh
// capabilities and may replace one bound below it, and a context X that
// embeds the last, may replace entries of the chain at any depth and may
// bind a fresh capability. contexts writes the chain; x writes X with the
// qualifier q of the chain's package.
// hiddenKinds holds the provider kinds of the chain when X hides an entry
// (the imported side then walks the chain's providers one by one).
type footprintCase struct {
	contexts    string
	x           func(q string) string
	hiddenKinds map[int]bool
}

type footprintEntry struct {
	replace   bool
	cap, kind int
}

// writeContext writes a context literal: its entries, then its embedding
// (an embedding written first would read as a key of the next entry).
func writeContext(q string, es []footprintEntry, embed string) string {
	var parts []string
	for _, e := range es {
		s := fmt.Sprintf("%sC%d: %s", q, e.cap, footprintProvider(q, e.cap, e.kind))
		if e.replace {
			s = "replace " + s
		}
		parts = append(parts, s)
	}
	if embed != "" {
		parts = append(parts, embed)
	}
	return "context { " + strings.Join(parts, ", ") + " }"
}

func genFootprintCase(r *rand.Rand) footprintCase {
	n := 1 + r.Intn(3)
	bound, kinds := map[int]bool{}, map[int]bool{}
	var b strings.Builder
	for i := range n {
		var es []footprintEntry
		var mine []int
		if i == 0 {
			es = append(es, footprintEntry{cap: 0, kind: []int{0, 2, 3, 4}[r.Intn(4)]})
			mine = append(mine, 0)
		}
		var free, below []int
		for k := range footprintCaps {
			if bound[k] {
				below = append(below, k)
			} else if k != 0 {
				free = append(free, k)
			}
		}
		r.Shuffle(len(free), func(a, c int) { free[a], free[c] = free[c], free[a] })
		for _, k := range free[:min(len(free), r.Intn(3))] {
			es = append(es, footprintEntry{cap: k, kind: r.Intn(footprintKinds)})
			mine = append(mine, k)
		}
		if len(below) > 0 && r.Intn(2) == 0 {
			k := below[r.Intn(len(below))]
			es = append(es, footprintEntry{replace: true, cap: k, kind: r.Intn(footprintKinds)})
		}
		embed := ""
		if i > 0 {
			embed = fmt.Sprintf("W%d", i-1)
		}
		fmt.Fprintf(&b, "let W%d = %s\n\n", i, writeContext("", es, embed))
		for _, e := range es {
			kinds[e.kind] = true
		}
		for _, k := range mine {
			bound[k] = true
		}
	}
	var boundList, freeList []int
	for k := range footprintCaps {
		if bound[k] {
			boundList = append(boundList, k)
		} else {
			freeList = append(freeList, k)
		}
	}
	// X hides up to two entries of the chain.
	var xs []footprintEntry
	for range r.Intn(3) {
		if len(boundList) == 0 {
			break
		}
		j := r.Intn(len(boundList))
		xs = append(xs, footprintEntry{replace: true, cap: boundList[j], kind: r.Intn(footprintKinds)})
		boundList = append(boundList[:j], boundList[j+1:]...)
	}
	if len(freeList) > 0 && r.Intn(2) == 0 {
		xs = append(xs, footprintEntry{cap: freeList[r.Intn(len(freeList))], kind: r.Intn(footprintKinds)})
	}
	var hidden map[int]bool
	if len(xs) > 0 && xs[0].replace {
		hidden = kinds
	}
	return footprintCase{
		hiddenKinds: hidden,
		contexts:    b.String(),
		x: func(q string) string {
			return "let X = " + writeContext(q, xs, fmt.Sprintf("%sW%d", q, n-1)) + "\n"
		},
	}
}

// footprintOfX checks the package of src and returns it and the footprint
// of X's builder, "" when it declares no X.
func footprintOfX(t *testing.T, path, src string, imp check.Importer) (*check.Package, string) {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, src)
	}
	pkg, ds := check.Check(path, []*syntax.File{f}, imp)
	if len(ds) != 0 {
		t.Fatalf("check %s: %v\n%s", path, ds, src)
	}
	x := pkg.Contexts["X"]
	if x == nil {
		return pkg, ""
	}
	fp, ok := pkg.Info.Footprints[x.Decl]
	if !ok {
		t.Fatalf("no footprint for X\n%s", src)
	}
	return pkg, showFootprint(fp)
}

// showFootprint prints fp with each capability's name: C0 of lib and C0 of
// main are one capability of the generated case.
func showFootprint(fp check.Footprint) string {
	var caps []string
	for _, c := range fp.Caps {
		s := c.String()
		caps = append(caps, s[strings.LastIndex(s, ".")+1:])
	}
	return fmt.Sprintf("caps=%v tasks=%v opaque=%v", caps, fp.Tasks, fp.Opaque)
}

// TestEmbeddedImportedContextFootprint (batch-9 C4): the footprint of a
// context that embeds a named context is the same whether that context is
// declared in the same package or imported from another one, with hidden
// (replaced) entries at any depth of an embedding chain and replaced
// entries inside the chain. Before, an imported embedded context made the
// footprint opaque.
func TestEmbeddedImportedContextFootprint(t *testing.T) {
	footprintProperty(t, contextFootprints)
}

// contextFootprints are the footprints the generated contexts reach:
// empty, C0 only, opaque only, both, and Tasks with and without C0 (a task
// start is also opaque: task.Go is a host function, and a provider uses
// its Task through an operation, t.Stop()). No context footprint has Tasks
// without opaque, even once a std body that starts a task is pure: a Task
// leaves its function only by return (runtime.md, Tasks 3), so a provider,
// whose result is its provider type, uses each Task it starts through an
// operation, and an operation call is opaque. Tasks without opaque is a
// function frame's footprint (TestFootprintsPureTaskBody).
var contextFootprints = []string{
	"caps=[] tasks=false opaque=false", "caps=[C0] tasks=false opaque=false",
	"caps=[] tasks=false opaque=true", "caps=[C0] tasks=false opaque=true",
	"caps=[] tasks=true opaque=true", "caps=[C0] tasks=true opaque=true",
}

// TestEmbeddedImportedContextFootprintPureTask (fu-pure-task) is the same
// property with task's std externs counted as pure (the test seam
// SetPureStdForTest): the Spawn provider's task.Go takes runs' pure-std
// path, which no std body reaches yet, in the same-package walk and in the
// imported provider-by-provider walk. Tasks must survive that path; opaque
// comes from t.Stop().
func TestEmbeddedImportedContextFootprintPureTask(t *testing.T) {
	defer pureTaskSeam(t)()
	footprintProperty(t, contextFootprints)
}

// pureTaskSeam opens the seam that counts task's std externs as pure and
// returns its restore. Every std package is checked first, so none records
// a footprint under the seam.
func pureTaskSeam(t *testing.T) (restore func()) {
	t.Helper()
	for _, path := range std.Packages() {
		if _, err := check.Std.Import(path); err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
	}
	return check.SetPureStdForTest("task")
}

// TestFootprintsPureTaskBody (fu-pure-task): a frame that runs a pure std
// body whose result holds a Task has Tasks and is not opaque, as runs says
// (footprint.go, pureStd), directly, through a function of its package and
// through an imported function's recorded footprint. Std has no such body
// yet, so the seam makes task.Go one; when std gets a real one, the
// tripwire below names it and the case runs without the seam.
func TestFootprintsPureTaskBody(t *testing.T) {
	if bodies := stdPureTaskBodies(t); len(bodies) > 0 {
		t.Errorf("std has pure bodies whose result holds a Task, %v: call one in this test without the seam, and keep the seam case", bodies)
	}
	defer pureTaskSeam(t)()
	const lib = "import task\n\ntype Fault {}\n\nfunc Start() Task[int, Fault] { task.Go() { 1 } }\n\nfunc Again() Task[int, Fault] { Start() }\n\nfunc Stops() int {\n\tlet t = task.Go() { 1 }\n\tt.Stop()\n\t0\n}\n"
	pkg := checkFootprintPackage(t, "lib", lib, nil)
	main := checkFootprintPackage(t, "main", "import lib\n\nfunc Later() Task[int, lib.Fault] { lib.Start() }\n", importedFootprintImporter{pkg})
	for _, c := range []struct {
		pkg        *check.Package
		name, want string
	}{
		{pkg, "Start", "caps=[] tasks=true opaque=false"},
		{pkg, "Again", "caps=[] tasks=true opaque=false"},
		{main, "Later", "caps=[] tasks=true opaque=false"},
		{pkg, "Stops", "caps=[] tasks=true opaque=true"},
	} {
		var got string
		for _, f := range c.pkg.Funcs {
			if f.Name == c.name {
				got = showFootprint(c.pkg.Info.Footprints[f.Decl])
			}
		}
		if got != c.want {
			t.Errorf("%s: footprint %q, want %q", c.name, got, c.want)
		}
	}
}

// checkFootprintPackage checks src as the package path, with no diagnostic.
func checkFootprintPackage(t *testing.T, path, src string, imp check.Importer) *check.Package {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, src)
	}
	pkg, ds := check.Check(path, []*syntax.File{f}, imp)
	if len(ds) != 0 {
		t.Fatalf("check %s: %v\n%s", path, ds, src)
	}
	return pkg
}

// stdPureTaskBodies are the std externs whose runtime/v1 body is pure and
// whose result holds a Task.
func stdPureTaskBodies(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, path := range std.Packages() {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, f := range pkg.Funcs {
			if f.Decl == nil || f.Decl.Extern == nil || f.Sig == nil {
				continue
			}
			target, name := f.Decl.Extern.Split()
			if target != "ergo.local/prototype/runtime/v1" {
				continue
			}
			if b, err := v1.BodyOf(name); err != nil || !b.Pure {
				continue
			}
			for _, r := range f.Sig.Results {
				if strings.Contains(r.String(), "Task[") {
					out = append(out, path+"."+f.Name)
					break
				}
			}
		}
	}
	return out
}

// footprintProperty runs the generated cases of
// TestEmbeddedImportedContextFootprint and asks for each footprint of want.
func footprintProperty(t *testing.T, want []string) {
	t.Helper()
	r := rand.New(rand.NewSource(9))
	decls := footprintDecls()
	kinds, hiddenKinds := map[string]bool{}, map[int]bool{}
	for i := range 300 {
		c := genFootprintCase(r)
		_, same := footprintOfX(t, "main", decls+c.contexts+c.x(""), nil)
		lib, _ := footprintOfX(t, "lib", decls+c.contexts, nil)
		_, imported := footprintOfX(t, "main", "import lib\n\n"+c.x("lib."), importedFootprintImporter{lib})
		if same != imported {
			t.Fatalf("case %d: same package %s, imported %s\nlib:\n%s\nmain:\n%s", i, same, imported, c.contexts, c.x("lib."))
		}
		kinds[same] = true
		for k := range c.hiddenKinds {
			hiddenKinds[k] = true
		}
	}
	for _, w := range want {
		if !kinds[w] {
			t.Errorf("no case has the footprint %s; have %v", w, kinds)
		}
	}
	// Each provider kind sits in a chain whose entry X hides, so the
	// imported side walks it provider by provider.
	for k := range footprintKinds {
		if !hiddenKinds[k] {
			t.Errorf("no case hides an entry of a chain with a provider of kind %d", k)
		}
	}
}

// TestForeignEntryProviderKinds (batch-9 C4 r2, the round-1 verifier's
// probe): an imported context whose entry the importer hides is walked
// provider by provider. A provider that starts a task keeps Tasks, and a
// function literal called where it is written keeps the capability it
// uses: the imported footprint equals the same-package one.
func TestForeignEntryProviderKinds(t *testing.T) {
	decls := `import task

type C0 { Get0() int }
type M0 {}
func Get0(_ M0) int { 0 }
type C1 { Get1() int }
type M1 {}
func Get1(_ M1) int { 1 }
type C2 { Get2() int }
type M2 {}
func Get2(_ M2) int { 2 }
func spawn() M1 {
	let t = task.Go() { 1 }
	t.Stop()
	M1
}

`
	cases := []struct{ name, w, want string }{
		{"task", "let W = context { C0: M0, C1: spawn() }\n", "caps=[] tasks=true opaque=true"},
		{"lit", "let W = context { C0: M0, C2: func() M2 {\n\tlet _ = C0.Get0()\n\tM2\n}() }\n", "caps=[C0] tasks=false opaque=false"},
	}
	for _, c := range cases {
		x := func(q string) string {
			return "let X = context { replace " + q + "C0: " + q + "M0, " + q + "W }\n"
		}
		_, same := footprintOfX(t, "main", decls+c.w+x(""), nil)
		lib, _ := footprintOfX(t, "lib", decls+c.w, nil)
		_, imported := footprintOfX(t, "main", "import lib\n\n"+x("lib."), importedFootprintImporter{lib})
		if same != c.want || imported != c.want {
			t.Errorf("%s: same package %s, imported %s, want both %s", c.name, same, imported, c.want)
		}
	}
}
