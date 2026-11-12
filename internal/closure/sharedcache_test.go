package closure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The runs of one package share a Go build cache (runPlan). A cache entry
// stands for the reads of the build that made it: a run that takes a build
// from the cache does not read that build's inputs. The property models
// that with a memo in GOCACHE: the target test reads a file only when the
// memo has no entry for it, and writes the entry after the read, as the go
// command stores a build after it read the build's inputs.

const (
	// memoVariable names the target's plan file: for each subtest, the
	// checkout files it needs, by absolute path.
	memoVariable = "ERGO_CLOSURE_MEMO"
	// memoHit is the line the target prints for each file the memo had.
	memoHit = "closure memo hit"
	// cacheSeedVariable replays the property from one seed.
	cacheSeedVariable = "ERGO_CLOSURE_CACHE_SEED"
)

// TestSharedCacheTarget reads the files of each subtest in its plan
// (memoVariable) through the memo in GOCACHE. It runs only as the target
// of TestSharedCacheKeepsEachPackagesInputs.
func TestSharedCacheTarget(t *testing.T) {
	file := os.Getenv(memoVariable)
	if file == "" {
		t.Skip("the target of TestSharedCacheKeepsEachPackagesInputs")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var plan map[string][]string
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	for _, name := range slices.Sorted(maps.Keys(plan)) {
		t.Run(name, func(t *testing.T) {
			for _, path := range plan[name] {
				entry := memoEntry(os.Getenv("GOCACHE"), path)
				if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(entry); err == nil {
					fmt.Println(memoHit, path)
					continue
				}
				content, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(entry, content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// memoEntry is the memo's entry for path in the cache dir, laid out as
// the go command lays out its cache (a two-digit subdirectory, a -d file),
// so that seedCache places a std store's entries.
func memoEntry(cache, path string) string {
	sum := sha256.Sum256([]byte(path))
	name := hex.EncodeToString(sum[:8])
	return filepath.Join(cache, name[:2], name+"-d")
}

// memoCase is one generated sweep: packages, each a plan of subtests
// (subtest name, then the files it needs) and its runs (subtest names).
type memoCase struct {
	plans []map[string][]string
	runs  [][][]string
}

// memoCases generates n sweeps over files from seed. Each has two or three
// packages. A package's subtests read random files; in each sweep a file
// that the first package reads is read by a later one too (a cache shared
// across packages would hide that read from the later package), and in
// each package with more than one run one file is read in two runs (a
// cache of each run's own would miss there).
func memoCases(files []string, n int, seed uint64) []memoCase {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var cases []memoCase
	for range n {
		var c memoCase
		for p := range 2 + rng.IntN(2) {
			plan := map[string][]string{}
			var names []string
			for s := range 1 + rng.IntN(4) {
				name := fmt.Sprintf("s%d", s)
				names = append(names, name)
				for _, f := range files {
					if rng.IntN(3) == 0 {
						plan[name] = append(plan[name], f)
					}
				}
			}
			if p > 0 {
				first := c.plans[0]
				shared := first[slices.Sorted(maps.Keys(first))[0]]
				if len(shared) == 0 {
					shared = files[:1]
					first["s0"] = append(first["s0"], files[0])
				}
				plan[names[0]] = append(plan[names[0]], shared[0])
			}
			// Deal the subtests into one to len(names) runs.
			k := 1 + rng.IntN(len(names))
			runs := make([][]string, k)
			for i, name := range rng.Perm(len(names)) {
				runs[i%k] = append(runs[i%k], names[name])
			}
			if k > 1 {
				plan[runs[0][0]] = append(plan[runs[0][0]], files[len(files)-1])
				plan[runs[1][0]] = append(plan[runs[1][0]], files[len(files)-1])
			}
			c.plans = append(c.plans, plan)
			c.runs = append(c.runs, runs)
		}
		cases = append(cases, c)
	}
	return cases
}

// TestSharedCacheKeepsEachPackagesInputs: the property of runPlan's shared
// cache over generated sweeps (memoCases), each package's runs one at a
// time. The union of a package's recorded inputs is every file its
// subtests need, whichever of its runs read it: no read is hidden by a
// cache entry another package made, and none is added from another
// package. Each package's runs take, from the one cache, every file they
// need again after the first time (the hits are the reads less the
// distinct files), so the runs do share it; and the cache starts from the
// std store (seedCache), whose entry each run takes without a read. A
// failing case names its
// seed; ERGO_CLOSURE_CACHE_SEED=<seed> replays it alone.
func TestSharedCacheKeepsEachPackagesInputs(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	var files []string
	for i := range 6 {
		name := filepath.Join(f.root, "lib", fmt.Sprintf("f%d.txt", i))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("a build input"), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, name)
	}
	seeds := []uint64{2026_11_08, 2026_11_09, 2026_11_10}
	if testing.Short() {
		seeds = seeds[:1]
	}
	if value := os.Getenv(cacheSeedVariable); value != "" {
		seed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: want a seed", cacheSeedVariable, value)
		}
		seeds = []uint64{seed}
	}
	for _, seed := range seeds {
		for i, c := range memoCases(files, 2, seed) {
			checkMemoCase(t, f, self, files, c, fmt.Sprintf("seed %d case %d", seed, i))
		}
	}
}

// checkMemoCase sweeps the packages of c, in order, each through runPlan.
func checkMemoCase(t *testing.T, f fixture, self string, files []string, c memoCase, at string) {
	t.Helper()
	work := t.TempDir()
	sem := make(chan struct{}, 1)
	// A std store (seedCache) with the entry of a file outside the
	// checkout, as GOROOT is; the file does not exist, so only the
	// store's entry lets a run take it. Each package's first subtest needs
	// it, so every run that needs it hits it, the first one too.
	store := t.TempDir()
	const stdInput = "/nonexistent/closure-std-store/goroot-input"
	if err := os.MkdirAll(filepath.Dir(memoEntry(store, stdInput)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoEntry(store, stdInput), []byte("a std compile"), 0o444); err != nil {
		t.Fatal(err)
	}
	for p, given := range c.plans {
		plan := maps.Clone(given)
		first := slices.Sorted(maps.Keys(plan))[0]
		plan[first] = append(slices.Clone(plan[first]), stdInput)
		file := filepath.Join(work, fmt.Sprintf("plan%d.json", p))
		data, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			t.Fatal(err)
		}
		var runs []sweepRun
		for _, names := range c.runs[p] {
			runs = append(runs, sweepRun{args: []string{"-test.run=^TestSharedCacheTarget$/^(" + strings.Join(names, "|") + ")$"}, tests: names})
		}
		stem := filepath.Join(work, fmt.Sprintf("p%d", p))
		entries, _, err := runPlan(f.root, []string{f.root}, "internal/p", f.pkg, stem, self, runs, store, sem, []string{memoVariable + "=" + file})
		if err != nil {
			t.Fatalf("%s package %d: %v", at, p, err)
		}
		got := map[string]bool{}
		for _, e := range entries {
			if strings.HasPrefix(e.path, "lib/") {
				got[e.path] = true
			}
		}
		want := map[string]bool{}
		reads := 0
		for _, needs := range plan {
			for _, path := range needs {
				reads++
				if path == stdInput {
					continue
				}
				rel, _ := filepath.Rel(f.root, path)
				want[filepath.ToSlash(rel)] = true
			}
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s package %d (plan %v, runs %v): recorded %v, want %v", at, p, plan, c.runs[p], slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
		}
		hits := 0
		for i := range runs {
			out := stem + ".out"
			if len(runs) > 1 {
				out = fmt.Sprintf("%s.%d.out", stem, i+1)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			hits += strings.Count(string(data), memoHit)
		}
		// Each file misses once, but the store's, which every read hits.
		if want := reads - countDistinct(plan) + 1; hits != want {
			t.Errorf("%s package %d (plan %v, runs %v): %d memo hits, want %d: the runs do not share one cache seeded from the std store", at, p, plan, c.runs[p], hits, want)
		}
	}
}

// countDistinct is the number of distinct files a plan needs.
func countDistinct(plan map[string][]string) int {
	seen := map[string]bool{}
	for _, needs := range plan {
		for _, path := range needs {
			seen[path] = true
		}
	}
	return len(seen)
}
