package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// inputEntry is one entry of a generated read-only tree.
type inputEntry struct {
	rel     string
	dir     bool
	mode    os.FileMode
	content string
}

// cacheEntryName is a Go cache entry file name: 64 hex digits and -a.
var cacheEntryName = strings.Repeat("0f", 32) + "-a"

// generateInputTree is a random tree: directories and files of every
// write-bit shape (owner, group, other, none), unreadable directories, and
// Go caches marked each way cmd/go marks one (README, trim.txt,
// testexpire.txt, an entry in a two-digit directory), beside look-alikes
// that are no cache (another README, an entry name outside a two-digit
// directory).
func generateInputTree(random *rand.Rand) (rootMode os.FileMode, entries []inputEntry) {
	pick := func(modes ...os.FileMode) os.FileMode { return modes[random.IntN(len(modes))] }
	dirMode := func() os.FileMode {
		if random.IntN(3) > 0 {
			return pick(0o555, 0o755)
		}
		return pick(0o575, 0o557, 0o000, 0o111, 0o555)
	}
	fileMode := func() os.FileMode {
		if random.IntN(2) > 0 {
			return 0o444
		}
		return pick(0o644, 0o464, 0o446, 0o440, 0o444)
	}
	var grow func(prefix string, depth int)
	grow = func(prefix string, depth int) {
		for i := range 1 + random.IntN(3) {
			name := filepath.Join(prefix, fmt.Sprintf("n%d", i))
			switch kind := random.IntN(9); {
			case kind < 3 && depth < 3:
				entries = append(entries, inputEntry{rel: name, dir: true, mode: dirMode()})
				grow(name, depth+1)
			case kind == 3:
				entries = append(entries, inputEntry{rel: name, dir: true, mode: dirMode()})
				switch random.IntN(5) {
				case 0:
					entries = append(entries, inputEntry{rel: filepath.Join(name, "README"), mode: 0o444, content: goCacheReadme + "\n"})
				case 1:
					entries = append(entries, inputEntry{rel: filepath.Join(name, "trim.txt"), mode: 0o444, content: "1\n"})
				case 2:
					entries = append(entries, inputEntry{rel: filepath.Join(name, "testexpire.txt"), mode: 0o444, content: "1\n"})
				default:
					entries = append(entries, inputEntry{rel: filepath.Join(name, "0f"), dir: true, mode: 0o555},
						inputEntry{rel: filepath.Join(name, "0f", cacheEntryName), mode: 0o444, content: "entry"})
				}
			case kind == 4:
				entries = append(entries, inputEntry{rel: filepath.Join(prefix, "README"), mode: 0o444, content: "Read me.\n"},
					inputEntry{rel: filepath.Join(prefix, cacheEntryName), mode: 0o444, content: "no cache"})
			default:
				entries = append(entries, inputEntry{rel: name, mode: fileMode(), content: "data"})
			}
		}
	}
	grow("", 0)
	rootMode = 0o555
	if random.IntN(8) == 0 {
		rootMode = pick(0o755, 0o000, 0o575)
	}
	return rootMode, entries
}

