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

// TestRegenerateWritesThenRestores: a row's regenerate step (MUT-REGEN)
// runs in its directory of the worker's copy, so the killers read what the
// mutated generator writes; after the row, every file of the checkout
// under that directory is the checkout's again and a file the step added
// is gone; a step that fails names why, and the row is invalid. The step
// here is cp, standing for go generate: the mechanism is the runner's, not
// the generator's.
//
// Mutants (testdata/mutants.json): regen-output-kept, regen-new-file-kept,
// regen-failure-ignored.
func TestRegenerateWritesThenRestores(t *testing.T) {
	// The copy is under the work directory, as workerPool makes it:
	// restoreUnder removes files only there.
	work := t.TempDir()
	root := filepath.Join(work, "w0", "src")
	files := map[string]string{"gen/in.txt": "mutated\n", "gen/out.txt": "original\n", "other/x.txt": "x\n"}
	for name, text := range files {
		writeFile(t, filepath.Join(root, name), text)
	}
	e := &engine{cfg: Config{CoverDeadline: time.Minute, Work: work}}
	for _, name := range []string{"gen/in.txt", "gen/out.txt", "other/x.txt"} {
		data := files[name]
		if name == "gen/in.txt" {
			data = "original input\n" // the checkout's; the copy holds the mutant
		}
		e.files = append(e.files, treeFile{path: name, mode: 0o644, data: []byte(data)})
	}
	// A link of the checkout under the step's folder (conformance/upstream's
	// CLAUDE.md -> AGENTS.md) stays a link; the step's own new link goes.
	if err := os.Symlink("out.txt", filepath.Join(root, "gen", "link.txt")); err != nil {
		t.Fatal(err)
	}
	e.files = append(e.files, treeFile{path: "gen/link.txt", mode: fs.ModeSymlink | 0o777, data: []byte("out.txt")})
	w := &worker{e: e, root: root, touched: map[string]bool{}}
	defer func() {
		if target, err := os.Readlink(filepath.Join(root, "gen", "link.txt")); err != nil || target != "out.txt" {
			t.Errorf("after the row the checkout's link gen/link.txt is %q, %v; want it to name out.txt (regen-link-removed)", target, err)
		}
		if err := e.verifyCopy(root, w.touched); err != nil {
			t.Errorf("verifyCopy after the restore: %v", err)
		}
	}()
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(data)
	}
	ctx := context.Background()
	if detail := w.regenerate(ctx, &Regenerate{Dir: "gen", Argv: []string{"sh", "-c", "cp in.txt out.txt && cp in.txt added.txt"}}); detail != "" {
		t.Fatalf("regenerate: %s", detail)
	}
	if got := read("gen/out.txt"); got != "mutated\n" {
		t.Fatalf("after the step out.txt is %q; want the mutated generator's output", got)
	}
	if !w.touched["gen/out.txt"] {
		t.Error("the step's directory is not marked touched: verifyCopy would not check its files")
	}
	if err := w.restoreUnder("gen"); err != nil {
		t.Fatal(err)
	}
	if got := read("gen/out.txt"); got != "original\n" {
		t.Errorf("after the row out.txt is %q; want the checkout's", got)
	}
	if got := read("gen/in.txt"); got != "original input\n" {
		t.Errorf("after the row in.txt is %q; want the checkout's", got)
	}
	if got := read("gen/added.txt"); !strings.HasPrefix(got, "<") {
		t.Errorf("the file the step added is still there: %q", got)
	}
	if got := read("other/x.txt"); got != "x\n" {
		t.Errorf("a file outside the step's directory changed: %q", got)
	}
	if detail := w.regenerate(ctx, &Regenerate{Dir: "gen", Argv: []string{"sh", "-c", "echo broken generator >&2; exit 3"}}); !strings.Contains(detail, "broken generator") {
		t.Errorf("a failing step: %q; want its output in the reason", detail)
	}
	if detail := w.regenerate(ctx, &Regenerate{Dir: "../gen", Argv: []string{"true"}}); detail == "" {
		t.Error("a step outside the checkout ran")
	}
}
