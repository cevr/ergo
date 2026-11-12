package build_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/build/exetest"
)

// both links module with the direct path and with go build, checks that
// the executables are the same, and returns the direct path's report.
func both(t *testing.T, c build.Cache, tc build.Toolchain, dir string) build.Report {
	t.Helper()
	out := t.TempDir()
	module := build.Module{Dir: dir}
	var stderr bytes.Buffer
	report, err := tc.Build(c, module, filepath.Join(out, "direct"), &stderr)
	if err != nil {
		t.Fatalf("direct: %v\n%s", err, stderr.String())
	}
	ref := tc
	ref.GoBuild = true
	if _, err := ref.Build(c, module, filepath.Join(out, "go"), &stderr); err != nil {
		t.Fatalf("go build: %v\n%s", err, stderr.String())
	}
	exetest.Same(t, dir, filepath.Join(out, "direct"), filepath.Join(out, "go"))
	return report
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// goLine is the go line of the toolchain that built this test: the
// fixture's main module must state the toolchain's language.
func goLine() string {
	v := strings.TrimPrefix(runtime.Version(), "go")
	parts := strings.SplitN(v, ".", 3)
	return parts[0] + "." + parts[1]
}

// fixture writes a module of the generated shape: a main package and a
// local module it requires through a replace, one standard import. The
// local module's leaf package imports nothing, so only the target and the
// tool in its key tell two targets' compiles of it apart.
func fixture(t *testing.T) string {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":           "module example.local/prog\n\ngo " + goLine() + "\n\nrequire example.local/dep v0.0.0\n\nreplace example.local/dep => ./dep\n",
		"main.go":          "package main\n\nimport (\n\t\"os\"\n\n\t\"example.local/dep/lib\"\n)\n\nfunc main() { os.Stdout.WriteString(lib.Greet(\"x\") + \"\\n\") }\n",
		"dep/go.mod":       "module example.local/dep\n\ngo " + goLine() + "\n",
		"dep/lib/lib.go":   "package lib\n\nimport \"example.local/dep/leaf\"\n\nfunc Greet(s string) string { return pad(s) }\n\n//go:noinline\nfunc pad(s string) string { return leaf.Open + s + \">\" }\n",
		"dep/leaf/leaf.go": "package leaf\n\nconst Open = \"<\"\n\n//go:noinline\nfunc Sum(a, b int) int { return a*3 + b }\n",
	})
	return dir
}

