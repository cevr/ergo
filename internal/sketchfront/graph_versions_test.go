package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/graphrows"
)

// TestGraphKeepsModuleVersionsApart is the rows of several programs in one
// table (GraphOf over a tree, `ergo graph export DIR/...`) when they select
// different versions of one required module: two programs, each
// requiring depm at v1.0.0 or v1.1.0 (drawn), whose versions declare
// drawn functions of depm/lib, and whose Main calls drawn ones of its
// version. A reference model, which reads only the draw, says each
// version read is one package node under depm@VERSION holding its
// version's declarations, and each Main's uses reach its own version's
// declarations; a shared version is one node. A failing seed prints both
// and the replay command. Mutant: graph-package-by-path (a package node
// keyed by its path alone, the two versions merged).
func TestGraphKeepsModuleVersionsApart(t *testing.T) {
	names := []string{"A", "B", "C", "D", "E"}
	versions := []string{"v1.0.0", "v1.1.0"}
	apart := 0
	for seed := uint64(1); seed <= 6; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x9e75))
		dir := t.TempDir()
		cache := filepath.Join(dir, "modcache")
		write := func(path, text string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		const mod = "module depm\n\ngo 1.27\n"
		declared := map[string][]string{}
		sums := map[string]string{}
		for _, v := range versions {
			var src strings.Builder
			for i, n := range names {
				if r.IntN(2) == 0 || n == "A" {
					declared[v] = append(declared[v], n)
					fmt.Fprintf(&src, "func %s() int { %d }\n\n", n, i)
				}
			}
			files := map[string]string{"go.mod": mod, "ergo.mod": "module depm\n", "lib/lib.ergo": src.String()}
			for name, text := range files {
				write(filepath.Join(cache, "depm@"+v, filepath.FromSlash(name)), text)
			}
			write(filepath.Join(cache, "cache", "download", "depm", "@v", v+".mod"), mod)
			sums[v] = "depm " + v + " " + refHash("depm@"+v+"/", files) + "\ndepm " + v + "/go.mod " + refHash("", map[string]string{"go.mod": mod}) + "\n"
		}
		var as []Analysis
		var want []string
		picked := map[string]bool{}
		for _, app := range []string{"appa", "appb"} {
			v := versions[r.IntN(2)]
			picked[v] = true
			var calls []string
			for _, n := range declared[v] {
				if r.IntN(2) == 0 || len(calls) == 0 && n == declared[v][len(declared[v])-1] {
					calls = append(calls, "lib."+n+"()")
					want = append(want, "use "+app+"/"+app+".Main -> depm@"+v+"/depm/lib."+n)
				}
			}
			root := filepath.Join(dir, app)
			write(filepath.Join(root, "ergo.mod"), "module "+app+"\n")
			write(filepath.Join(root, "go.mod"), "module "+app+"\n\ngo 1.27\n\nrequire depm "+v+"\n")
			write(filepath.Join(root, "go.sum"), sums[v])
			write(filepath.Join(root, "main.ergo"), "import depm/lib\n\nfunc Main() int { "+strings.Join(calls, " + ")+" }\n")
			a, err := checkDirIn(hostFS{}, root, gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, cache)
			if err != nil || len(a.Diagnostics) != 0 {
				t.Fatalf("seed %d: %s does not check: %v %v", seed, app, err, a.Diagnostics)
			}
			as = append(as, a)
		}
		for _, v := range versions {
			if picked[v] {
				want = append(want, "package depm@"+v+"/depm/lib")
				for _, n := range declared[v] {
					want = append(want, "decl depm@"+v+"/depm/lib."+n)
				}
			}
		}
		if len(picked) == 2 {
			apart++
		}
		slices.Sort(want)
		got := versionRows(GraphOf(as))
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: the rows differ from the model\n--- rows\n%s\n--- model\n%s\nreplay: go test -run '^TestGraphKeepsModuleVersionsApart$' ./internal/sketchfront", seed, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
	if apart == 0 {
		t.Fatal("vacuous: no seed drew two versions")
	}
}

// versionRows is the rows the model speaks of: each package node of
// depm/lib and its declarations, by module, and each use from an app's
// declaration, by the module of each end.
func versionRows(g *graphrows.Table) []string {
	module := func(n uint32) string {
		for ; n != graphrows.NoNode; n = g.Nodes[n].Parent {
			if g.Nodes[n].Kind == graphrows.Module {
				return g.Str(g.Nodes[n].Name)
			}
		}
		return "?"
	}
	var out []string
	for i, n := range g.Nodes {
		switch {
		case n.Kind == graphrows.Package && g.Str(n.Name) == "depm/lib":
			out = append(out, "package "+module(uint32(i))+"/depm/lib")
		case n.Kind == graphrows.Decl && strings.HasPrefix(fullName(g, uint32(i)), "depm/lib."):
			out = append(out, "decl "+module(uint32(i))+"/"+fullName(g, uint32(i)))
		}
	}
	for _, e := range g.Edges {
		if e.Kind == graphrows.Uses && strings.HasPrefix(module(e.From), "app") {
			out = append(out, "use "+module(e.From)+"/"+fullName(g, e.From)+" -> "+module(e.To)+"/"+fullName(g, e.To))
		}
	}
	slices.Sort(out)
	return out
}
