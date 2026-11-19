package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/stage"
)

// incProgram is a module of four packages: a, c, b (imports a) and the
// root cmd/inc (imports b and c, and a when mainA), written from a set of
// edit flags. Each flag is one edit of the plan's model (§5.3): a comment,
// a body, a signature, a new and a removed export, a new file, a type
// error, an edit of another package, an added import.
type incProgram struct {
	comment, plus, rename, triple, extra, broken, quadPlus, cc, mainA bool
}

const incFlags = 9

func (p *incProgram) flip(i int) {
	f := []*bool{&p.comment, &p.plus, &p.rename, &p.triple, &p.extra, &p.broken, &p.quadPlus, &p.cc, &p.mainA}
	*f[i] = !*f[i]
}

var incFlagNames = []string{"comment in a", "body of a.Double", "a.Open's parameter name", "export a.Triple", "file a/extra.ergo", "a type error in a", "body of b.Quad", "body of c.Name", "main imports a"}

// files is the program's files by module-relative path.
func (p incProgram) files() map[string]string {
	a := ""
	if p.comment {
		a += "// Package a doubles.\n"
	}
	if p.plus {
		a += "func Double(n int) int {\n\tn + n\n}\n"
	} else {
		a += "func Double(n int) int {\n\tn * 2\n}\n"
	}
	a += "\ntype Box { V int }\n\n"
	if p.rename {
		a += "func Open(box Box) int {\n\tbox.V\n}\n"
	} else {
		a += "func Open(b Box) int {\n\tb.V\n}\n"
	}
	if p.triple {
		a += "\nfunc Triple(n int) int {\n\tn * 3\n}\n"
	}
	if p.broken {
		a += "\nfunc bad() int {\n\t\"x\"\n}\n"
	}
	b := "import inc/a\n\n"
	if p.quadPlus {
		b += "func Quad(n int) int {\n\ta.Double(n) + a.Double(n)\n}\n"
	} else {
		b += "func Quad(n int) int {\n\ta.Double(a.Double(n))\n}\n"
	}
	b += "\nfunc Unbox(n int) int {\n\ta.Open(a.Box{V: n})\n}\n\ntest QuadIsFour {\n\tassert Quad(1) == 4\n\tassert Unbox(3) == 3\n}\n"
	c := "func Name() string {\n\t\"c\"\n}\n"
	if p.cc {
		c = "func Name() string {\n\t\"c\" + \"c\"\n}\n"
	}
	m := "import inc/b\nimport inc/c\n"
	use := "b.Quad(2)"
	if p.mainA {
		m += "import inc/a\n"
		use = "a.Double(b.Quad(2))"
	}
	m += "\nfunc main() needs Console {\n\tConsole.Println(\"\\(" + use + ") \\(c.Name())\")\n}\n"
	out := map[string]string{"ergo.mod": "module inc\n", "a/a.ergo": a, "b/b.ergo": b, "c/c.ergo": c, "cmd/inc/main.ergo": m}
	if p.extra {
		out["a/extra.ergo"] = "func Extra() int {\n\t1\n}\n"
	}
	return out
}

// write makes dir hold exactly p's files.
func (p incProgram) write(t *testing.T, dir string) {
	t.Helper()
	files := p.files()
	if _, err := os.Stat(filepath.Join(dir, "a", "extra.ergo")); err == nil && files["a/extra.ergo"] == "" {
		if err := os.Remove(filepath.Join(dir, "a", "extra.ergo")); err != nil {
			t.Fatal(err)
		}
	}
	for rel, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// checkWork runs f and returns the packages it checked and took from the cache.
func checkWork(f func()) (checked, reused int64) {
	c0, r0 := stage.Get(stage.PackagesChecked), stage.Get(stage.PackagesReused)
	f()
	return stage.Get(stage.PackagesChecked) - c0, stage.Get(stage.PackagesReused) - r0
}

// cachedEdit checks p, then checks it again after edit with the cache, and
// returns the work of the second check.
func cachedEdit(t *testing.T, p incProgram, edit int) (checked, reused int64) {
	t.Helper()
	dir := t.TempDir()
	p.write(t, dir)
	root := filepath.Join(dir, "cmd", "inc")
	cache := NewCheckCache()
	if a, err := cache.CheckDir(root); err != nil || len(a.Diagnostics) > 0 {
		t.Fatalf("first check: %v %v", err, a.Diagnostics)
	}
	p.flip(edit)
	p.write(t, dir)
	checked, reused = checkWork(func() {
		a, err := cache.CheckDir(root)
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("after %s: %v %v", incFlagNames[edit], err, a.Diagnostics)
		}
	})
	return checked, reused
}

