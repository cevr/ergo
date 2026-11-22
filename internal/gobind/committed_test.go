package gobind_test

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
)

// TestCommittedBindingsAreCurrent regenerates every binding a module of
// the repository commits under its ergo.bind/ folder (hosts/mygo,
// hosts/opentui, examples/admit and any added later), with ergo bind's
// own library path (gobind.Bind in the module's root, as `ergo bind go`
// runs it), and holds the committed set to it (gobind.Stale): a generator
// change, or a Go declaration added to a bound package, without `ergo bind
// go` run again fails here. The embedded bindings of Go's standard library
// are TestGoStdBindingsMatchToolchain's; the two testdata fixtures are
// TestDrvBindingIsCurrent's and TestHostresBindingIsCurrent's. The
// packages a module binds are its files' `// ergo:bind` paths that no
// other of its bindings imports (the rest are the bindings ergo bind adds
// for the packages those name).
//
// It builds Go: gobind.Bind derives its facts from the compiler's escape
// analysis and reads the export data (go list -export) of each bound
// package on every platform, so it runs in the full tier only (needsBuild).
func TestCommittedBindingsAreCurrent(t *testing.T) {
	needsBuild(t)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	sets := committedBindings(t, root)
	if len(sets) == 0 {
		t.Fatal("no committed ergo.bind/ folder under the repository")
	}
	v, err := gobind.GoVersion(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range sortedKeys(sets) {
		committed := sets[dir]
		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		t.Run(rel, func(t *testing.T) {
			bound := boundBy(committed)
			written, err := gobind.Bind(dir, os.Environ(), v, bound...)
			if err != nil {
				t.Fatal(err)
			}
			if stale := gobind.Stale(committed, written); len(stale) > 0 {
				t.Errorf("%s/ergo.bind is not what ergo bind go writes now; run `ergo bind go %s` in %s:\n%s", rel, strings.Join(bound, " "), rel, strings.Join(stale, "\n"))
			}
		})
	}
}

// TestStaleNamesEveryChangedBinding holds gobind.Stale, the comparison
// TestCommittedBindingsAreCurrent makes, to a reference on seeded sets:
// each seed edits, drops or adds bindings at random paths of a set, and
// Stale names exactly the paths changed, in order. Mutant: the comparison
// skips a file (gobind-stale-skips-a-file). It reads no file and builds
// nothing, so it runs in the fast tier, where the full-tier test does not.
func TestStaleNamesEveryChangedBinding(t *testing.T) {
	for seed := uint64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0))
		committed := map[string]string{}
		for i := range 1 + rng.IntN(6) {
			committed[fmt.Sprintf("p%d", i)] = fmt.Sprintf("// ergo:bind p%d\nline a\nline b\n", i)
		}
		written := map[string]string{}
		for p, text := range committed {
			written[p] = text
		}
		want := map[string]string{}
		for _, p := range sortedKeys(committed) {
			switch rng.IntN(4) {
			case 0:
				written[p] = strings.Replace(written[p], "line b", "line c", 1)
				want[p] = "differs"
			case 1:
				delete(written, p)
				want[p] = "no longer writes"
			}
		}
		if rng.IntN(2) == 0 {
			written["q"] = "// ergo:bind q\n"
			want["q"] = "not committed"
		}
		got := gobind.Stale(committed, written)
		if len(got) != len(want) {
			t.Fatalf("seed %d: Stale names %d bindings, want %d (%v):\n%s", seed, len(got), len(want), want, strings.Join(got, "\n"))
		}
		for i, p := range sortedKeys(want) {
			if !strings.HasPrefix(got[i], p+": ") || !strings.Contains(got[i], want[p]) {
				t.Errorf("seed %d: line %d is %q, want %s: ... %s", seed, i+1, got[i], p, want[p])
			}
		}
	}
}

// committedBindings is each module root with an ergo.bind/ folder, and its
// bindings by the import path each one states.
func committedBindings(t *testing.T, root string) map[string]map[string]string {
	t.Helper()
	sets := map[string]map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		switch d.Name() {
		case ".git", "node_modules":
			return filepath.SkipDir
		case "ergo.bind":
			set := map[string]string{}
			err := filepath.WalkDir(p, func(f string, e fs.DirEntry, err error) error {
				if err != nil || e.IsDir() || filepath.Ext(f) != ".ergo" {
					return err
				}
				b, err := os.ReadFile(f)
				if err != nil {
					return err
				}
				path := bindPath(string(b))
				if path == "" {
					t.Errorf("%s has no // ergo:bind line", f)
					return nil
				}
				set[path] = string(b)
				return nil
			})
			sets[filepath.Dir(p)] = set
			if err != nil {
				return err
			}
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sets
}

// bindPath is the import path a binding's `// ergo:bind PATH GO PLATFORMS`
// line states.
func bindPath(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(l, "// ergo:bind "); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

// boundBy is the packages a set's ergo bind go named: its paths that no
// binding of the set imports.
func boundBy(set map[string]string) []string {
	imported := map[string]bool{}
	for _, text := range set {
		for _, l := range strings.Split(text, "\n") {
			if rest, ok := strings.CutPrefix(l, "import go "); ok {
				imported[strings.TrimSpace(rest)] = true
			}
		}
	}
	var out []string
	for _, p := range sortedKeys(set) {
		if !imported[p] {
			out = append(out, p)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
