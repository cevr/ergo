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
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
		roots, err := resolveRoots(trees, hostLink)
		if err != nil {
			t.Fatal(err)
		}
		masks, err := checkInputs(sandboxPlan{Programs: programs}, roots)
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

// TestResolvePathMatchesKernel generates trees of directories and symbolic
// links (absolute and relative, chains, links to parents, through "..",
// dangling and looping) and checks resolvePath with the host's links
// against the kernel's resolution (filepath.EvalSymlinks, which follows
// each component as open(2) does): equal for every path that exists, and
// an error exactly when the kernel loops.
func TestResolvePathMatchesKernel(t *testing.T) {
	random := rand.New(rand.NewPCG(3, 5))
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	compared := 0
	for i := range 200 {
		root := filepath.Join(base, fmt.Sprintf("case%d", i))
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		names := []string{root}
		for range 12 {
			parent := names[random.IntN(len(names))]
			if info, err := os.Lstat(parent); err != nil || !info.IsDir() {
				continue
			}
			path := filepath.Join(parent, fmt.Sprintf("e%d", len(names)))
			if random.IntN(2) == 0 {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				target := names[random.IntN(len(names))]
				switch random.IntN(4) {
				case 0:
					target, _ = filepath.Rel(parent, target)
				case 1:
					target = "../" + filepath.Base(target)
				case 2:
					target = filepath.Join(target, "missing")
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			names = append(names, path)
		}
		for _, name := range names {
			for _, path := range []string{name, filepath.Join(name, "e1"), name + "/../e2"} {
				want, kernelErr := filepath.EvalSymlinks(path)
				got, err := resolvePath(path, hostLink)
				switch {
				case kernelErr == nil:
					compared++
					if err != nil || got != want {
						t.Fatalf("case %d: resolvePath(%s) = %q, %v; the kernel resolves %q", i, path, got, err, want)
					}
				case strings.Contains(kernelErr.Error(), "too many links"):
					if err == nil {
						t.Fatalf("case %d: resolvePath(%s) = %q; the kernel loops", i, path, got)
					}
				}
			}
		}
	}
	if compared < 1000 {
		t.Fatalf("compared %d paths; the generator reaches too few", compared)
	}
}

// TestInputAdmissionResolvesLinks is TestInputContractProperty with each
// tree declared through symbolic links (spec Q-R8-1): the tree itself a
// link, absolute or relative; a parent a link; a chain of links; or no
// link. The check walks the resolved tree, so it refuses exactly when the
// resolved tree breaks the contract, and the masks are the contract's
// paths at the declared spelling, where the sandbox binds the tree. Each
// tree also holds links: to an entry inside (no mask), and out of every
// tree, absolute or relative (masked at the target, as the sandbox
// resolves it).
func TestInputAdmissionResolvesLinks(t *testing.T) {
	random := rand.New(rand.NewPCG(13, 17))
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	restorePermissions(t, base)
	symlink := func(target, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	admitted, refused := 0, 0
	spelled := map[int]int{}
	for i := range 300 {
		var trees []sandboxPath
		var resolvedTrees, units, programs []string
		var outward []string
		for j := range 2 {
			caseDir, tree := fmt.Sprintf("case%d", i), fmt.Sprintf("tree%d", j)
			resolved := filepath.Join(base, "real", caseDir, tree)
			if err := os.MkdirAll(resolved, 0o755); err != nil {
				t.Fatal(err)
			}
			declared := resolved
			spelling := random.IntN(5)
			spelled[spelling]++
			switch spelling {
			case 1: // the tree is a link
				declared = filepath.Join(base, "root-link", caseDir, tree)
				symlink(resolved, declared)
			case 2: // the tree's parent is a link
				parent := filepath.Join(base, "parent-link", caseDir)
				if _, err := os.Lstat(parent); err != nil {
					symlink(filepath.Join(base, "real", caseDir), parent)
				}
				declared = filepath.Join(parent, tree)
			case 3: // a chain of links
				hop := filepath.Join(base, "hop", caseDir, tree)
				symlink(resolved, hop)
				declared = filepath.Join(base, "chain", caseDir, tree)
				symlink(hop, declared)
			case 4: // a relative link
				declared = filepath.Join(base, "relative-link", caseDir, tree)
				symlink(filepath.Join("..", "..", "real", caseDir, tree), declared)
			}
			// Links in the tree: one in, one out absolute, one out relative.
			symlink("n0", filepath.Join(resolved, "link-in"))
			if random.IntN(2) == 0 {
				target := filepath.Join(base, "outside", caseDir, tree, "x")
				symlink(target, filepath.Join(resolved, "link-out"))
				outward = append(outward, target)
			}
			if random.IntN(2) == 0 {
				symlink(filepath.Join("..", "..", "outside-relative"), filepath.Join(resolved, "link-up"))
				outward = append(outward, filepath.Join(filepath.Dir(filepath.Dir(declared)), "outside-relative"))
			}
			rootMode, entries := generateInputTree(random)
			buildInputTree(t, resolved, rootMode, entries)
			trees = append(trees, sandboxPath{Path: declared, Maskable: random.IntN(4) > 0})
			resolvedTrees = append(resolvedTrees, resolved)
			units = append(units, inputUnits(resolved, rootMode, entries)...)
			for _, e := range entries {
				if !e.dir && random.IntN(12) == 0 {
					programs = append(programs, filepath.Join(resolved, e.rel))
				}
			}
		}
		// declaredAt is a host path of a resolved tree at its declared
		// spelling.
		declaredAt := func(host string) string {
			for j, r := range resolvedTrees {
				if within(host, r) {
					rel, _ := filepath.Rel(r, host)
					return filepath.Join(trees[j].Path, rel)
				}
			}
			t.Fatalf("%s is in no tree", host)
			return ""
		}
		violates := func(u string) bool {
			for j, r := range resolvedTrees {
				if within(u, r) && (u == r || !trees[j].Maskable) {
					return true
				}
			}
			return slices.ContainsFunc(programs, func(p string) bool { return within(p, u) })
		}
		want := slices.ContainsFunc(units, violates)
		roots, err := resolveRoots(trees, hostLink)
		if err != nil {
			t.Fatal(err)
		}
		masks, err := checkInputs(sandboxPlan{Programs: programs}, roots)
		switch {
		case want && err == nil:
			t.Fatalf("case %d: admitted, with masks %v, though a unit of %v violates the contract (trees %v)", i, masks, units, trees)
		case !want && err != nil:
			t.Fatalf("case %d: refused a contract the masks keep: %v (units %v, trees %v)", i, err, units, trees)
		case err != nil:
			refused++
			continue
		}
		admitted++
		var expected []string
		for _, u := range units {
			expected = append(expected, declaredAt(u))
		}
		expected = append(expected, outward...)
		for _, path := range expected {
			if !slices.ContainsFunc(masks, func(m sandboxMask) bool { return within(path, m.Path) }) {
				t.Fatalf("case %d: %s is visible: no mask covers it (masks %v, trees %v)", i, path, masks, trees)
			}
		}
		for _, m := range masks {
			if !slices.Contains(expected, m.Path) {
				t.Fatalf("case %d: mask %s is neither a contract path at its declared spelling nor an outward link's target (want %v)", i, m.Path, expected)
			}
		}
	}
	if admitted < 50 || refused < 50 || len(spelled) < 5 {
		t.Fatalf("admitted %d, refused %d, spellings %v: the generator does not reach every side", admitted, refused, spelled)
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

// symlinkRootPlan is this host's sandbox plan with one more read-only tree,
// declared as alias, a symbolic link to tree; the plan forbids the
// temporary directory the test's trees are in, and the sandbox binds them
// over its own.
func symlinkRootPlan(t *testing.T, alias string, maskable bool) (string, sandboxPlan) {
	t.Helper()
	root := fixture(t)
	g := wholeFixture(t, root, nil)
	plan, err := g.sandboxPlanFor([]*Step{{Name: "probe"}})
	if err != nil {
		t.Fatal(err)
	}
	plan.ReadOnly = append(plan.ReadOnly, sandboxPath{Path: alias, Reason: "a tree declared through a symbolic link", Maskable: maskable})
	resolved, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	plan.Forbidden = slices.DeleteFunc(plan.Forbidden, func(f string) bool { return within(alias, f) || within(resolved, f) })
	return root, plan
}

// TestSandboxAdmitsSymlinkRoots is round 7's verifier probe (M2): a tree
// declared as a symbolic link to a tree a process of this user wrote. Not
// maskable, the run is refused, naming the path in the resolved tree; the
// check walked what bwrap binds, not the link. Maskable, the sandbox
// starts, the written bytes are empty inside, the clean file beside them
// is visible, and bwrap's argv binds the resolved tree at the link's path.
func TestSandboxAdmitsSymlinkRoots(t *testing.T) {
	requireSandbox(t)
	base := t.TempDir()
	restorePermissions(t, base)
	tree, alias := filepath.Join(base, "tree"), filepath.Join(base, "alias")
	buildInputTree(t, tree, 0o555, []inputEntry{
		{rel: "deep", dir: true, mode: 0o755},
		{rel: "deep/result", mode: 0o644, content: "PRE_RUN_WRITABLE_BYTES"},
		{rel: "clean", mode: 0o444, content: "visible"},
	})
	if err := os.Symlink(tree, alias); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(tree)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("not maskable", func(t *testing.T) {
		_, plan := symlinkRootPlan(t, alias, false)
		box, err := startSandbox(plan)
		if err == nil {
			box.close()
			t.Fatal("the sandbox started over a symbolic-link root whose tree a process of this user wrote")
		}
		if want := filepath.Join(resolved, "deep"); !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "cannot be masked") {
			t.Fatalf("refused with %v; want the resolved path %s named, and cannot be masked", err, want)
		}
	})
	t.Run("maskable", func(t *testing.T) {
		root, plan := symlinkRootPlan(t, alias, true)
		admitted, err := admitInputs(plan)
		if err != nil {
			t.Fatal(err)
		}
		plan.Admitted = admitted
		bwrapArgv := strings.Join(sandboxArgv(plan), "\x00")
		if want := strings.Join([]string{"--ro-bind", resolved, alias}, "\x00"); !strings.Contains(bwrapArgv, want) {
			t.Errorf("argv %q does not bind the resolved tree %s at %s", sandboxArgv(plan), resolved, alias)
		}
		box, err := startSandbox(plan)
		if err != nil {
			t.Fatal(err)
		}
		defer box.close()
		argv := box.wrap([]string{"sh", "-c", `printf 'deep=%s;clean=%s;' "$(ls -A "$1/deep" 2>&1)" "$(cat "$1/clean")"`, "sh", alias}, root)
		output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		if want := "deep=;clean=visible;"; err != nil || string(output) != want {
			t.Fatalf("inside the sandbox: %v\n%s\nwant %s", err, output, want)
		}
	})
}

// TestSandboxArgvBindsAdmittedRoots: bwrap's argv binds every read-only
// tree from its resolved path (the kernel's, filepath.EvalSymlinks) at its
// declared path, and masks the targets of the trees' links that leave
// every tree, absolute or relative, with an empty read-only directory; a
// link inside a tree gets none. Inside, each outward link shows nothing.
func TestSandboxArgvBindsAdmittedRoots(t *testing.T) {
	requireSandbox(t)
	base := t.TempDir()
	restorePermissions(t, base)
	tree, alias := filepath.Join(base, "tree"), filepath.Join(base, "links", "alias")
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"out":    "/gate-outward-probe/x",
		"up":     strings.Repeat("../", 32) + "gate-outward-relative",
		"in":     "clean",
		"tmp":    "/tmp",
		"parent": "..",
	} {
		if err := os.Symlink(target, filepath.Join(tree, name)); err != nil {
			t.Fatal(err)
		}
	}
	buildInputTree(t, tree, 0o555, []inputEntry{{rel: "clean", mode: 0o444, content: "visible"}})
	if err := os.Symlink(tree, alias); err != nil {
		t.Fatal(err)
	}
	root, plan := symlinkRootPlan(t, alias, true)
	admitted, err := admitInputs(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Admitted = admitted
	bwrapArgv := sandboxArgv(plan)
	for i := range len(bwrapArgv) - 2 {
		if bwrapArgv[i] != "--ro-bind" || bwrapArgv[i+1] == "/dev/null" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(bwrapArgv[i+2]); err != nil || resolved != bwrapArgv[i+1] {
			t.Errorf("--ro-bind %s %s: the destination resolves to %q (%v), not the source", bwrapArgv[i+1], bwrapArgv[i+2], resolved, err)
		}
	}
	// ".." above the root stays at the root, so the relative link climbs
	// out of HOME and every scratch path.
	relative := "/gate-outward-relative"
	masked := map[string]bool{}
	for _, m := range admitted.Masks {
		masked[m.Path] = m.Dir
	}
	for _, want := range []string{"/gate-outward-probe/x", relative} {
		if dir, ok := masked[want]; !ok || !dir {
			t.Errorf("the outward link's target %s is not masked with a directory (masks %v)", want, admitted.Masks)
		}
		if !strings.Contains(strings.Join(bwrapArgv, "\x00"), strings.Join([]string{"--tmpfs", want, "--remount-ro", want}, "\x00")) {
			t.Errorf("argv %q does not mask %s", bwrapArgv, want)
		}
	}
	// /tmp is a scratch path, the run's own; ".." holds the tree's own
	// binding; "in" is in the tree.
	for _, path := range []string{"/tmp", filepath.Dir(alias), filepath.Join(alias, "clean")} {
		if _, ok := masked[path]; ok {
			t.Errorf("%s is masked; the sandbox shows only the run's own or checked bytes there (masks %v)", path, admitted.Masks)
		}
	}
	box, err := startSandbox(plan)
	if err != nil {
		t.Fatal(err)
	}
	defer box.close()
	argv := box.wrap([]string{"sh", "-c", `cd "$1" && printf 'out=%s;up=%s;in=%s;' "$(ls -A out/ 2>&1)" "$(ls -A up/ 2>&1)" "$(cat in)" && ! touch out/probe 2>/dev/null && echo read-only`, "sh", alias}, root)
	output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if want := "out=;up=;in=visible;read-only\n"; err != nil || string(output) != want {
		t.Fatalf("inside the sandbox: %v\n%s\nwant %s", err, output, want)
	}
}
