package sketchfront

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/check"
)

// TestModuleSnapshotAgrees is NP5's acceptance (std-nopriv plan, S3: std
// is the toolchain's module, and a required module, read-only and named by
// its content hash, gets the same checked snapshot). Over MD1's generated
// module graphs (modules_test.go), a program is checked by a cold process,
// which writes the snapshot, and again by a warm one, which installs it:
// every output (diagnostics, inspect envelope, both lowered modes,
// evaluation) is the same, and the warm check decodes, and checks from
// source none of, exactly the packages a reference picks from the files: a
// package of a module copy in the module cache (a local replacement has no
// hash), whose source uses no Go, all of whose imports are such packages,
// in a program that checks. A failing seed prints its files, both outputs
// and the replay command.
//
// Mutants it alone catches (testdata/mutants.json): snapshot-std-only (the
// loader never takes a module package from the snapshot), np5-local-held
// (a local replacement's packages written), np5-go-held (a package that
// uses Go written), np5-syntax-kept (a decoded package keeps the syntax
// the loader parsed), np5-import-unwrapped (a module package checked with
// the loader itself as its importer, which the codec cannot carry).
func TestModuleSnapshotAgrees(t *testing.T) {
	savedIdentity := compilerIdentity
	stdCache.mu.Lock()
	savedRoot := stdCache.root
	stdCache.mu.Unlock()
	t.Cleanup(func() {
		compilerIdentity = savedIdentity
		UseStdCache(savedRoot)
		resetStdCache(t)
	})
	compilerIdentity = func() (string, error) { return "sha256:" + strings.Repeat("d", 64), nil }
	UseStdCache(t.TempDir())
	first, n := uint64(1), uint64(160)
	if s := os.Getenv("ERGO_MODULE_SNAPSHOT_SEED"); s != "" {
		var err error
		if first, err = strconv.ParseUint(s, 0, 64); err != nil {
			t.Fatal(err)
		}
		n = 1
	}
	hits := 0
	for seed := first; seed < first+n; seed++ {
		m := genModules(seed)
		if seed%2 == 0 {
			m.clean()
		}
		dir := t.TempDir()
		root := m.write(t, dir)
		// A main, so the build mode lowers the program and its imports.
		mainFile := filepath.Join(root, "cmd", "x.ergo")
		text, err := os.ReadFile(mainFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mainFile, append(text, mainSource...), 0o644); err != nil {
			t.Fatal(err)
		}
		cache := filepath.Join(dir, "modcache")
		run := func() (map[string]string, Analysis, check.Work, int64) {
			t.Helper()
			resetStdCache(t)
			a, err := checkDirIn(hostFS{}, filepath.Join(root, "cmd"), gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, cache)
			if err != nil {
				t.Fatal(err)
			}
			stdCache.mu.Lock()
			taken := stdCache.taken
			stdCache.mu.Unlock()
			return analysisOutcome(t, a, ""), a, check.WorkDone(), taken
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\n--- files\n%s--- replay: ERGO_MODULE_SNAPSHOT_SEED=%d go test -run '^TestModuleSnapshotAgrees$' ./internal/sketchfront",
				seed, fmt.Sprintf(format, args...), m.listing(), seed)
		}
		cold, _, coldWork, coldTaken := run()
		warm, a, warmWork, warmTaken := run()
		for key, want := range cold {
			if warm[key] != want {
				fail("%s differs\n--- cold\n%s\n--- warm\n%s", key, want, warm[key])
			}
		}
		want := int64(snapshotReference(a, cache))
		if coldWork.ModulePackagesDecoded != 0 || coldTaken != 0 || warmWork.ModulePackagesDecoded != want || warmTaken != want {
			fail("decoded %d and took %d module packages cold, %d and %d warm; the reference holds %d",
				coldWork.ModulePackagesDecoded, coldTaken, warmWork.ModulePackagesDecoded, warmTaken, want)
		}
		if want > 0 {
			hits++
		}
	}
	if n > 1 && hits == 0 {
		t.Fatal("no seed's program took a module package from the snapshot")
	}
}

// mainSource is the root package's main: it calls F, which calls the F of
// every import.
const mainSource = "\nfunc main() {\n\tlet n = F()\n\tif n < 0 {\n\t\treturn\n\t}\n}\n"

// shapeSource gives a module package a union with an exhaustive match, and
// a generic record built through a generic function: an instance's checked
// form names the checker that resolved it, so a snapshot of the package
// carries the checker and its importer (np5-import-unwrapped).
const shapeSource = "\ntype Shape = Dot{} | Line{N int}\n\nfunc Size(s Shape) int {\n\tmatch s {\n\tcase Dot:\n\t\t0\n\tcase Line as l:\n\t\tl.N\n\t}\n}\n" +
	"\ntype Box[T] { Value T }\n\nfunc Wrap[T](x T) Box[T] {\n\tBox[T]{Value: x}\n}\n\nfunc Boxed() Box[int] {\n\tWrap(F())\n}\n"

// clean drops the model's faults (hashes left out, copies missing or
// changed, Go modules and misnamed ergo.mod files, untrusted Go, functions
// that leak, a call of an unexported function), so more programs check and
// reach the snapshot and the lowering.
func (m *genModel) clean() {
	m.noSum, m.noCopy = map[string]bool{}, map[string]bool{}
	scrub := func(pkgs map[string]genPkg) {
		for folder, p := range pkgs {
			p.leak, p.extra = false, shapeSource
			pkgs[folder] = p
		}
	}
	m.trust = slices.Clone(m.paths)
	for _, path := range m.paths {
		for _, c := range m.mods[path] {
			c.tamper, c.modEdit, c.ergoMod = false, false, "module "+path+"\n"
			scrub(c.pkgs)
		}
		if c := m.local[path]; c != nil {
			c.ergoMod = "module " + path + "\n"
			scrub(c.pkgs)
		}
	}
	m.root.leak, m.root.secret = false, ""
	if m.lib != nil {
		m.lib.leak = false
	}
}

// snapshotReference counts the packages of a, a checked program, that a
// snapshot holds, read from the files: in the module cache, checked with
// no diagnostic, using no Go, importing only such packages.
func snapshotReference(a Analysis, cache string) int {
	held := map[string]bool{}
	for _, p := range a.Packages() {
		if !strings.HasPrefix(p.Dir, cache+string(filepath.Separator)) || p.Package == nil {
			continue
		}
		ok := true
		for _, f := range p.Files {
			text := a.Files()[f.Rel]
			for _, line := range strings.Split(text, "\n") {
				switch {
				case strings.HasPrefix(line, "import go "), strings.HasPrefix(line, "extern go "):
					ok = false
				case strings.HasPrefix(line, "import "):
					ok = ok && held[strings.TrimPrefix(line, "import ")]
				}
			}
		}
		held[p.Path] = ok
	}
	count := 0
	for _, ok := range held {
		if ok {
			count++
		}
	}
	return count
}
