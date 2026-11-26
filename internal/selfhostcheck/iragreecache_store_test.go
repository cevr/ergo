package selfhostcheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
)

// TestIRAgreeCacheServesOnlyItsKey proves the Go side's store serves a
// kept lowering only to the same inputs: an edit of the program's source
// or of a file of its module folder, the run build, an answer variable and
// ERGO_IR_AGREE_FRESH each miss, and a hit is the lowering a fresh run
// makes. Mutants it alone catches: testdata/mutants.json, rows
// agree-cache-* (not-run-crunch).
func TestIRAgreeCacheServesOnlyItsKey(t *testing.T) {
	t.Setenv(irAgreeCacheVariable, t.TempDir())
	t.Setenv(irAgreeFreshVariable, "")
	saved := irDigest
	t.Cleanup(func() { irDigest = saved })
	dir := t.TempDir()
	load := func(src string) irGoProgram {
		t.Helper()
		irDigest = &irDigests{trees: map[string]string{}, flats: map[string]string{}}
		if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := irspike.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return irGoProgram{u: u, dir: dir, module: dir, tests: true, reach: ownFuncs(u, map[string]bool{})}
	}
	c := &irLowerStore{}
	lower := func(pr irGoProgram) irLowered {
		t.Helper()
		l, err := c.lower(t, pr)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	misses := func(step string, pr irGoProgram) irLowered {
		t.Helper()
		hits := c.hits
		l := lower(pr)
		if c.hits != hits {
			t.Fatalf("%s: the store served a kept lowering", step)
		}
		return l
	}

	pr := load("func main() {}\nfunc Next(n int) int { n + 1 }\ntest Next { assert Next(2) == 3 }\n")
	first := misses("first run", pr)
	if got := lower(pr); c.hits != 1 || !reflect.DeepEqual(got, first) {
		t.Fatalf("rerun: hits %d, equal %v", c.hits, reflect.DeepEqual(got, first))
	}
	edited := misses("source edited", load("func main() {}\nfunc Next(n int) int { n + 2 }\ntest Next { assert Next(2) == 4 }\n"))
	if reflect.DeepEqual(edited, first) {
		t.Fatalf("the edit changed no lowering; the fixture proves nothing: %+v", first)
	}
	pr = load("func main() {}\nfunc Next(n int) int { n + 1 }\ntest Next { assert Next(2) == 3 }\n")
	lower(pr)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "host.go"), []byte("package sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pr = load("func main() {}\nfunc Next(n int) int { n + 1 }\ntest Next { assert Next(2) == 3 }\n")
	misses("module file added", pr)
	run := pr
	run.tests = false
	misses("run build", run)
	t.Setenv("ERGO_AGREE_CACHE_PROBE", "1")
	misses("answer variable", pr)
	t.Setenv(irAgreeFreshVariable, "1")
	misses("fresh", pr)
}

// TestIRDumpStoreRespellsTheRepository proves irdump's kept output names
// this checkout's paths when read back, and that an output naming the
// run's temporary folder is not kept. Mutants: rows agree-cache-dump-*.
func TestIRDumpStoreRespellsTheRepository(t *testing.T) {
	t.Setenv(irAgreeCacheVariable, t.TempDir())
	t.Setenv(irAgreeFreshVariable, "")
	bin := filepath.Join(t.TempDir(), "irdump")
	if err := os.WriteFile(bin, []byte("irdump image"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := []string{"@pkg x", "P 0 " + filepath.Join(repoRoot(), "examples", "x", "main.ergo")}
	irDumpKeep(bin, "k", out, "/nonexistent/agree-tmp")
	got, ok := irDumpStored(bin, "k")
	if !ok || !reflect.DeepEqual(got, []string{"@pkg x", "P 0 " + irRootPrefix() + "examples/x/main.ergo"}) {
		t.Fatalf("stored %v %q", ok, got)
	}
	irDumpKeep(bin, "t", []string{"P 0 /nonexistent/agree-tmp/ext1"}, "/nonexistent/agree-tmp")
	if _, ok := irDumpStored(bin, "t"); ok {
		t.Fatal("an output naming the run's temporary folder was kept")
	}
	if _, ok := irDumpStored(bin, "other"); ok {
		t.Fatal("another key was served")
	}
}
