package residue_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// rowInference matches a spec sentence that states row inference.
var rowInference = regexp.MustCompile(`(?i)inferred (error |capability )?rows?|infers each unwritten row|row inference|rows are inferred|is inferred for an unexported|inferred and unbounded`)

// TestExplicitSignatureDocs holds the docs to the explicit-signature
// decision (owner, 2026-11-12): NORTH_STAR.md carries the tiebreak and no
// pass-through elision, docs/decisions.md the decision, GLOSSARY.md the
// retired inferred row, and no line of docs/spec states row inference: the
// checker infers no row since EXPLICIT EX2. Mutant: a spec line that
// promises inference, or the elision text put back.
func TestExplicitSignatureDocs(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, c := range []struct{ file, text string }{
		{"NORTH_STAR.md", "**Explicit local signatures** beat **inference**"},
		{"docs/decisions.md", "q137"},
		{"docs/decisions.md", "q140"},
		{"GLOSSARY.md", "**Inferred row** (retired by decision"},
	} {
		if !strings.Contains(read(c.file), c.text) {
			t.Errorf("%s does not carry %q", c.file, c.text)
		}
	}
	if strings.Contains(read("NORTH_STAR.md"), "Elide only pass-through rows") {
		t.Error("NORTH_STAR.md still elides pass-through rows (q140 dropped it)")
	}
	specs, err := filepath.Glob(filepath.Join(repo, "docs", "spec", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range specs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if rowInference.MatchString(line) {
				rel, _ := filepath.Rel(repo, path)
				t.Errorf("%s:%d states row inference, which EXPLICIT EX2 removed (q137)", rel, i+1)
			}
		}
	}
}
