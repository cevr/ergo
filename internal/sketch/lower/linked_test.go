package lower

import (
	"bytes"
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// linkedSeeds and linkedSample size TestLinkedRuntimeHoldsEveryImport;
// ERGO_LINKED_SEED=N replays one seed.
const linkedSample = 24

// linkedPlatforms are the platforms the reference resolves build
// constraints for: a file the program links on any of them is linked.
var linkedPlatforms = [][2]string{{"linux", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}, {"js", "wasm"}}

// TestLinkedRuntimeHoldsEveryImport is LinkedRuntime's law (CI-SELECT
// CS-CUTOFF): over corpus programs, as built and as test builds, every
// runtime package that go/build's import resolution (an independent
// reader, which applies build constraints) reaches from the program's
// files, on each of four platforms, is in LinkedRuntime, with all of its
// files; and the runtime's go.mod is always in it. A failing seed prints
// the program, the missing package and the replay command. Mutants
// linked-runtime-skips-transitive, linked-runtime-drops-unowned.
func TestLinkedRuntimeHoldsEveryImport(t *testing.T) {
	sources, err := corpusDomain()
	if err != nil {
		t.Fatal(err)
	}
	seed := uint64(1)
	if s := os.Getenv("ERGO_LINKED_SEED"); s != "" {
		if seed, err = strconv.ParseUint(s, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	runtime := Runtime()
	fsys := memFS{}
	for _, f := range runtime {
		fsys[f.Path] = f.Data
	}
	r := rand.New(rand.NewPCG(seed, 0x6c6b))
	programs := 0
	for _, i := range r.Perm(len(sources)) {
		if programs == linkedSample {
			break
		}
		name := fmt.Sprintf("linked%d", i)
		var files []File
		if r.IntN(2) == 0 {
			o := lowerSource(name, sources[i], true)
			if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 || o.file == nil {
				continue
			}
			files = o.files
		} else {
			o, err := lowerTestBuildErr(name, sources[i])
			if err != nil {
				continue
			}
			files = o.files
		}
		programs++
		linked := LinkedRuntime(runtime, files)
		held := map[string]bool{}
		for _, f := range linked {
			held[f.Path] = true
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d, program %s: %s\nsource:\n%s\nreplay: ERGO_LINKED_SEED=%d go test -run '^TestLinkedRuntimeHoldsEveryImport$' ./internal/sketch/lower", seed, name, fmt.Sprintf(format, args...), sources[i], seed)
		}
		if !held[runtimeDir+"/go.mod"] {
			fail("the runtime's go.mod is not linked")
		}
		for _, p := range linkedPlatforms {
			for _, dir := range referenceDeps(t, fsys, files, p) {
				for _, f := range runtime {
					if strings.HasPrefix(f.Path, dir+"/") && !held[f.Path] && ownedBy(f.Path, dir, fsys) {
						fail("%s/%s links %s, which LinkedRuntime leaves out", p[0], p[1], f.Path)
					}
				}
			}
		}
	}
	if programs < linkedSample {
		t.Fatalf("only %d corpus programs lower", programs)
	}
}

// ownedBy reports whether the file name belongs to the package in dir:
// no folder between them holds Go files of its own.
func ownedBy(name, dir string, fsys memFS) bool {
	for d := path.Dir(name); d != dir; d = path.Dir(d) {
		for p := range fsys {
			if path.Dir(p) == d && strings.HasSuffix(p, ".go") {
				return false
			}
		}
	}
	return true
}

// referenceDeps are the runtime folders go/build reaches from the
// program's Go files on platform p, through the runtime's own imports.
func referenceDeps(t *testing.T, fsys memFS, program []File, p [2]string) []string {
	t.Helper()
	ctxt := build.Default
	ctxt.GOOS, ctxt.GOARCH, ctxt.CgoEnabled = p[0], p[1], false
	ctxt.GOPATH, ctxt.GOROOT = "", ""
	ctxt.JoinPath = path.Join
	ctxt.IsAbsPath = func(string) bool { return false }
	ctxt.IsDir = func(d string) bool { return fsys.isDir(d) }
	ctxt.HasSubdir = func(root, dir string) (string, bool) { return "", false }
	ctxt.ReadDir = func(d string) ([]fs.FileInfo, error) { return fsys.readDir(d), nil }
	ctxt.OpenFile = func(name string) (io.ReadCloser, error) {
		data, ok := fsys[name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	var out []string
	seen := map[string]bool{}
	var visit func(imports []string)
	visit = func(imports []string) {
		for _, imp := range imports {
			rest, ok := strings.CutPrefix(imp, runtimeModule+"/")
			if !ok {
				continue
			}
			dir := runtimeDir + "/" + rest
			if seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
			pkg, err := ctxt.ImportDir(dir, 0)
			if err != nil {
				if _, none := err.(*build.NoGoError); none {
					continue
				}
				t.Fatalf("go/build %s on %s/%s: %v", dir, p[0], p[1], err)
			}
			visit(pkg.Imports)
		}
	}
	for _, f := range program {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		imports, ok := runtimeImports(f.Path, f.Data)
		if !ok {
			t.Fatalf("%s does not parse", f.Path)
		}
		for _, d := range imports {
			visit([]string{runtimeModule + "/" + strings.TrimPrefix(d, runtimeDir+"/")})
		}
	}
	slices.Sort(out)
	return out
}

// memFS is a set of files by slash path, for go/build.
type memFS map[string][]byte

func (m memFS) isDir(d string) bool {
	for p := range m {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

func (m memFS) readDir(d string) []fs.FileInfo {
	var out []fs.FileInfo
	seen := map[string]bool{}
	for p, data := range m {
		rest, ok := strings.CutPrefix(p, d+"/")
		if !ok {
			continue
		}
		name, _, sub := strings.Cut(rest, "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, memInfo{name: name, dir: sub, size: int64(len(data))})
	}
	slices.SortFunc(out, func(a, b fs.FileInfo) int { return strings.Compare(a.Name(), b.Name()) })
	return out
}

type memInfo struct {
	name string
	dir  bool
	size int64
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.dir }
func (i memInfo) Sys() any           { return nil }
