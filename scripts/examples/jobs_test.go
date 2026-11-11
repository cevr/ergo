package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
)

// examples/jobs gives each typed failure its status in one catch with no
// case _ (api/handle.ergo). This row adds a failure to answer's row in a
// copy of the program and checks the copy: the catch must be refused, at
// Handle's catch, naming the new failure. Mutant it catches, which no law
// or case does: a case _ arm in Handle's catch, which keeps every answer
// the model law sees and lets a new failure through with no status.
func TestJobsFailureNeedsAStatus(t *testing.T) {
	t.Parallel()
	src := filepath.Join("..", "..", "examples", "jobs")
	dst := t.TempDir()
	for _, rel := range []string{"ergo.mod", "jobs", "memory", "api"} {
		copyTree(t, filepath.Join(src, rel), filepath.Join(dst, rel))
	}
	handle := filepath.Join(dst, "api", "handle.ergo")
	text, err := os.ReadFile(handle)
	if err != nil {
		t.Fatal(err)
	}
	const row = "fails http.NotFound | WrongMethod"
	if strings.Count(string(text), row) != 1 {
		t.Fatalf("api/handle.ergo no longer declares answer's row as %q; update this row", row)
	}
	edited := strings.Replace(string(text), row, "fails Expired | http.NotFound | WrongMethod", 1) + "\ntype Expired {}\n"
	if err := os.WriteFile(handle, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := sketchfront.CheckDir(filepath.Join(dst, "api"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, d := range a.Diagnostics {
		found = append(found, d.Code+" "+d.File+": "+d.Message)
		if d.Code == "S103" && strings.HasSuffix(d.File, "handle.ergo") && strings.Contains(d.Message, "does not handle {Expired}") {
			if line := strings.Split(edited, "\n")[d.Span.Line-1]; !strings.Contains(line, "answer(r) catch {") {
				t.Errorf("S103 at line %d %q, want Handle's catch", d.Span.Line, line)
			}
			return
		}
	}
	t.Errorf("a failure with no status checks; diagnostics: %q", found)
}

// copyTree copies the regular files under from to to.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
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
