package closure

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The round-6 verifier's probe (closure-vr6_probe_test.go.txt), kept as a
// regression test and run the way the sweep runs a package with no observer
// (internal/testinputs): a test makes a link in its scratch directory to the
// checkout's docs and reads docs/tooling.md through it. Its log is cmd/go's
// in-process test log, which records the path as the test spelled it. The
// sweep removed the run's scratch before it classified the log, so the path
// then named an absent place in scratch and gave no entry. It uses only what
// the round-6 head had, so it runs there too, where it fails.

// scratchLinkTest is the fixture package's test, built without
// testinputs.Main, as internal/testinputs's tests are. SCRATCH_LINK "kept"
// makes the link in a directory of its own under TMPDIR, which the test
// leaves for the sweep to remove; "tempdir" makes it in t.TempDir, which the
// testing package removes when the test ends.
const scratchLinkTest = `package p

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadThroughAScratchLink(t *testing.T) {
	dir := t.TempDir()
	if os.Getenv("SCRATCH_LINK") == "kept" {
		var err error
		if dir, err = os.MkdirTemp("", "link"); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(os.Getenv("SCRATCH_LINK_DOCS"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(link, "tooling.md")); err != nil {
		t.Fatal(err)
	}
}
`

// TestScratchLinkReadFailsClosed: a read of docs/tooling.md through a link
// the test left in its scratch directory is outside the closure of a
// package with no identity, as an unidentified read that only an exact
// declared entry can cover.
func TestScratchLinkReadFailsClosed(t *testing.T) {
	needSandbox(t)
	f := newFixture(t)
	const pkg = "internal/testinputs"
	if _, ok := unobservable[pkg]; !ok {
		t.Fatalf("%s is not unobservable; the probe needs a package with no identity", pkg)
	}
	if err := os.WriteFile(filepath.Join(f.pkg, "p_test.go"), []byte(scratchLinkTest), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "p.test")
	build := exec.Command("go", "test", "-c", "-o", binary, "./internal/p")
	build.Dir = f.root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the fixture's test binary: %v\n%s", err, out)
	}
	run := func(where string) ([]entry, []entry) {
		stem := filepath.Join(t.TempDir(), "internal_testinputs")
		extra := []string{"SCRATCH_LINK=" + where, "SCRATCH_LINK_DOCS=" + filepath.Join(f.root, "docs")}
		es, err := runTests(f.root, []string{f.root}, pkg, f.pkg, stem, extra, binary, "-test.run=^TestReadThroughAScratchLink$")
		if err != nil {
			t.Fatalf("%s: want the run classified; got %v", where, err)
		}
		return es, newClosure(pkg, nil, nil).check(es, map[string]bool{})
	}
	es, outside := run("kept")
	t.Logf("kept: entries %v; outside the closure %v", es, outside)
	if want := (entry{opUnidentified + opOpen, "docs/tooling.md"}); !slices.Contains(outside, want) {
		t.Errorf("the test read docs/tooling.md through a link in its scratch, and the sweep has no %v outside the closure: %v", want, outside)
	}
	// A link in t.TempDir is gone before the test process exits, so the
	// log alone cannot tell where it led: the known limit of a package
	// with no identity (docs/tooling.md). Measured, not asserted.
	es, outside = run("tempdir")
	t.Logf("tempdir: entries %v; outside the closure %v", es, outside)
}
