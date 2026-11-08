package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestNoTierRunsASweepBesideAWriter enumerates the steps of each tier (the
// fast tier, the full tier, and the merge tier's uncached run of the full
// tier's steps) and fails if a step that writes the checkout can run while a
// closure sweep runs: every sweep starts only after every writer has ended.
// The CLI's build writes bin/ergo, so it declares bin/ (batch-9 try4: the
// merge tier's sweeps of internal/bindc, internal/sketchfront and
// scripts/gate/replay failed with "bin/ergo changed during").
func TestNoTierRunsASweepBesideAWriter(t *testing.T) {
	host := sweepOS
	t.Cleanup(func() { sweepOS = host })
	sweepOS = "linux"
	root := checkoutRoot(t)
	full, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	var merge []*Step
	for _, step := range full {
		merge = append(merge, uncachedStep(step))
	}
	for tier, steps := range map[string][]*Step{fastTier: fast, fullTier: full, mergeTierName: merge} {
		index := map[string]int{}
		for i, step := range steps {
			index[step.Name] = i
		}
		after, err := sweepsAfterWriters(steps, index)
		if err != nil {
			t.Fatalf("%s tier: %v", tier, err)
		}
		build, ok := index[buildCLIStep]
		if !ok {
			t.Fatalf("%s tier: no %q step", tier, buildCLIStep)
		}
		if !slices.Contains(steps[build].Writes, "bin/") {
			t.Errorf("%s tier: %q writes bin/ergo and declares Writes %v; want bin/", tier, buildCLIStep, steps[build].Writes)
		}
		sweeps := 0
		for i, step := range steps {
			if step.Sweep == "" {
				continue
			}
			sweeps++
			for j, writer := range steps {
				if len(writer.Writes) > 0 && !slices.Contains(after[i], j) {
					t.Errorf("%s tier: %s can run beside %s, which writes %v", tier, step.Name, writer.Name, writer.Writes)
				}
			}
		}
		if sweeps < 30 {
			t.Errorf("%s tier: %d sweep steps; want one per test package", tier, sweeps)
		}
	}
}

// sweepFixture is a gate fixture with two step slots and a directory
// outside the checkout where its steps leave signals to each other.
func sweepFixture(t *testing.T) (root, signals string) {
	t.Helper()
	t.Setenv(cpuVariable, "2")
	return fixture(t), t.TempDir()
}

// waitFor is a shell loop that waits up to 10 s for a file, then fails.
func waitFor(file string) string {
	return `n=0; while [ ! -e '` + file + `' ]; do n=$((n+1)); [ $n -gt 1000 ] && { echo "no ` + filepath.Base(file) + `"; exit 1; }; sleep 0.01; done`
}

// TestSweepFailsOnAnUndeclaredWriter: a step that writes the checkout and
// does not say so (no Writes) runs beside a sweep, and writes a file while
// the sweep runs: bin/x, a path Git ignores, or a golden file in a testdata
// directory, as a test's update mode does. The sweep fails and names the
// path, though nothing it ran read it.
func TestSweepFailsOnAnUndeclaredWriter(t *testing.T) {
	for _, path := range []string{"bin/x", "internal/pkg/testdata/golden.txt"} {
		t.Run(path, func(t *testing.T) {
			root, signals := sweepFixture(t)
			started, wrote := filepath.Join(signals, "started"), filepath.Join(signals, "wrote")
			steps := []*Step{
				{Name: "writer", Argv: []string{"sh", "-c", waitFor(started) + `; mkdir -p '` + filepath.Dir(path) + `' && echo x > '` + path + `' && touch '` + wrote + `'`}},
				{Name: "sweep", Sweep: "pkg", Argv: []string{"sh", "-c", `touch '` + started + `'; ` + waitFor(wrote)}},
			}
			code, out := runGate(t, root, t.TempDir(), steps)
			if code != 1 {
				t.Fatalf("exit %d, want 1\n%s", code, out)
			}
			for _, want := range []string{"== writer: ok", "== sweep: FAILED", "the checkout changed while the sweep ran", path} {
				if !strings.Contains(out, want) {
					t.Fatalf("output lacks %q:\n%s", want, out)
				}
			}
			// A change outside the checkout (the signals) is not the checkout's.
			if strings.Contains(out, signals) {
				t.Fatalf("the sweep names a path outside the checkout:\n%s", out)
			}
		})
	}
}

// TestSweepStartsAfterEveryWriter: a sweep starts only after every step
// that declares Writes has ended, one that failed included, and is not
// blocked by it; a sweep with no writer beside it passes.
func TestSweepStartsAfterEveryWriter(t *testing.T) {
	root, signals := sweepFixture(t)
	first, second := filepath.Join(signals, "first"), filepath.Join(signals, "second")
	steps := []*Step{
		{Name: "sweep", Sweep: "pkg", Argv: []string{"sh", "-c", `for f in '` + first + `' '` + second + `'; do [ -e "$f" ] || { echo "started before $f"; exit 1; }; done; echo swept`}},
		{Name: "writer", Writes: []string{"bin/"}, Argv: []string{"sh", "-c", `sleep 0.3; mkdir -p bin && echo x > bin/x && touch '` + first + `'`}},
		{Name: "failing writer", Writes: []string{"gen/"}, Argv: []string{"sh", "-c", `sleep 0.2; mkdir -p gen && echo x > gen/x && touch '` + second + `'; exit 3`}},
	}
	code, out := runGate(t, root, t.TempDir(), steps)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (the failing writer)\n%s", code, out)
	}
	for _, want := range []string{"== sweep: ok", "swept", "== writer: ok", "== failing writer: FAILED", ": failing writer\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestSweepWaitingForItselfIsRefused: a step that sweeps and writes, or a
// writer that needs a sweep through its needs, would wait for itself; the
// gate refuses the steps before it runs any.
func TestSweepWaitingForItselfIsRefused(t *testing.T) {
	cases := map[string][]*Step{
		"sweeps and writes": {{Name: "both", Sweep: "pkg", Writes: []string{"bin/"}}},
		"writer needs a sweep": {
			{Name: "sweep", Sweep: "pkg"},
			{Name: "middle", Needs: []string{"sweep"}},
			{Name: "writer", Needs: []string{"middle"}, Writes: []string{"bin/"}},
		},
	}
	for name, steps := range cases {
		index := map[string]int{}
		for i, step := range steps {
			index[step.Name] = i
		}
		if _, err := sweepsAfterWriters(steps, index); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	steps := []*Step{{Name: "sweep", Sweep: "pkg"}, {Name: "writer", Needs: []string{"other"}, Writes: []string{"bin/"}}, {Name: "other"}}
	after, err := sweepsAfterWriters(steps, map[string]int{"sweep": 0, "writer": 1, "other": 2})
	if err != nil || !slices.Equal(after[0], []int{1}) || after[1] != nil || after[2] != nil {
		t.Errorf("after = %v, %v; want the sweep after the writer only", after, err)
	}
}

// TestCheckoutEntriesSeeEveryChange: the list a sweep compares sees a file
// added in an ignored directory, a removed file, a file changed to the same
// size (by its time), a mode change and git's index lock, and not a write
// in a subdirectory of .git, which a linked worktree's commit makes.
func TestCheckoutEntriesSeeEveryChange(t *testing.T) {
	root := fixture(t)
	write(t, root, "keep.txt", "aaaa\n")
	write(t, root, "gone.txt", "gone\n")
	write(t, root, "mode.sh", "true\n")
	before, err := checkoutEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before[".git/HEAD"]; !ok {
		t.Fatalf("no .git/HEAD in %d entries", len(before))
	}
	write(t, root, ".git/objects/ab/cdef", "an object\n")
	again, err := checkoutEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed := changedEntries(before, again); len(changed) != 0 {
		t.Fatalf("a write below .git/objects shows as %v", changed)
	}
	write(t, root, "bin/x", "x\n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "keep.txt", "bbbb\n")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "keep.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, ".git/index.lock", "")
	after, err := checkoutEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	changed := changedEntries(before, after)
	// Not "." or ".git": a directory's time moves only when the clock
	// ticked between the lists (head-all run of round 4: the kernel's
	// coarse clock gave both lists one time); its new entries show.
	for _, want := range []string{".git/index.lock", "bin", "bin/x", "gone.txt", "keep.txt", "mode.sh"} {
		if !slices.Contains(changed, want) {
			t.Errorf("changed %v lacks %s", changed, want)
		}
	}
	for _, path := range changed {
		if strings.HasPrefix(path, ".git/objects") {
			t.Errorf("changed %v holds %s", changed, path)
		}
	}
}

// listingSeedOffsetVariable moves TestCheckoutEntriesSeeAnyPath's seeds:
// with it set to n the sequences are seeds n, n+1, …, so a failing seed
// replays alone and a check can run other sequences. The gate never sets
// it: its runs use seeds from 0.
const listingSeedOffsetVariable = "ERGO_LISTING_SEED_OFFSET"

// listingNames are the directory names the property draws from: the names
// a listing that skips a kind of directory would skip (testdata, bin, docs,
// a directory the fixture's .gitignore ignores, dot names, names that start
// with an underscore) and generated names.
var listingNames = []string{"testdata", "bin", "docs", "runs", "vendor", ".cache", ".github", "_build", "internal", "examples", "cmd"}

// TestCheckoutEntriesSeeAnyPath is a property over generated checkout
// trees: a file added, removed or rewritten to another size, at a random
// depth under random directory names (testdata among them), shows in
// changedEntries between the two lists. Round 4 of b9-fix-gate found that a
// listing that skips every testdata directory passed every other test.
func TestCheckoutEntriesSeeAnyPath(t *testing.T) {
	offset := 0
	if text := os.Getenv(listingSeedOffsetVariable); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil {
			t.Fatalf("%s=%q: %v", listingSeedOffsetVariable, text, err)
		}
		offset = n
	}
	const sequences, steps = 20, 15
	for seed := offset; seed < offset+sequences; seed++ {
		random := rand.New(rand.NewPCG(uint64(seed), 0))
		root := fixture(t)
		var files []string
		made := 0
		name := func() string {
			if random.IntN(3) == 0 {
				return fmt.Sprintf("d%d", random.IntN(1000))
			}
			return listingNames[random.IntN(len(listingNames))]
		}
		newPath := func() string {
			parts := make([]string, 1+random.IntN(5))
			for i := range parts {
				parts[i] = name()
			}
			// A file's name ends in .f and a directory's never does, so a
			// path is never both; the count makes each file new.
			made++
			parts[len(parts)-1] += fmt.Sprintf("-%d.f", made)
			return strings.Join(parts, "/")
		}
		for range 10 {
			path := newPath()
			write(t, root, path, "seed\n")
			files = append(files, path)
		}
		var trace []string
		for range steps {
			before, err := checkoutEntries(root)
			if err != nil {
				t.Fatal(err)
			}
			var path, action string
			switch choice := random.IntN(3); {
			case choice == 0:
				path, action = newPath(), "add"
				write(t, root, path, "added\n")
				files = append(files, path)
			case choice == 1 && len(files) > 1:
				i := random.IntN(len(files))
				path, action = files[i], "remove"
				if err := os.Remove(filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
				files = slices.Delete(files, i, i+1)
			default:
				path, action = files[random.IntN(len(files))], "rewrite"
				content, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				write(t, root, path, string(content)+"more\n")
			}
			trace = append(trace, action+" "+path)
			after, err := checkoutEntries(root)
			if err != nil {
				t.Fatal(err)
			}
			if changed := changedEntries(before, after); !slices.Contains(changed, path) {
				t.Fatalf("seed %d (replay: %s=%d): changed %v lacks %s after: %s", seed, listingSeedOffsetVariable, seed, changed, path, strings.Join(trace, "; "))
			}
		}
	}
}

// TestCheckoutEntriesIgnoreLinkedWorktrees: in a main checkout, whose .git
// is the common directory of every worktree, git commands run in a linked
// worktree of the same repository write the files of .git that all
// worktrees share: a commit takes .git/packed-refs.lock (which moves the
// time of .git), pack-refs and gc write .git/packed-refs, setting an
// upstream writes .git/config, and adding a worktree adds an entry under
// .git/worktrees (which moves the time of .git). None of these is a step
// of this checkout, so the listing does not show them (round-4 U1 of
// b9-fix-gate). The files that are this checkout's own, its index and
// HEAD, still show.
func TestCheckoutEntriesIgnoreLinkedWorktrees(t *testing.T) {
	root := fixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitIn(t, root, "branch", "upstream")
	gitIn(t, root, "worktree", "add", "-q", "-b", "lane", linked)
	// A packed-refs file exists in a repository that has packed once; a
	// commit then takes its lock.
	gitIn(t, linked, "pack-refs", "--all")
	before, err := checkoutEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	// A directory's time moves only when the kernel's file clock ticks
	// between two writes (a few milliseconds).
	time.Sleep(20 * time.Millisecond)
	gitIn(t, linked, "commit", "-q", "--allow-empty", "-m", "a lane's commit")
	gitIn(t, linked, "pack-refs", "--all")
	gitIn(t, linked, "gc", "-q")
	gitIn(t, linked, "branch", "--set-upstream-to=upstream", "lane")
	gitIn(t, linked, "worktree", "add", "-q", "-b", "second", filepath.Join(t.TempDir(), "second"))
	after, err := checkoutEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed := changedEntries(before, after); len(changed) != 0 {
		t.Fatalf("git commands in a linked worktree change the main checkout's listing: %v", changed)
	}
	write(t, root, ".git/index", "not an index\n")
	write(t, root, ".git/ORIG_HEAD", "0000000000000000000000000000000000000000\n")
	// Only .git's own time is left out: a file made and removed in another
	// directory still moves that directory's time.
	write(t, root, "docs/gone.md", "gone\n")
	if err := os.Remove(filepath.Join(root, "docs/gone.md")); err != nil {
		t.Fatal(err)
	}
	again, err := checkoutEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	changed := changedEntries(after, again)
	for _, want := range []string{".git/index", ".git/ORIG_HEAD", "docs"} {
		if !slices.Contains(changed, want) {
			t.Errorf("changed %v lacks %s", changed, want)
		}
	}
}
