package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// wsTree is a generated workspace: the files to write and what discovery
// must find in it.
type wsTree struct {
	files   map[string]string // slash path below the root -> text
	members []string          // the members' folders, sorted ("." for the root)
	names   map[string]string // member folder -> module name
	edges   [][2]string       // member folder -> member folder, sorted, once
	pkgs    map[string]string // package folder -> its member's folder
	// diverge are the requirements on another member's Go module by
	// version with no replace: "from -> to path version", in member then
	// line order.
	diverge []string
}

// genWorkspace draws a tree of modules: members at nested folders (a
// member inside another too), each with zero to two packages; go.mod
// replace lines to other members (an edge), to a folder that is no
// member, and to a remote version (neither is an edge), written in
// single-line and block form; and decoy modules where discovery must
// not look: testdata, hidden, underscored, node_modules, vendor and,
// with an ergo.work, a skipped folder. A replace only points at an
// earlier member, so the edges are acyclic and the order is defined.
func genWorkspace(r *rand.Rand) wsTree {
	t := wsTree{files: map[string]string{}, names: map[string]string{}, pkgs: map[string]string{}}
	words := []string{"app", "lib", "svc", "core", "tool", "web"}
	n := 1 + r.IntN(6)
	var dirs []string
	for i := 0; i < n; i++ {
		var dir string
		switch {
		case i == 0 && r.IntN(4) == 0:
			dir = "."
		case len(dirs) > 0 && r.IntN(3) == 0 && dirs[len(dirs)-1] != ".":
			dir = dirs[r.IntN(len(dirs))] + "/" + words[r.IntN(len(words))] + fmt.Sprint(i)
		default:
			dir = words[r.IntN(len(words))] + fmt.Sprint(i)
		}
		dir = strings.TrimPrefix(dir, "./")
		if slices.Contains(dirs, dir) {
			continue
		}
		dirs = append(dirs, dir)
	}
	at := func(dir, name string) string {
		if dir == "." {
			return name
		}
		return dir + "/" + name
	}
	seen := map[[2]string]bool{}
	hasGoMod := map[int]bool{}
	for i, dir := range dirs {
		name := fmt.Sprintf("m%d", i)
		t.members = append(t.members, dir)
		t.names[dir] = name
		t.files[at(dir, ModFile)] = "module " + name + "\n"
		for p := 0; p < r.IntN(3); p++ {
			pkg := at(dir, fmt.Sprintf("p%d", p))
			t.files[pkg+"/p.ergo"] = fmt.Sprintf("func F%d() int { %d }\n", p, p)
			t.pkgs[pkg] = dir
		}
		var replaces, requires []string
		replaced := map[string]bool{}
		for k := 0; k < r.IntN(3); k++ {
			// A requirement of an earlier member's Go module by version:
			// replaced below or not, a divergence when not.
			if i > 0 {
				j := r.IntN(i)
				requires = append(requires, fmt.Sprintf("example.com/m%d v1.%d.0", j, k))
			}
		}
		for k := 0; k < r.IntN(4); k++ {
			switch {
			case i > 0 && r.IntN(3) > 0:
				j := r.IntN(i)
				rel, _ := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(dirs[j]))
				rel = filepath.ToSlash(rel)
				if !strings.HasPrefix(rel, "../") {
					rel = "./" + rel
				}
				replaces = append(replaces, fmt.Sprintf("example.com/m%d => %s", j, rel))
				replaced[fmt.Sprintf("example.com/m%d", j)] = true
				e := [2]string{dir, dirs[j]}
				if !seen[e] {
					seen[e] = true
					t.edges = append(t.edges, e)
				}
			case r.IntN(2) == 0:
				replaces = append(replaces, "example.com/other => ./notamodule")
				t.files[at(dir, "notamodule/README")] = "not a module\n"
			default:
				replaces = append(replaces, "example.com/remote => example.com/fork v1.2.3")
			}
		}
		if len(replaces) > 0 || len(requires) > 0 || r.IntN(2) == 0 {
			gomod := fmt.Sprintf("module example.com/m%d\n\ngo 1.27\n", i)
			if len(requires) == 1 {
				gomod += "require " + requires[0] + "\n"
			} else if len(requires) > 1 {
				gomod += "require (\n\t" + strings.Join(requires, "\n\t") + "\n)\n"
			}
			for _, q := range requires {
				path, version, _ := strings.Cut(q, " ")
				var j int
				fmt.Sscanf(path, "example.com/m%d", &j)
				if !replaced[path] && hasGoMod[j] {
					t.diverge = append(t.diverge, dir+" -> "+dirs[j]+" "+path+" "+version)
				}
			}
			if r.IntN(2) == 0 {
				for _, l := range replaces {
					gomod += "replace " + l + "\n"
				}
			} else if len(replaces) > 0 {
				gomod += "replace (\n\t" + strings.Join(replaces, "\n\t") + "\n)\n"
			}
			t.files[at(dir, GoModFile)] = gomod
			hasGoMod[i] = true
		}
	}
	decoys := []string{"testdata", ".hidden", "_draft", "node_modules", "vendor"}
	for k := 0; k < 1+r.IntN(3); k++ {
		base := dirs[r.IntN(len(dirs))]
		decoy := at(base, decoys[r.IntN(len(decoys))]) + "/copy"
		t.files[decoy+"/"+ModFile] = "module decoy\n"
		t.files[decoy+"/x/x.ergo"] = "func X() int { 0 }\n"
	}
	if r.IntN(2) == 0 {
		t.files[WorkFile] = "// the workspace's file\nversion 1\n\nskip skipped/away // left out\n"
		t.files["skipped/away/"+ModFile] = "module skippedaway\n"
		t.files["skipped/away/y/y.ergo"] = "func Y() int { 0 }\n"
	}
	slices.Sort(t.members)
	// Discovery reports divergences in member order (the folders sorted),
	// each member's in its go.mod's line order: requires are written in
	// draw order, so a stable sort by the from folder is that order.
	slices.SortStableFunc(t.diverge, func(a, b string) int {
		fa, _, _ := strings.Cut(a, " -> ")
		fb, _, _ := strings.Cut(b, " -> ")
		return strings.Compare(fa, fb)
	})
	slices.SortFunc(t.edges, func(a, b [2]string) int {
		if c := strings.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return strings.Compare(a[1], b[1])
	})
	return t
}

