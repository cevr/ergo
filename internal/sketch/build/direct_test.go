package build_test

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// buildIDMarker opens the build ID string every Go executable carries
// (cmd/internal/buildid).
const buildIDMarker = "\xff Go build ID: \""

// maskBuildID zeroes every occurrence of the executable's own build ID:
// the .note.go.buildid note of an ELF file, the marked string at the start
// of the text elsewhere. Build IDs differ by design: Go's are its own
// action IDs, the direct path's are its action keys. Nothing else may
// differ.
func maskBuildID(t *testing.T, b []byte) []byte {
	t.Helper()
	var id []byte
	if f, err := elf.NewFile(bytes.NewReader(b)); err == nil {
		s := f.Section(".note.go.buildid")
		if s == nil {
			t.Fatal("ELF executable has no Go build ID note")
		}
		note, err := s.Data()
		// namesz, descsz, type, then the name "Go\x00\x00" and the ID.
		if err != nil || len(note) < 16 {
			t.Fatalf("Go build ID note: %v", err)
		}
		id = bytes.Clone(note[16 : 16+binary.LittleEndian.Uint32(note[4:8])])
		// The linker derives the GNU build ID from the Go build ID.
		if s := f.Section(".note.gnu.build-id"); s != nil {
			gnu, err := s.Data()
			if err != nil || len(gnu) < 16 {
				t.Fatalf("GNU build ID note: %v", err)
			}
			b = bytes.ReplaceAll(b, gnu[16:16+binary.LittleEndian.Uint32(gnu[4:8])], make([]byte, binary.LittleEndian.Uint32(gnu[4:8])))
		}
	} else {
		if f, err := macho.NewFile(bytes.NewReader(b)); err == nil {
			// The linker derives the LC_UUID from the build ID, and the
			// ad hoc code signature hashes every page, the build ID's too.
			b = bytes.Clone(b)
			for _, l := range f.Loads {
				raw := l.Raw()
				switch f.ByteOrder.Uint32(raw[0:4]) {
				case 0x1b: // LC_UUID: cmd, cmdsize, uuid[16]
					b = bytes.ReplaceAll(b, raw[8:24], make([]byte, 16))
				case 0x1d: // LC_CODE_SIGNATURE: cmd, cmdsize, dataoff, datasize
					off, size := f.ByteOrder.Uint32(raw[8:12]), f.ByteOrder.Uint32(raw[12:16])
					clear(b[off : off+size])
				}
			}
		}
		i := bytes.Index(b, []byte(buildIDMarker))
		if i < 0 {
			t.Fatal("executable has no build ID")
		}
		rest := b[i+len(buildIDMarker):]
		j := bytes.Index(rest, []byte("\"\n"))
		if j < 0 {
			t.Fatal("executable has no build ID end")
		}
		id = bytes.Clone(rest[:j])
	}
	return bytes.ReplaceAll(b, id, make([]byte, len(id)))
}

// sameExecutable fails unless the two files are equal but for their
// build IDs, naming the first differing offset.
func sameExecutable(t *testing.T, what, direct, gobuild string) {
	t.Helper()
	a, err := os.ReadFile(direct)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(gobuild)
	if err != nil {
		t.Fatal(err)
	}
	a, b = maskBuildID(t, a), maskBuildID(t, b)
	if bytes.Equal(a, b) {
		return
	}
	at := 0
	for at < min(len(a), len(b)) && a[at] == b[at] {
		at++
	}
	t.Fatalf("%s: the direct executable (%d bytes) differs from go build's (%d bytes) at offset %d", what, len(a), len(b), at)
}

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
	sameExecutable(t, dir, filepath.Join(out, "direct"), filepath.Join(out, "go"))
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
	}{
		{"embed", map[string]string{"dep/lib/embed.go": "package lib\n\nimport _ \"embed\"\n\n//go:embed data.txt\nvar Data string\n", "dep/lib/data.txt": "x"}},
		{"assembly", map[string]string{"dep/lib/stub.s": "", "dep/lib/decl.go": "package lib\n\nfunc stub()\n"}},
		{"go:debug", map[string]string{"debug.go": "//go:debug panicnil=1\n\npackage main\n"}},
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
			if got.Direct || !strings.HasPrefix(got.Refused, "direct build refused:") {
				t.Fatalf("got %v, want a refusal", got)
			}
		})
	}
}