func edit(t *testing.T, path, from, to string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(from)) {
		t.Fatalf("%s holds no %q", path, from)
	}
	if err := os.WriteFile(path, bytes.Replace(data, []byte(from), []byte(to), 1), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDirectRebuildsOnEachInput edits each input of the direct path's
// action keys in turn and checks, by the actions it counts, that exactly
// the actions that read the input run again, and that the executable is
// still go build's. It builds Go because the counts are of real compile
// and link actions and the reference is go build's executable; no
// in-process check runs the linker.
func TestDirectRebuildsOnEachInput(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	c := build.Cache{Root: t.TempDir()}
	tc := build.Toolchain{Go: "go"}
	dir := fixture(t)
	lib := filepath.Join(dir, "dep", "lib", "lib.go")
	outer := t.TempDir()

	first := both(t, c, tc, dir)
	if !first.Direct || first.Compiled != 3 || !first.Linked || !first.Planned {
		t.Fatalf("cold build: %v", first)
	}
	type row struct {
		name  string
		apply func(t *testing.T) build.Toolchain
		// compiled is the number of the module's three packages compiled
		// again; linked whether the link runs again; planned whether the
		// plan is made again.
		compiled        int
		linked, planned bool
	}
	rows := []row{
		{"warm", func(t *testing.T) build.Toolchain { return tc }, 0, false, false},
		{"main source", func(t *testing.T) build.Toolchain {
			edit(t, filepath.Join(dir, "main.go"), `"x"`, `"y"`)
			return tc
		}, 1, true, false},
		{"dependency body, same export data", func(t *testing.T) build.Toolchain {
			edit(t, lib, `s + ">"`, `s + "]"`)
			return tc
		}, 1, true, false},
		{"dependency export data and a new standard import", func(t *testing.T) build.Toolchain {
			edit(t, lib, "import \"example.local/dep/leaf\"\n", "import \"example.local/dep/leaf\"\n\nimport \"strings\"\n\nfunc Upper(s string) string { return strings.ToUpper(s) }\n")
			return tc
		}, 2, true, true},
		{"file name", func(t *testing.T) build.Toolchain {
			if err := os.Rename(lib, filepath.Join(dir, "dep", "lib", "renamed.go")); err != nil {
				t.Fatal(err)
			}
			lib = filepath.Join(dir, "dep", "lib", "renamed.go")
			return tc
		}, 2, true, false}, // export data names positions by file
		{"file set", func(t *testing.T) build.Toolchain {
			writeFiles(t, dir, map[string]string{"dep/lib/more.go": "package lib\n\nfunc More() int { return 1 }\n"})
			return tc
		}, 2, true, false},
		{"language version of a dependency", func(t *testing.T) build.Toolchain {
			edit(t, filepath.Join(dir, "dep", "go.mod"), "go "+goLine(), "go 1.22")
			return tc
		}, 2, true, true}, // lib and leaf; their export data stays
		{"required version (the trimmed path)", func(t *testing.T) build.Toolchain {
			edit(t, filepath.Join(dir, "go.mod"), "dep v0.0.0", "dep v0.0.1")
			return tc
		}, 3, true, true}, // the trimmed path names the export data's positions
		{"replace directory (the build information only)", func(t *testing.T) build.Toolchain {
			if err := os.Rename(filepath.Join(dir, "dep"), filepath.Join(dir, "dep2")); err != nil {
				t.Fatal(err)
			}
			lib = filepath.Join(dir, "dep2", "lib", "renamed.go")
			edit(t, filepath.Join(dir, "go.mod"), "=> ./dep", "=> ./dep2")
			return tc
		}, 0, true, true},
		{"build tags", func(t *testing.T) build.Toolchain {
			writeFiles(t, dir, map[string]string{"dep2/lib/tagged.go": "//go:build fixturetag\n\npackage lib\n\nfunc Tagged() int { return 2 }\n"})
			tagged := tc
			tagged.Tags = []string{"fixturetag"}
			return tagged
		}, 2, true, true},
		{"target architecture", func(t *testing.T) build.Toolchain {
			other := tc
			other.GOARCH = map[string]string{"amd64": "arm64", "arm64": "amd64"}[runtime.GOARCH]
			return other
		}, 3, true, true},
		{"target system", func(t *testing.T) build.Toolchain {
			other := tc
			other.GOOS = map[string]string{"linux": "darwin", "darwin": "linux"}[runtime.GOOS]
			return other
		}, 3, true, true},
		{"C compiler name (the external linker)", func(t *testing.T) build.Toolchain {
			t.Setenv("CC", "/nonexistent/loop-probe-x/cc")
			return tc
		}, 0, true, true},
		{"strip (ergo build's default)", func(t *testing.T) build.Toolchain {
			stripped := tc
			stripped.Strip = true
			return stripped
		}, 0, true, false},
		{"second module directory", func(t *testing.T) build.Toolchain {
			moved := filepath.Join(outer, "moved")
			copyTree(t, dir, moved)
			dir = moved
			lib = filepath.Join(dir, "dep2", "lib", "renamed.go")
			return tc
		}, 0, false, false},
		{"toolchain identity", func(t *testing.T) build.Toolchain {
			plans, err := filepath.Glob(filepath.Join(c.Root, "go", "plans", "*"))
			if err != nil || len(plans) == 0 {
				t.Fatalf("no plans: %v", err)
			}
			for _, path := range plans {
				var p map[string]any
				data, err := os.ReadFile(path)
				dec := json.NewDecoder(bytes.NewReader(data))
				dec.UseNumber() // a tool's status is an int64 no float64 holds
				if err != nil || dec.Decode(&p) != nil {
					t.Fatalf("plan %s: %v", path, err)
				}
				for _, tool := range []string{"Compile", "Link"} {
					p[tool].(map[string]any)["Digest"] = "another toolchain"
				}
				data, _ = json.Marshal(p)
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			return tc
		}, 3, true, false},
	}
	if runtime.GOARCH == "amd64" {
		rows = append(rows, row{"architecture level", func(t *testing.T) build.Toolchain {
			t.Setenv("GOAMD64", "v3")
			return tc
		}, 3, true, true})
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := both(t, c, r.apply(t), dir)
			if !got.Direct || got.Compiled != r.compiled || got.Linked != r.linked || got.Planned != r.planned {
				t.Fatalf("got %v; want compiled %d, linked %t, planned %t", got, r.compiled, r.linked, r.planned)
			}
		})
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestDirectRefusesWhatItCannotKey: a module with an input the direct
// path does not key falls back to go build, which builds it.
func TestDirectRefusesWhatItCannotKey(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	c := build.Cache{Root: t.TempDir()}
	for _, r := range []struct {
		name  string
		files map[string]string
		// reason is in the refusal: go build embeds the non-ASCII
		// name, so only the reason tells an undecided name from a bad one.
		reason string
	}{
		{"embed of a non-ASCII name", map[string]string{"dep/lib/embed.go": "package lib\n\nimport _ \"embed\"\n\n//go:embed données.txt\nvar Data string\n", "dep/lib/données.txt": "x"}, "left to go build"},
		{"assembly", map[string]string{"dep/lib/stub.s": "", "dep/lib/decl.go": "package lib\n\nfunc stub()\n"}, ""},
		{"go:debug", map[string]string{"debug.go": "//go:debug panicnil=1\n\npackage main\n"}, ""},
	} {
		t.Run(r.name, func(t *testing.T) {
			dir := fixture(t)
			writeFiles(t, dir, r.files)
			out := filepath.Join(t.TempDir(), "app")
			var stderr bytes.Buffer
			got, err := build.Toolchain{Go: "go"}.Build(c, build.Module{Dir: dir}, out, &stderr)
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr.String())
			}
			if got.Direct || !strings.HasPrefix(got.Refused, "direct build refused:") || !strings.Contains(got.Refused, r.reason) {
				t.Fatalf("got %v, want a refusal (%q)", got, r.reason)
			}
		})
	}
}