func (t wsTree) write(tb testing.TB, root string) {
	for name, text := range t.files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
}

func (t wsTree) source() string {
	var names []string
	for n := range t.files {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "-- %s --\n%s", n, t.files[n])
	}
	return b.String()
}

// TestWorkspaceDiscoveryAgrees is WS1's property (MONOREPO §7.2): over
// drawn trees of modules, DiscoverWorkspace finds exactly the generator's
// members, names and edges, and DiscoverPackages at the root selects every
// member's packages once, a member after each member it resolves locally
// (a root that is itself a module, with no ergo.work, selects its own).
// A failing seed prints the tree and the replay command.
//
// It kills ws-walks-testdata, ws-drops-replace-edge, ws-member-twice,
// ws-skip-ignored and ws-order-ignores-edges.
func TestWorkspaceDiscoveryAgrees(t *testing.T) {
	for seed := uint64(1); seed <= 60; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x57))
		tree := genWorkspace(r)
		root := t.TempDir()
		tree.write(t, root)
		root, _ = filepath.EvalSymlinks(root)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\ntree:\n%s\nreplay: go test ./internal/sketchfront -run TestWorkspaceDiscoveryAgrees", seed, fmt.Sprintf(format, args...), tree.source())
		}
		ws, err := DiscoverWorkspace(nil, root)
		if err != nil {
			fail("DiscoverWorkspace: %v", err)
		}
		var members []string
		for _, m := range ws.Members {
			members = append(members, m.Rel)
			if want := tree.names[m.Rel]; m.Module != want || m.Problem != "" {
				fail("member %s is module %q (problem %q), want %q", m.Rel, m.Module, m.Problem, want)
			}
			if m.Dir != filepath.Join(root, filepath.FromSlash(m.Rel)) {
				fail("member %s has the folder %s", m.Rel, m.Dir)
			}
		}
		if !slices.Equal(members, tree.members) {
			fail("members %v, want %v", members, tree.members)
		}
		var edges [][2]string
		for _, e := range ws.Edges {
			edges = append(edges, [2]string{ws.Members[e.From].Rel, ws.Members[e.To].Rel})
		}
		if !slices.Equal(edges, tree.edges) {
			fail("edges %v, want %v", edges, tree.edges)
		}
		var diverge []string
		for _, d := range ws.Divergences {
			diverge = append(diverge, ws.Members[d.From].Rel+" -> "+ws.Members[d.To].Rel+" "+d.Path+" "+d.Version)
		}
		if !slices.Equal(diverge, tree.diverge) {
			fail("divergences %v, want %v", diverge, tree.diverge)
		}
		// A root that is a module, with no ergo.work, is that module:
		// ./... there is its packages alone, as before workspaces.
		want := tree.pkgs
		if _, work := tree.files[WorkFile]; !work && tree.names["."] != "" {
			want = map[string]string{}
			for p, m := range tree.pkgs {
				if m == "." {
					want[p] = m
				}
			}
		}
		if len(want) == 0 {
			if _, err := DiscoverPackages(root); err == nil {
				fail("DiscoverPackages selected packages in a workspace with none")
			}
			continue
		}
		targets, err := DiscoverPackages(root)
		if err != nil {
			fail("DiscoverPackages: %v", err)
		}
		position := map[string]int{}
		for i, target := range targets {
			rel, _ := filepath.Rel(root, target.Dir)
			rel = filepath.ToSlash(rel)
			if _, twice := position[rel]; twice {
				fail("the package %s is selected twice", rel)
			}
			position[rel] = i
			member, ok := want[rel]
			if !ok {
				fail("the package %s is not selected at the root", rel)
			}
			inside := rel
			if member != "." {
				inside = strings.TrimPrefix(rel, member+"/")
			}
			if want := tree.names[member] + "/" + inside; target.Path != want {
				fail("the package %s has the path %s, want %s", rel, target.Path, want)
			}
		}
		if len(position) != len(want) {
			fail("selected %d packages, want %d", len(position), len(want))
		}
		for _, e := range tree.edges {
			for pf, mf := range want {
				for pt, mt := range want {
					if mf == e[0] && mt == e[1] && position[pt] > position[pf] {
						fail("%s (of %s) is selected before %s (of %s), which it resolves locally", pf, e[0], pt, e[1])
					}
				}
			}
		}
	}
}