// TestDirectMatchesGoBuild is the identity property: for every example
// program and seeded generated programs, the direct path's executable is
// go build's but for the build ID. It builds Go because the property is
// about the executables the two paths write. A failing case names the
// program (a generated one prints its source and its seed; replay with
// ERGO_DIRECT_SEED=N).
func TestDirectMatchesGoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	c := build.Cache{Root: t.TempDir()}
	tc := build.Toolchain{Go: "go"}
	var programs []string
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && e.Name() == "main.ergo" {
			programs = append(programs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil || len(programs) == 0 {
		t.Fatalf("no example programs: %v", err)
	}
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if s := os.Getenv("ERGO_DIRECT_SEED"); s != "" {
		var seed uint64
		fmt.Sscan(s, &seed)
		seeds, programs = []uint64{seed}, nil
	}
	for _, seed := range seeds {
		dir := filepath.Join(t.TempDir(), fmt.Sprintf("generated%d", seed))
		writeFiles(t, dir, map[string]string{"main.ergo": generated(seed)})
		programs = append(programs, dir)
	}
	for _, dir := range programs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			if strings.HasPrefix(filepath.Base(dir), "generated") {
				source, _ := os.ReadFile(filepath.Join(dir, "main.ergo"))
				defer func() {
					if t.Failed() {
						t.Logf("generated program (replay with ERGO_DIRECT_SEED):\n%s", source)
					}
				}()
			}
			a, err := sketchfront.CheckDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			absolute, err := filepath.Abs(dir)
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			var report build.Report
			var stderr bytes.Buffer
			for _, path := range []struct {
				out     string
				goBuild bool
			}{{"direct", false}, {"go", true}} {
				req := sketchfront.BuildRequest{Mode: build.ModeBuild, Source: absolute, Output: filepath.Join(out, path.out), Cache: c, Toolchain: tc, Stderr: &stderr}
				req.Toolchain.GoBuild = path.goBuild
				if !path.goBuild {
					req.Report = &report
				}
				if _, err := sketchfront.Build(a, req); err != nil {
					t.Fatalf("%s: %v\n%s", path.out, err, stderr.String())
				}
			}
			if !report.Direct {
				if !strings.HasPrefix(report.Refused, "direct build refused:") {
					t.Fatalf("the direct path failed: %s", report.Refused)
				}
				t.Logf("go build: %s", report.Refused)
			}
			sameExecutable(t, dir, filepath.Join(out, "direct"), filepath.Join(out, "go"))
		})
	}
}

// generated is a seeded program: a chain of string functions, some of
// them calling the standard strings and strconv packages, so seeds vary
// the standard imports, the main package's size and its text.
func generated(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x6469726563740a))
	useStrings, useStrconv := r.IntN(2) == 0, r.IntN(2) == 0
	var b strings.Builder
	if useStrings {
		b.WriteString("import strings\n")
	}
	if useStrconv {
		b.WriteString("import strconv\n")
	}
	b.WriteString("\n")
	n := 1 + r.IntN(12)
	for i := range n {
		body := fmt.Sprintf("s + %q", fmt.Sprintf("-%d.%d", seed, r.IntN(1000)))
		switch k := r.IntN(3); {
		case k == 1 && useStrings:
			body = "strings.TrimSpace(" + body + ")"
		case k == 2 && useStrconv:
			body = fmt.Sprintf("s + strconv.Itoa(%d)", r.IntN(1<<20))
		}
		fmt.Fprintf(&b, "func f%d(s string) string { %s }\n\n", i, body)
	}
	call := fmt.Sprintf("%q", fmt.Sprintf("seed %d", seed))
	for i := range n {
		call = fmt.Sprintf("f%d(%s)", i, call)
	}
	fmt.Fprintf(&b, "func main() needs Console {\n\tConsole.Println(%s)\n}\n", call)
	return b.String()
}
