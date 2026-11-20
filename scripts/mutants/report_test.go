package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyCopyNamesWhatChanged: at a run's end a file a row edited that
// differs from the checkout is "a mutant was not restored"; a file only a
// regenerate step's folder brought into the check, changed after the
// runner restored it, is a test or step writing the copy, not a mutant
// (IR1: a file under conformance/upstream was reported as an unrestored
// mutant). Each names its first differing line. Mutants
// (testdata/mutants.json): vc-written-as-mutant, vc-no-first-diff.
func TestVerifyCopyNamesWhatChanged(t *testing.T) {
	dst := t.TempDir()
	for name, data := range map[string]string{"a.go": "package a\nvar x = 2\n", "up/b.txt": "one\nthree\n"} {
		p := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := &engine{files: []treeFile{
		{path: "a.go", data: []byte("package a\nvar x = 1\n")},
		{path: "up/b.txt", data: []byte("one\ntwo\n")},
	}}
	err := e.verifyCopy(dst, map[string]bool{"a.go": true, "up/b.txt": true}, map[string]bool{"a.go": true})
	if err == nil {
		t.Fatal("two changed files: no error")
	}
	msg := err.Error()
	for _, want := range []string{
		"a mutant was not restored in " + dst + `: a.go (line 2: checkout "var x = 1", copy "var x = 2")`,
		"a test or step wrote a file of the checkout in the copy " + dst,
		`up/b.txt (line 2: checkout "two", copy "three")`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("verifyCopy: %s\nwant it to hold %q", msg, want)
		}
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
