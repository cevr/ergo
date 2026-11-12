package build_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

// Each generated tree under the embedding package holds embedAlways, two
// groups of embedMaybe, and at most one of embedBad. Each is a
// case of Go's embed rules: hidden and underscore names (skipped in a walk
// unless all:), a version control directory (never embedded), a nested
// module (skipped in a walk, an error when matched), a directory with
// nothing to embed, symbolic links to a file and to a directory (skipped in
// a walk, an error when matched or crossed); embedBad are names module
// paths refuse (a reserved name, a refused character, a trailing dot),
// an error wherever a pattern reaches them.
var (
	embedAlways = []string{"data/a.txt", "data/sub/c.txt", "top.txt", ".top"}
	// A group's probe is a pattern that reaches its rule.
	embedMaybe = []struct {
		files []string
		probe string
	}{
		{[]string{"data/b.md"}, "data/*.md"},
		{[]string{"data/.hidden"}, "data/.hidden"},
		{[]string{"data/_under.txt"}, "data/_*"},
		{[]string{"data/sub/.deep"}, "all:data/sub"},
		{[]string{"data/.git/config"}, "all:data"},
		{[]string{"data/_dir/x.txt"}, "data/_dir"},
		{[]string{"data/mod/go.mod", "data/mod/m.txt"}, "data/mod/m.txt"},
		{[]string{"data/empty/.keep"}, "data/[es]*"},
		{[]string{"data/link"}, "data/link"},
		{[]string{"data/ldir"}, "data/ldir/*"},
	}
	embedBad = []string{"data/con.txt", "data/semi;colon.txt", "data/end."}
)

// embedPatterns are the //go:embed patterns a generated package may use.
var embedPatterns = []string{
	"data", "all:data", "data/*.txt", "data/sub", "top.txt", "*.txt", "data/*", ".top", "all:data/*",
	"data/mod/m.txt", "nomatch*", "./top.txt", "data/con.txt", "data/link", "data/ldir/*", "data/empty",
}

// embedTree writes seed's tree and //go:embed directive into the fixture's
// lib package and returns the directive's patterns.
func embedTree(t *testing.T, dir string, seed uint64) []string {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 0x656d626564))
	lib := filepath.Join(dir, "dep", "lib")
	entries := slices.Clone(embedAlways)
	// Two groups and at most one bad name, so that one rule at a time
	// decides whether the build fails.
	var probes []string
	for _, i := range r.Perm(len(embedMaybe))[:2] {
		entries = append(entries, embedMaybe[i].files...)
		probes = append(probes, embedMaybe[i].probe)
	}
	if r.IntN(3) == 0 {
		entries = append(entries, embedBad[r.IntN(len(embedBad))])
	}
	for _, e := range entries {
		path := filepath.Join(lib, filepath.FromSlash(e))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		var err error
		switch e {
		case "data/link":
			err = os.Symlink("../top.txt", path)
		case "data/ldir":
			if err = os.MkdirAll(filepath.Join(lib, "data", "sub"), 0o755); err == nil {
				err = os.Symlink("sub", path)
			}
		case "data/mod/go.mod":
			err = os.WriteFile(path, []byte("module example.local/nested\n"), 0o644)
		default:
			err = os.WriteFile(path, []byte(fmt.Sprintf("%s %d\n", e, r.IntN(1000))), 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	patterns := []string{embedPatterns[r.IntN(len(embedPatterns))]}
	switch r.IntN(3) {
	case 0:
		patterns = append(patterns, embedPatterns[r.IntN(len(embedPatterns))])
	case 1:
		patterns = append(patterns, probes[r.IntN(len(probes))])
	}
	// main prints every embedded file, so the linker keeps them all and
	// the executables differ when the embedded files do.
	writeFiles(t, dir, map[string]string{
		"dep/lib/embed.go": "package lib\n\nimport \"embed\"\n\n//go:embed " + strings.Join(patterns, " ") + "\nvar Files embed.FS\n",
		"main.go": "package main\n\nimport (\n\t\"io/fs\"\n\t\"os\"\n\n\t\"example.local/dep/lib\"\n)\n\n" +
			"func main() {\n\tfs.WalkDir(lib.Files, \".\", func(p string, d fs.DirEntry, err error) error {\n" +
			"\t\tif err == nil && !d.IsDir() {\n\t\t\tdata, _ := lib.Files.ReadFile(p)\n\t\t\tos.Stdout.WriteString(p + \"\\n\" + string(data))\n\t\t}\n\t\treturn nil\n\t})\n}\n",
	})
	return patterns
}

// TestDirectEmbedMatchesGoBuild is the embed property: on seeded directory
// trees and patterns that reach each of Go's embed rules, the direct path
// builds exactly when go build does, and then the same executable but for
// the build ID; where go build fails, the direct path refused first. It
// builds Go because the reference is go build's own resolution of the
// patterns and the executable it writes. Replay a seed with
// ERGO_EMBED_SEED=N.
func TestDirectEmbedMatchesGoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	c := build.Cache{Root: t.TempDir()}
	tc := build.Toolchain{Go: "go"}
	var seeds []uint64
	for s := uint64(1); s <= 160; s++ {
		seeds = append(seeds, s)
	}
	if s := os.Getenv("ERGO_EMBED_SEED"); s != "" {
		n, _ := strconv.ParseUint(s, 10, 64)
		seeds = []uint64{n}
	}
	built, failed := 0, 0
	for _, seed := range seeds {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			dir := fixture(t)
			if seed%2 == 1 {
				// Glob characters in the module directory match only
				// themselves (cmd/go quotes the directory).
				moved := filepath.Join(t.TempDir(), "g[l]ob*")
				if err := os.Rename(dir, moved); err != nil {
					t.Fatal(err)
				}
				dir = moved
			}
			patterns := embedTree(t, dir, seed)
			defer func() {
				if t.Failed() {
					t.Logf("seed %d patterns %q (replay with ERGO_EMBED_SEED=%d)", seed, patterns, seed)
				}
			}()
			out := t.TempDir()
			module := build.Module{Dir: dir}
			var stderr bytes.Buffer
			ref := tc
			ref.GoBuild = true
			_, goErr := ref.Build(c, module, filepath.Join(out, "go"), &stderr)
			report, err := tc.Build(c, module, filepath.Join(out, "direct"), &stderr)
			if goErr != nil {
				failed++
				// The resolution itself decides: a compile that fails
				// on the embed configuration is not a refusal.
				if err == nil || !strings.HasPrefix(report.Refused, "direct build refused:") {
					t.Fatalf("go build fails (%v) but the direct path did not refuse: %v", goErr, report)
				}
				return
			}
			built++
			if err != nil || !report.Direct {
				t.Fatalf("go build builds but the direct path did not: %v, %v\n%s", report, err, stderr.String())
			}
			sameExecutable(t, dir, filepath.Join(out, "direct"), filepath.Join(out, "go"))
		})
	}
	t.Logf("%d seeds built, %d failed in both paths", built, failed)
	if len(seeds) > 1 && (built == 0 || failed == 0) {
		t.Fatalf("the seeds reach one side only: %d built, %d failed", built, failed)
	}
}