// TestBodyEditChecksOnePackage (CS5b-3, plan §6 row CS5): after a body
// edit in a, only a is checked; b, c and the root are taken from the
// cache, b and the root against the new check of a: the cutoff stops at
// a's unchanged interface. Mutant: the key holds the imports'
// implementation fingerprints (cs5b3-cutoff-on-implementation).
func TestBodyEditChecksOnePackage(t *testing.T) {
	if checked, reused := cachedEdit(t, incProgram{}, 1); checked != 1 || reused != 3 {
		t.Errorf("a body edit checked %d packages and reused %d; want 1 and 3", checked, reused)
	}
}

// TestSignatureEditStopsAtUnchangedInterface: an exported signature edit
// in a checks a and the packages that reach it (b, the root), and c, which
// does not, is taken from the cache. Mutant: the key leaves out the
// imports' interfaces (cs5b3-key-omits-imports), so b, decoded against
// the new a, is reused.
func TestSignatureEditStopsAtUnchangedInterface(t *testing.T) {
	if checked, reused := cachedEdit(t, incProgram{}, 2); checked != 3 || reused != 1 {
		t.Errorf("a signature edit checked %d packages and reused %d; want 3 and 1", checked, reused)
	}
}

// TestIncrementalCheckAgrees (CS5b-3, plan §5.3): over seeded edit
// sequences of incProgram, the check with the cache equals the check from
// scratch after every edit: the same diagnostics, the same interface of
// every package, the same test results and the same lowered Go; every package is checked or
// reused; a package whose files no clean check has seen is checked; and a
// package none of whose files, or its imports' files, changed since the
// last clean check is reused. ERGO_SEED=<n>
// replays one seed and prints its edits.
//
// Mutant (sketchfront ledger): a reused package keeps the loader's parse
// trees, which its decoded Info does not name, and the lowering reads
// them (cs5b3-reuse-keeps-parse).
func TestIncrementalCheckAgrees(t *testing.T) {
	seeds := []uint64{}
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		n := uint64(12)
		if testing.Short() {
			n = 3
		}
		for i := range n {
			seeds = append(seeds, i+1)
		}
	}
	deps := map[string][]string{"inc/a": nil, "inc/c": nil, "inc/b": {"inc/a"}, "main": {"inc/b", "inc/c"}}
	var reusedTotal int64
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0xc5b3))
		dir := t.TempDir()
		root := filepath.Join(dir, "cmd", "inc")
		var p incProgram
		p.write(t, dir)
		cache := NewCheckCache()
		var edits []string
		var prev map[string]string // the files of the last clean check; nil before one
		// seen are, by package, the texts of its files in the clean checks
		// so far: the cache may hold any of them.
		seen := map[string]map[string]bool{}
		for step := range 7 {
			if step > 0 {
				e := r.IntN(incFlags)
				p.flip(e)
				p.write(t, dir)
				edits = append(edits, incFlagNames[e])
			}
			fail := func(format string, args ...any) {
				t.Helper()
				t.Fatalf("seed %d after %v: %s\nReplay: ERGO_SEED=%d go test -run '^TestIncrementalCheckAgrees$' ./internal/sketchfront", seed, edits, fmt.Sprintf(format, args...), seed)
			}
			var got Analysis
			checked, reused := checkWork(func() {
				var err error
				if got, err = cache.CheckDir(root); err != nil {
					fail("%v", err)
				}
			})
			want, err := CheckDir(root)
			if err != nil {
				fail("%v", err)
			}
			if g, w := fmt.Sprint(got.Diagnostics), fmt.Sprint(want.Diagnostics); g != w {
				fail("diagnostics\n%s\nwant\n%s", g, w)
			}
			if len(want.Diagnostics) > 0 {
				prev = nil // a broken check caches nothing for its broken packages
				continue
			}
			if checked+reused != 4 {
				fail("checked %d and reused %d of 4 packages", checked, reused)
			}
			reusedTotal += reused
			gp, wp := interfaces(got.Checked()), interfaces(want.Checked())
			if !slices.Equal(gp, wp) {
				fail("interfaces differ:\n%s\nwant\n%s", strings.Join(gp, "\n"), strings.Join(wp, "\n"))
			}
			if g, w := results(t, got), results(t, want); g != w || !strings.Contains(w, "QuadIsFour:passed") {
				fail("test results %s, want %s", g, w)
			}
			if g, w := loweredGo(t, got), loweredGo(t, want); g != w {
				fail("lowered Go differs:\n%s\nwant\n%s", g, w)
			}
			// Work bounds from the model: which packages' files changed.
			now := p.files()
			if prev != nil {
				changed := map[string]bool{}
				for rel := range merge(prev, now) {
					if prev[rel] != now[rel] {
						changed[pkgOf(rel)] = true
					}
				}
				var stale func(string) bool
				stale = func(pkg string) bool {
					if changed[pkg] {
						return true
					}
					for _, d := range deps[pkg] {
						if stale(d) {
							return true
						}
					}
					return false
				}
				mustReuse, mustCheck := 0, 0
				for pkg := range deps {
					if !stale(pkg) {
						mustReuse++
					}
					if !seen[pkg][pkgText(now, pkg)] {
						mustCheck++
					}
				}
				if reused < int64(mustReuse) || checked < int64(mustCheck) {
					fail("checked %d and reused %d; at least %d must be checked and %d reused", checked, reused, mustCheck, mustReuse)
				}
			}
			prev = now
			for pkg := range deps {
				if seen[pkg] == nil {
					seen[pkg] = map[string]bool{}
				}
				seen[pkg][pkgText(now, pkg)] = true
			}
		}
	}
	if len(seeds) > 1 && reusedTotal == 0 {
		t.Error("no check took a package from the cache")
	}
}