// buildInputTree writes a generated tree at root, modes last, deepest
// first.
func buildInputTree(t *testing.T, root string, rootMode os.FileMode, entries []inputEntry) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		path := filepath.Join(root, e.rel)
		if e.dir {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte(e.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if err := os.Chmod(filepath.Join(root, entries[i].rel), entries[i].mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(root, rootMode); err != nil {
		t.Fatal(err)
	}
}

// restorePermissions lets the test's cleanup remove a generated tree.
func restorePermissions(t *testing.T, base string) {
	t.Cleanup(func() {
		if err := emptyStepTempDir(base); err != nil {
			t.Errorf("remove %s: %v", base, err)
		}
	})
}

// inputUnits are the paths of a generated tree the contract requires
// masked, by the rule stated independently of checkInputs: an entry with a
// write bit (this user owns every generated entry and is in its group), a
// directory this user cannot list, and the root of each Go cache.
func inputUnits(root string, rootMode os.FileMode, entries []inputEntry) []string {
	units := map[string]bool{}
	unit := func(mode os.FileMode, dir bool, path string) {
		if mode&0o222 != 0 || dir && mode&0o500 != 0o500 {
			units[path] = true
		}
	}
	unit(rootMode, true, root)
	for _, e := range entries {
		path := filepath.Join(root, e.rel)
		unit(e.mode, e.dir, path)
		name := filepath.Base(e.rel)
		parent := filepath.Dir(path)
		switch {
		case name == "trim.txt" || name == "testexpire.txt" || name == "README" && strings.HasPrefix(e.content, goCacheReadme):
			units[parent] = true
		case name == cacheEntryName && filepath.Base(parent) == "0f":
			units[filepath.Dir(parent)] = true
		}
	}
	return slices.Sorted(func(yield func(string) bool) {
		for u := range units {
			if !yield(u) {
				return
			}
		}
	})
}

// TestInputContractProperty generates read-only trees, maskable or not,
// with programs inside, and checks checkInputs against the contract: it
// refuses exactly when a path the contract requires masked is a tree's
// root, is in a tree that may not be masked, or holds a program; and when
// it admits, every such path is inside a mask, every mask is such a path,
// and no mask hides a program.
func TestInputContractProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(7, 11))
	base := t.TempDir()
	restorePermissions(t, base)
	admitted, masked, refused := 0, 0, 0
	for i := range 300 {
		var trees []sandboxPath
		var units, programs []string
		for j := range 2 {
			root := filepath.Join(base, fmt.Sprintf("case%d", i), fmt.Sprintf("tree%d", j))
			rootMode, entries := generateInputTree(random)
			buildInputTree(t, root, rootMode, entries)
			trees = append(trees, sandboxPath{Path: root, Maskable: random.IntN(4) > 0})
			units = append(units, inputUnits(root, rootMode, entries)...)
			for _, e := range entries {
				if !e.dir && random.IntN(12) == 0 {
					programs = append(programs, filepath.Join(root, e.rel))
				}
			}
		}
		violates := func(u string) bool {
			for _, tree := range trees {
				if within(u, tree.Path) && (u == tree.Path || !tree.Maskable) {
					return true
				}
			}
			return slices.ContainsFunc(programs, func(p string) bool { return within(p, u) })
		}
		want := slices.ContainsFunc(units, violates)
		masks, err := checkInputs(trees, programs)
		switch {
		case want && err == nil:
			t.Fatalf("case %d: admitted, with masks %v, though a unit of %v violates the contract (programs %v, trees %v)", i, masks, units, programs, trees)
		case !want && err != nil:
			t.Fatalf("case %d: refused a contract the masks keep: %v (units %v, programs %v)", i, err, units, programs)
		case err != nil:
			refused++
			continue
		}
		admitted++
		if len(masks) > 0 {
			masked++
		}
		for _, u := range units {
			if !slices.ContainsFunc(masks, func(m sandboxMask) bool { return within(u, m.Path) }) {
				t.Fatalf("case %d: %s is visible: no mask covers it (masks %v)", i, u, masks)
			}
		}
		for _, m := range masks {
			if !slices.Contains(units, m.Path) {
				t.Fatalf("case %d: mask %s hides a path the contract admits (units %v)", i, m.Path, units)
			}
		}
	}
	if masked < 50 || refused < 50 {
		t.Fatalf("admitted %d (%d with masks), refused %d: the generator does not reach every side", admitted, masked, refused)
	}
}

// TestSandboxMasksContaminatedInputs starts the sandbox over a read-only
// tree that holds a user-writable directory and file, a group-writable
// file and a Go cache, and reads each inside: every one is empty, and the
// clean file beside them is visible.
func TestSandboxMasksContaminatedInputs(t *testing.T) {
	requireSandbox(t)
	base := t.TempDir()
	restorePermissions(t, base)
	tree := filepath.Join(base, "tree")
	buildInputTree(t, tree, 0o555, []inputEntry{
		{rel: "rw-dir", dir: true, mode: 0o755},
		{rel: "rw-dir/result", mode: 0o444, content: "stale"},
		{rel: "rw-file", mode: 0o644, content: "stale"},
		{rel: "group-file", mode: 0o464, content: "stale"},
		{rel: "gocache", dir: true, mode: 0o555},
		{rel: "gocache/README", mode: 0o444, content: goCacheReadme + "\n"},
		{rel: "gocache/0f", dir: true, mode: 0o555},
		{rel: "gocache/0f/" + cacheEntryName, mode: 0o444, content: "stale"},
		{rel: "clean", mode: 0o444, content: "visible"},
	})
	root := fixture(t)
	g := wholeFixture(t, root, nil)
	plan, err := g.sandboxPlanFor([]*Step{{Name: "probe"}})
	if err != nil {
		t.Fatal(err)
	}
	plan.ReadOnly = append(plan.ReadOnly, sandboxPath{Path: tree, Reason: "a contaminated test tree", Maskable: true})
	// The test tree is in the temporary directory, which the plan forbids
	// as a read-only path; the sandbox binds it over its own.
	plan.Forbidden = slices.DeleteFunc(plan.Forbidden, func(f string) bool { return within(tree, f) })
	box, err := startSandbox(plan)
	if err != nil {
		t.Fatal(err)
	}
	defer box.close()
	argv := box.wrap([]string{"sh", "-c", `cd "$1" && for name in rw-dir rw-file group-file gocache clean; do printf '%s=%s;' "$name" "$(cat "$name" 2>/dev/null; ls -A "$name" 2>/dev/null | grep -v "^$name\$")"; done`, "sh", tree}, root)
	output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if want := "rw-dir=;rw-file=;group-file=;gocache=;clean=visible;"; err != nil || string(output) != want {
		t.Fatalf("inside the sandbox: %v\n%s\nwant %s", err, output, want)
	}
}