// TestDirectEmbedRebuilds edits the embedded files and counts the compile
// actions: an edit, an added, a removed and a renamed file compile the
// embedding package again; a hidden or underscore file the pattern skips
// does not. It builds Go because the counts are of real compile actions
// and every row compares with go build's executable.
func TestDirectEmbedRebuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	c := build.Cache{Root: t.TempDir()}
	tc := build.Toolchain{Go: "go"}
	dir := fixture(t)
	data := filepath.Join(dir, "dep", "lib", "data")
	writeFiles(t, dir, map[string]string{
		"dep/lib/embed.go":    "package lib\n\nimport \"embed\"\n\n//go:embed data\nvar Files embed.FS\n",
		"dep/lib/data/a.txt":  "a\n",
		"dep/lib/data/c.txt":  "c\n",
		"dep/lib/data/s/d.md": "d\n",
	})
	if first := both(t, c, tc, dir); !first.Direct || first.Compiled != 3 {
		t.Fatalf("cold build: %v", first)
	}
	for _, r := range []struct {
		name     string
		apply    func(t *testing.T)
		compiled int
		linked   bool
	}{
		{"warm", func(t *testing.T) {}, 0, false},
		{"edited file", func(t *testing.T) { edit(t, filepath.Join(data, "a.txt"), "a", "A") }, 1, true},
		{"added file", func(t *testing.T) { writeFiles(t, data, map[string]string{"b.txt": "b\n"}) }, 1, true},
		{"removed file", func(t *testing.T) {
			if err := os.Remove(filepath.Join(data, "c.txt")); err != nil {
				t.Fatal(err)
			}
		}, 1, true},
		{"renamed file", func(t *testing.T) {
			if err := os.Rename(filepath.Join(data, "b.txt"), filepath.Join(data, "e.txt")); err != nil {
				t.Fatal(err)
			}
		}, 1, true},
		{"hidden file", func(t *testing.T) { writeFiles(t, data, map[string]string{".h": "h\n", "s/_u.txt": "u\n"}) }, 0, false},
	} {
		t.Run(r.name, func(t *testing.T) {
			r.apply(t)
			got := both(t, c, tc, dir)
			if !got.Direct || got.Compiled != r.compiled || got.Linked != r.linked {
				t.Fatalf("got %v; want compiled %d, linked %t", got, r.compiled, r.linked)
			}
		})
	}
}