// TestWorkFileRefusesWhatItCannotRead is ergo.work's rule (build.md,
// Workspace): an unknown key, a newer version, a missing version line and
// a skip that is not a clean folder below the root fail, naming the line,
// never ignored. It catches ws-work-unknown-key-ignored and
// ws-work-newer-version-read.
func TestWorkFileRefusesWhatItCannotRead(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"version 1\nskip a\ntasks build\n", "ergo.work:3: unknown key tasks"},
		{"version 2\nskip a\n", "ergo.work:1: version 2 needs a newer ergo"},
		{"skip a\n", "ergo.work:1: the first line is version N"},
		{"", "ergo.work: the first line is version N"},
		{"version 1\nskip ../out\n", "ergo.work:2: skip ../out is not a clean"},
		{"version 1\nskip a/./b\n", "ergo.work:2: skip a/./b is not a clean"},
	} {
		if _, err := parseWorkFile(c.text); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("parseWorkFile(%q): %v, want %q", c.text, err, c.want)
		}
	}
	skips, err := parseWorkFile("// c\nversion 1\nskip a/b // why\nskip c\n")
	if err != nil || len(skips) != 2 || skips[0].dir != "a/b" || skips[1].line != 4 {
		t.Errorf("a valid file read as %v, %v", skips, err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, WorkFile), []byte("version 1\nskip gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverWorkspace(nil, root); err == nil || !strings.Contains(err.Error(), "skip gone names no folder") {
		t.Errorf("a skip of no folder: %v", err)
	}
}
