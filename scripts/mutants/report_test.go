package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	e := &engine{base: map[string]*covTest{"cmd/ergo:TestX": {ID: "cmd/ergo:TestX", Failed: true, Detail: baseDetail("cmd/ergo:TestX", outcome{reason: "fail", tail: out})}}}
	want := "cmd/ergo:TestX (x_test.go:12: got 1, want 2) fails on the unmutated tree"
	if got := e.redDetail([]string{"cmd/ergo:TestX"}); got != want {
		t.Errorf("redDetail: %q\nwant %q", got, want)
	}
	// A command killer's skip and ok lines are not its failure; its FAIL
	// line is (I22 882: the examples runner's full-tier skip line was
	// named; rd-skip-named-as-failure).
	ex := "skip modules/work: test git/checkout: the full tier runs it (full-tier)\nok modules/work: fmt\nFAIL modules/work: test: copy: law refuted\n"
	if got, want := firstError(ex), "FAIL modules/work: test: copy: law refuted"; got != want {
		t.Errorf("firstError of an examples run: %q, want %q", got, want)
	}
	if got := firstError("skip modules/work: test git/checkout: the full tier runs it (full-tier)\nexit status 1\n"); got != "exit status 1" {
		t.Errorf("firstError named a skip line: %q", got)
	}
}

// TestRedBaselineKeepsItsWholeOutput: a child that fails has its whole
// output kept in a file its Detail names, though the display tail drops
// the middle (OWN1's no-verdict red lost its cause there; SUPERVISE-2's
// TestStdActorLawsRun likewise), and a child that passes leaves none.
// The child is sh, so it builds no Go. Mutant (testdata/mutants.json):
// red-output-dropped.
func TestRedBaselineKeepsItsWholeOutput(t *testing.T) {
	red := t.TempDir()
	t.Setenv(redVariable, red)
	work := t.TempDir()
	e := &engine{cfg: Config{Work: work}}
	// 40 KiB before and after the error line: more than the tail keeps.
	script := "yes filler | head -c 40000; echo; echo MIDDLE-ERROR; yes filler | head -c 40000; echo; exit 1"
	o := e.exec(context.Background(), work, []string{"sh", "-c", script}, os.Environ(), time.Hour)
	if o.reason == "" || strings.Contains(o.tail, "MIDDLE-ERROR") {
		t.Fatalf("the child: reason %q, tail holds the middle %v; want a failure whose tail dropped it", o.reason, strings.Contains(o.tail, "MIDDLE-ERROR"))
	}
	detail := baseDetail("cmd/ergo:TestX", o)
	_, path, ok := strings.Cut(firstLine(detail), "(whole output: ")
	path = strings.TrimSuffix(path, ")")
	if !ok || !strings.HasPrefix(path, red) {
		t.Fatalf("the Detail names no kept output under %s: %q", red, firstLine(detail))
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "MIDDLE-ERROR") {
		t.Errorf("the kept output %s: %v; want the whole output with its middle", path, err)
	}
	if ok := e.exec(context.Background(), work, []string{"sh", "-c", "echo fine"}, os.Environ(), time.Hour); ok.reason != "" || ok.log != "" {
		t.Errorf("a passing child: reason %q, kept %q; want no file", ok.reason, ok.log)
	}
}
