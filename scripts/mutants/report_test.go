package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyCopyComparesLinksByTarget: at a run's end a symbolic link of
// the checkout, restored as the checkout has it, is equal to it: it is
// compared by its target, not by the file it points to (IR1, PF: every run
// with a `go run ./scripts/linked` row failed on
// conformance/upstream/foldkit-d21db423/CLAUDE.md -> AGENTS.md). A file
// left changed is named with its first differing line. Mutants
// (testdata/mutants.json): vc-link-read-through, vc-no-first-diff.
func TestVerifyCopyComparesLinksByTarget(t *testing.T) {
	dst := t.TempDir()
	for name, data := range map[string]string{"a.go": "package a\nvar x = 2\n", "up/AGENTS.md": "# agents\n"} {
		p := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(dst, "up", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	e := &engine{files: []treeFile{
		{path: "up/AGENTS.md", mode: 0o644, data: []byte("# agents\n")},
		{path: "up/CLAUDE.md", mode: fs.ModeSymlink | 0o777, data: []byte("AGENTS.md")},
	}}
	touched := map[string]bool{"a.go": true, "up/AGENTS.md": true, "up/CLAUDE.md": true}
	if err := e.verifyCopy(dst, touched); err != nil {
		t.Errorf("a restored link and its file: %v", err)
	}
	e.files = append(e.files, treeFile{path: "a.go", mode: 0o644, data: []byte("package a\nvar x = 1\n")})
	want := "a mutant was not restored in " + dst + `: a.go (line 2: checkout "var x = 1", copy "var x = 2")`
	if err := e.verifyCopy(dst, touched); err == nil || err.Error() != want {
		t.Errorf("a changed a.go: %v\nwant %s", err, want)
	}
}

// TestRedBaselineNamesItsFirstError: a row left unjudged by a test that
// fails on the unmutated tree names that test's first error line, not its
// verdict alone (I22: TestTaskGenRegeneratesMemberGen was red in the
// runner and passed outside it, and the report gave no cause). Mutant
// (testdata/mutants.json): rd-first-error-dropped.
func TestRedBaselineNamesItsFirstError(t *testing.T) {
	out := "=== RUN   TestX\n    x_test.go:12: got 1, want 2\n    x_test.go:13: later\n--- FAIL: TestX (0.01s)\nFAIL\n"
	e := &engine{base: map[string]*covTest{"cmd/ergo:TestX": {ID: "cmd/ergo:TestX", Failed: true, Detail: baseDetail(outcome{reason: "fail", tail: out})}}}
	want := "cmd/ergo:TestX (x_test.go:12: got 1, want 2) fails on the unmutated tree"
	if got := e.redDetail([]string{"cmd/ergo:TestX"}); got != want {
		t.Errorf("redDetail: %q\nwant %q", got, want)
	}
}
