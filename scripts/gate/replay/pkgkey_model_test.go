package replay

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestPackageKeyModel runs generated sequences of edits, tool and host
// changes and fast, full and merge runs against a reference model and
// against the real package key, package store, tier key and merge tier, and
// requires that they agree after every run.
//
// The model holds, for each test package, the inputs as its closure rule
// says them (refInputs: the files and directories directly in each
// dependency's directory, its own testdata at any depth, the module
// lookups, go.mod and go.sum, and its declared inputs), the tool
// fingerprints and the host facts, and the passes recorded per tier. The
// oracle, after every run:
//
//	(1) a package whose closure, tool or host input changed since its last
//	    recorded pass never replays;
//	(2) a package with no change since a recorded pass replays;
//	(3) a fast record never satisfies --merge: the merge tier runs the
//	    inner (full) tier whenever no full run passed at these inputs;
//	(4) a failure is never recorded: a package that failed reruns.
func TestPackageKeyModel(t *testing.T) {
	sequences, commands := 60, 40
	if testing.Short() {
		sequences = 20
	}
	for seed := range uint64(sequences) {
		w := newPackageWorld(t, seed)
		for range commands {
			w.step()
			if t.Failed() {
				t.Fatalf("seed %d, after:\n%s", seed, strings.Join(w.log, "\n"))
			}
		}
	}
}

// modelPackages are the model's test packages: a imports b; c walks the
// examples; b is a leaf. a declares one file and a program (b's closure).
var modelPackages = []*Closure{
	{Dir: "a", Deps: []string{"b"}, Declared: []Allowance{{"docs/one.md", GrantFile}}},
	{Dir: "b"},
	{Dir: "c", Declared: []Allowance{{"examples", GrantSubtree}, {"docs/spec/*.md", GrantFiles}}},
}

// modelPaths are the files a sequence adds, edits and deletes, inside and
// outside each closure.
var modelPaths = []string{
	"go.mod", "go.sum", "top.txt", "a/a.go", "a/a_test.go", "a/testdata/x.txt", "a/testdata/deep/y.txt",
	"a/sub/n.go", "b/b.go", "b/b_test.go", "b/sub/z.txt", "c/c.go", "c/testdata/w", "d/d.go",
	"docs/one.md", "docs/two.md", "docs/spec/s.md", "docs/spec/sub/t.md", "examples/e1/main.ergo",
	"examples/e2/main.ergo", "d/go.mod",
}

type packageWorld struct {
	t    *testing.T
	rng  *rand.Rand
	root string
	// store is a real store; the model's records are below.
	store Store
	tools map[string]string
	host  map[string]string
	// passed holds the model's recorded passes: "tier package inputs" for
	// a package, "tier * inputs" for a whole run.
	passed map[string]bool
	log    []string
	clock  time.Time
	spool  string
}

func newPackageWorld(t *testing.T, seed uint64) *packageWorld {
	w := &packageWorld{t: t, rng: rand.New(rand.NewPCG(seed, seed^0x5eed)), root: t.TempDir(), store: Open(t.TempDir()), spool: t.TempDir(),
		tools: map[string]string{"go": "go1", "git": "git1", "sh": "sh1"}, host: map[string]string{"kernel release": "6.1", "cpu budget": "4"},
		passed: map[string]bool{}, clock: time.Now()}
	for _, p := range modelPaths[:12] {
		w.write(p, "v0 "+p)
	}
	return w
}

func (w *packageWorld) note(format string, args ...any) {
	w.log = append(w.log, fmt.Sprintf(format, args...))
}

