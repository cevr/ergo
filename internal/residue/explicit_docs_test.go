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

// explicitMark is the status every such spec line carries until the unit
// that deletes inference rewrites it.
const explicitMark = "Row inference is superseded by [explicit local signatures]"

// TestExplicitSignatureDocs holds the docs to the explicit-signature
// decision (owner, 2026-11-12): NORTH_STAR.md carries the tiebreak and no
// pass-through elision, docs/decisions.md the decision, GLOSSARY.md the
// retired inferred row, and every line of docs/spec that states row
// inference carries the status mark. Mutant: a spec line that promises
// inference with no mark, or the elision text put back.
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
			if rowInference.MatchString(line) && !strings.Contains(line, explicitMark) {
				rel, _ := filepath.Rel(repo, path)
				t.Errorf("%s:%d states row inference without the status mark %q", rel, i+1, explicitMark)
			}
		}
	}
}