func merge(a, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// pkgOf is the package of a module-relative file path.
func pkgOf(rel string) string {
	switch {
	case strings.HasPrefix(rel, "cmd/"):
		return "main"
	case rel == "ergo.mod":
		return "module"
	}
	return "inc/" + strings.Split(rel, "/")[0]
}

// pkgText is the text of pkg's files in files.
func pkgText(files map[string]string, pkg string) string {
	var names []string
	for rel := range files {
		if pkgOf(rel) == pkg {
			names = append(names, rel)
		}
	}
	slices.Sort(names)
	var b strings.Builder
	for _, rel := range names {
		fmt.Fprintf(&b, "%s\x00%s\x00", rel, files[rel])
	}
	return b.String()
}

func interfaces(pkgs []*check.Package) []string {
	var out []string
	for _, p := range pkgs {
		out = append(out, p.Path+"\n"+check.InterfaceText(p))
	}
	return out
}

// loweredGo is the program's lowered Go (lower.Lower), file by file: it
// reads the syntax trees the analysis hands on, which for a reused package
// are the decoded ones its Info names.
func loweredGo(t *testing.T, a Analysis) string {
	t.Helper()
	unit, err := analysisUnit(a, "")
	if err != nil {
		t.Fatal(err)
	}
	files, err := lower.Lower(unit)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "== %s\n%s\n", f.Path, f.Data)
	}
	return b.String()
}

// results are the evaluator's results of every package's tests, which
// run the bodies of the packages they import.
func results(t *testing.T, a Analysis) string {
	t.Helper()
	var out []string
	for _, pkg := range a.Checked() {
		rep, err := eval.Run(pkg, eval.DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Results {
			out = append(out, pkg.Path+"."+r.Name+":"+string(r.Status))
		}
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}