func (w *packageWorld) write(name, content string) {
	full := filepath.Join(w.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *packageWorld) files() []string {
	var names []string
	_ = filepath.WalkDir(w.root, func(name string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			rel, _ := filepath.Rel(w.root, name)
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(names)
	return names
}

func (w *packageWorld) step() {
	switch w.rng.IntN(9) {
	case 0, 1:
		name := modelPaths[w.rng.IntN(len(modelPaths))]
		w.write(name, fmt.Sprintf("v%d %s", w.rng.IntN(1000), name))
		w.note("write %s", name)
	case 2:
		if files := w.files(); len(files) > 0 {
			name := files[w.rng.IntN(len(files))]
			if err := os.Remove(filepath.Join(w.root, filepath.FromSlash(name))); err != nil {
				w.t.Fatal(err)
			}
			w.note("delete %s", name)
		}
	case 3:
		if files := w.files(); len(files) > 0 {
			name := files[w.rng.IntN(len(files))]
			// The owner's executable bit is keyed, group write is not.
			bit := []fs.FileMode{0o100, 0o020}[w.rng.IntN(2)]
			full := filepath.Join(w.root, filepath.FromSlash(name))
			info, err := os.Stat(full)
			if err != nil {
				w.t.Fatal(err)
			}
			if err := os.Chmod(full, info.Mode().Perm()^bit); err != nil {
				w.t.Fatal(err)
			}
			w.note("chmod %s ^%o", name, bit)
		}
	case 4:
		tool := []string{"go", "git", "sh"}[w.rng.IntN(3)]
		w.tools[tool] = fmt.Sprintf("%s%d", tool, w.rng.IntN(3))
		w.note("tool %s = %s", tool, w.tools[tool])
	case 5:
		fact := []string{"kernel release", "cpu budget"}[w.rng.IntN(2)]
		w.host[fact] = fmt.Sprint(w.rng.IntN(3))
		w.note("host %s = %s", fact, w.host[fact])
	case 6:
		w.fastRun()
	case 7:
		w.fullRun()
	case 8:
		w.mergeRun()
	}
}

// facts are the real facts of the world, as package main collects them.
func (w *packageWorld) facts(tier string) *Facts {
	entries, recent, unreplayable, err := Walk(w.root, func(string) Class { return Tracked }, w.clock.Add(time.Hour))
	if err != nil {
		w.t.Fatal(err)
	}
	if len(recent)+len(unreplayable) > 0 {
		w.t.Fatalf("recent %v, unreplayable %v", recent, unreplayable)
	}
	f := &Facts{Executable: "gate", Root: w.root, Head: "head", Entries: entries,
		Environment: []Variable{{Name: "GOFLAGS", Set: true, Value: "-p=4"}}}
	for name, value := range w.tools {
		f.Host = append(f.Host, Fact{Name: "program " + name, Value: value})
	}
	for name, value := range w.host {
		f.Host = append(f.Host, Fact{Name: name, Value: value})
	}
	return WithTier(f, tier)
}

// refInputs is the model's statement of a package's inputs: each path it
// covers with its content and keyed permission bits, the tools and the
// host facts.
func (w *packageWorld) refInputs(c *Closure) string {
	deps := append([]string{c.Dir}, c.Deps...)
	covered := func(p string, dir bool) bool {
		parent := path.Dir(p)
		switch {
		case slices.Contains(deps, parent), slices.Contains(deps, p):
			return true
		case strings.HasPrefix(p, c.Dir+"/testdata/"), p == c.Dir+"/testdata":
			return true
		case p == "go.mod", p == "go.sum", path.Base(p) == "go.mod":
			return true
		}
		for _, d := range deps { // a directory on the way to a dependency
			if dir && strings.HasPrefix(d, p+"/") {
				return true
			}
		}
		for _, a := range c.Declared {
			switch a.Grant {
			case GrantFile:
				if p == a.Path {
					return true
				}
			case GrantSubtree:
				if a.Path == "." || p == a.Path || strings.HasPrefix(p, a.Path+"/") {
					return true
				}
			case GrantFiles:
				if ok, _ := path.Match(a.Path, p); ok {
					return true
				}
			}
		}
		return false
	}
	var b strings.Builder
	_ = filepath.WalkDir(w.root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == w.root {
			return err
		}
		rel, _ := filepath.Rel(w.root, name)
		rel = filepath.ToSlash(rel)
		if !covered(rel, entry.IsDir()) {
			return nil
		}
		if entry.IsDir() {
			fmt.Fprintf(&b, "dir %s\n", rel)
			return nil
		}
		info, _ := entry.Info()
		content, _ := os.ReadFile(name)
		fmt.Fprintf(&b, "file %s %o %q\n", rel, info.Mode().Perm()&KeyedPermissions, content)
		return nil
	})
	return b.String() + fmt.Sprint(w.tools, w.host)
}

// refWhole is the model's statement of the whole run's inputs.
func (w *packageWorld) refWhole() string {
	return w.refInputs(&Closure{Dir: "-", Declared: []Allowance{{".", GrantSubtree}}})
}

func (w *packageWorld) fastRun() {
	facts := w.facts("fast")
	packages := w.store.Packages()
	allPassed := true
	for _, c := range modelPackages {
		name := "go test ./" + c.Dir + " (fast)"
		key, problem := PackageKey(facts, c, name)
		if problem != "" {
			w.t.Fatalf("%s: %s", name, problem)
		}
		ref := "fast " + c.Dir + " " + w.refInputs(c)
		recorded, err := packages.Lookup(key, func() (*os.File, error) { return os.CreateTemp(w.spool, "spool") })
		replayed := err == nil
		if recorded != nil {
			recorded.Close()
		}
		w.note("fast %s: replayed %t, model %t", c.Dir, replayed, w.passed[ref])
		if replayed != w.passed[ref] {
			w.t.Errorf("fast %s: replayed %t, but the model has a pass at these inputs: %t", c.Dir, replayed, w.passed[ref])
		}
		if replayed {
			continue
		}
		pass := w.rng.IntN(5) > 0
		allPassed = allPassed && pass
		object, err := w.store.NewObject()
		if err != nil {
			w.t.Fatal(err)
		}
		fmt.Fprintf(object, "ok %s\n", c.Dir)
		if line := packages.RecordPackage(key, key, pass, Step{Name: name, Outcome: "ran", Passed: w.clock}, object, w.clock); strings.Contains(line, "not recorded") {
			w.t.Fatalf("%s: %s", name, line)
		}
		object.Abort()
		w.passed[ref] = pass
		w.note("fast %s ran: passed %t", c.Dir, pass)
	}
	// The fast run's whole record, under the fast tier's key.
	w.wholeRun(facts, "fast", allPassed)
}

// wholeRun records or withdraws a whole-gate run at the facts' key.
func (w *packageWorld) wholeRun(facts *Facts, tier string, pass bool) {
	run := w.store.Begin(facts)
	object, err := run.Output()
	if err != nil {
		w.t.Fatal(err)
	}
	fmt.Fprintf(object, "%s run\n", tier)
	run.Passed("step", object, "ran", 1, w.clock)
	line := run.Finish(pass, func() (*Facts, error) { return w.facts(tier), nil }, Meta{Now: w.clock})
	if pass && !strings.Contains(line, "recorded") {
		w.t.Fatalf("%s run not recorded: %s", tier, line)
	}
	w.passed[tier+" * "+w.refWhole()] = pass
}

func (w *packageWorld) fullRun() {
	pass := w.rng.IntN(5) > 0
	w.wholeRun(w.facts("full"), "full", pass)
	w.note("full run: passed %t", pass)
}

func (w *packageWorld) mergeRun() {
	full := "full * " + w.refWhole()
	innerRan := false
	tier := MergeTier{
		Store:   w.store,
		Collect: func() (*Facts, error) { return w.facts("full"), nil },
		Inner: func() bool {
			innerRan = true
			w.wholeRun(w.facts("full"), "full", true)
			return true
		},
		Uncached: func() ([]string, map[string]Verdict) {
			return []string{"step"}, map[string]Verdict{"step": Pass}
		},
	}
	expectInner := !w.passed[full]
	fastOnly := w.passed["fast * "+w.refWhole()] && expectInner
	code := tier.Run(&strings.Builder{})
	w.note("merge: inner ran %t (model: %t, a fast record only: %t), exit %d", innerRan, expectInner, fastOnly, code)
	if innerRan != expectInner {
		w.t.Errorf("merge: the inner tier ran %t; the model has a full pass at these inputs: %t (a fast record only: %t)", innerRan, w.passed[full], fastOnly)
	}
	if code != 0 {
		w.t.Errorf("merge: exit %d with equal verdicts", code)
	}
}
